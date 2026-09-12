package app

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"wisp/internal/agentstatus"
	"wisp/internal/cli"
	"wisp/internal/lock"
	"wisp/internal/process"
)

type agentSnapshotWriter func([]byte) (int, error)

func (f agentSnapshotWriter) Write(p []byte) (int, error) { return f(p) }

func TestWatchAgentSnapshots(t *testing.T) {
	const empty = "{\"schema_version\":1,\"agents\":[]}\n"
	const working = "{\"schema_version\":1,\"agents\":[{\"id\":\"run\",\"state\":\"working\"}]}\n"
	const idle = "{\"schema_version\":1,\"agents\":[{\"id\":\"run\",\"state\":\"idle\"}]}\n"
	snapshots := []string{empty, empty, working, working, idle, empty, empty}
	priorOutput := []string{"", empty, empty, empty + working, empty + working, empty + working + idle, empty + working + idle + empty}
	ticks := make(chan time.Time, 1)
	var stdout bytes.Buffer
	calls := 0
	err := writeAgentSnapshots(context.Background(), &stdout, ticks, func(ctx context.Context) ([]byte, error) {
		deadline, ok := ctx.Deadline()
		if !ok || time.Until(deadline) <= 0 || time.Until(deadline) > 10*time.Second {
			t.Fatalf("collection deadline=%v present=%v", deadline, ok)
		}
		if calls >= len(snapshots) {
			t.Fatal("unexpected collection")
		}
		if stdout.String() != priorOutput[calls] {
			t.Fatalf("collection %d output=%q want=%q", calls, stdout.String(), priorOutput[calls])
		}
		snapshot := snapshots[calls]
		calls++
		if calls == len(snapshots) {
			close(ticks)
		} else {
			ticks <- time.Time{}
		}
		return []byte(snapshot), nil
	})
	if err != nil || calls != len(snapshots) || stdout.String() != empty+working+idle+empty {
		t.Fatalf("calls=%d output=%q err=%v", calls, stdout.String(), err)
	}
}

func TestWatchAgentSnapshotFailuresAndCancellation(t *testing.T) {
	failure := errors.New("Docker unavailable")
	for _, variant := range []string{"initial error", "later error", "timeout", "cancel before", "cancel collecting", "cancel waiting", "write error", "short write"} {
		t.Run(variant, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			if variant == "cancel before" {
				cancel()
			}
			ticks := make(chan time.Time, 1)
			var stdout bytes.Buffer
			calls := 0
			writer := agentSnapshotWriter(func(p []byte) (int, error) {
				switch variant {
				case "write error":
					return 0, io.ErrClosedPipe
				case "short write":
					return 0, nil
				case "cancel waiting":
					cancel()
				}
				return stdout.Write(p)
			})
			err := writeAgentSnapshots(ctx, writer, ticks, func(collectionCtx context.Context) ([]byte, error) {
				calls++
				if calls > 2 {
					t.Fatal("unexpected collection")
				}
				if variant == "initial error" || variant == "later error" && calls == 2 {
					return nil, failure
				}
				if variant == "timeout" {
					return nil, context.DeadlineExceeded
				}
				if variant == "cancel collecting" {
					cancel()
					<-collectionCtx.Done()
					return nil, collectionCtx.Err()
				}
				if variant == "later error" {
					ticks <- time.Time{}
				}
				return []byte("{\"schema_version\":1,\"agents\":[]}\n"), nil
			})
			var wantErr error
			switch variant {
			case "initial error", "later error":
				wantErr = failure
			case "timeout":
				wantErr = context.DeadlineExceeded
			case "write error":
				wantErr = io.ErrClosedPipe
			case "short write":
				wantErr = io.ErrShortWrite
			}
			wantOutput := ""
			if variant == "later error" || variant == "cancel waiting" {
				wantOutput = "{\"schema_version\":1,\"agents\":[]}\n"
			}
			if !errors.Is(err, wantErr) || stdout.String() != wantOutput || variant == "cancel before" && calls != 0 {
				t.Fatalf("calls=%d output=%q err=%v want=%v", calls, stdout.String(), err, wantErr)
			}
		})
	}
}

func TestAgentsWatchNeedsNeitherDockerConfigNorAssets(t *testing.T) {
	var stdout, stderr bytes.Buffer
	runner := &recordingRunner{capture: func(process.Command) ([]byte, []byte, error) {
		t.Fatal("unexpected process")
		return nil, nil, nil
	}}
	a := newFixtureApp(t, t.TempDir(), runner, &stdout, &stderr)
	a.deps.RuntimeAssets = nil
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	a.deps.Stdout = agentSnapshotWriter(func(p []byte) (int, error) {
		cancel()
		return stdout.Write(p)
	})
	code := a.Execute(ctx, cli.Request{Command: cli.CommandAgents, Agents: cli.AgentsRequest{Watch: true}})
	if code != 0 || stdout.String() != "{\"schema_version\":1,\"agents\":[]}\n" || stderr.Len() != 0 {
		t.Fatalf("code=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
	}
}

func TestAgentsEmptyNeedsNeitherDockerConfigNorAssets(t *testing.T) {
	root := t.TempDir()
	var stdout, stderr bytes.Buffer
	runner := &recordingRunner{capture: func(process.Command) ([]byte, []byte, error) {
		t.Fatal("unexpected process")
		return nil, nil, nil
	}}
	a := newFixtureApp(t, root, runner, &stdout, &stderr)
	a.deps.RuntimeAssets = nil
	if code := a.Execute(context.Background(), cli.Request{Command: cli.CommandAgents}); code != 0 || stdout.String() != "{\"schema_version\":1,\"agents\":[]}\n" || stderr.Len() != 0 {
		t.Fatalf("code=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
	}
}

func TestAgentsRequiresRunningOwnedMatchingRun(t *testing.T) {
	for _, variant := range []string{"valid", "pi", "legacy", "watch valid", "ready", "invalid", "unsupported", "stale", "foreign", "stopped", "missing", "error", "watch error"} {
		t.Run(variant, func(t *testing.T) {
			root := t.TempDir()
			runtimeRoot, err := lock.RuntimeRoot("", root, os.Getuid())
			if err != nil {
				t.Fatal(err)
			}
			// A stored launch PID distinct from this listing/watching process.
			pid := os.Getpid() + 1000
			if variant == "legacy" {
				pid = 0
			}
			agentName := ""
			if variant == "pi" {
				agentName = "pi"
			}
			r, err := agentstatus.Register(runtimeRoot, agentstatus.Metadata{UID: os.Getuid(), HostPID: pid, Repo: "/repo", SandboxName: "sandbox", ProjectHash: strings.Repeat("a", 64), ComposeProject: "compose", Agent: agentName})
			if err != nil {
				t.Fatal(err)
			}
			defer r.Cleanup()
			report := map[string]string{
				"ready":       `{"schema_version":1,"state":"waiting","reason":"question","updated_at":"2020-01-01T00:00:00Z","summary":"private"}`,
				"invalid":     `{"schema_version":1,"state":"private","updated_at":"2020-01-01T00:00:00Z"}`,
				"unsupported": `{"schema_version":2,"state":"waiting","reason":"question","updated_at":"2020-01-01T00:00:00Z"}`,
			}[variant]
			if report != "" {
				if err := os.WriteFile(filepath.Join(r.StatusDir, "agent.json"), []byte(report), 0600); err != nil {
					t.Fatal(err)
				}
			}
			labels := composeProjectLabels(r.ProjectHash, r.UID, "sandbox", r.ComposeProject)
			labels[agentstatus.RunLabel] = r.RunID
			if variant == "stale" {
				labels[agentstatus.RunLabel] = "old-run"
			}
			if variant == "foreign" {
				labels["wisp.managed"] = "false"
			}
			runner := &recordingRunner{capture: func(c process.Command) ([]byte, []byte, error) {
				if len(c.Args) != 3 || c.Args[0] != "container" || c.Args[1] != "inspect" || c.Args[2] != "sandbox" {
					t.Fatalf("unexpected process: %+v", c)
				}
				if variant == "error" || variant == "watch error" {
					return nil, nil, errors.New("daemon unavailable")
				}
				if variant == "missing" {
					return nil, []byte("No such container"), errors.New("exit 1")
				}
				data, err := json.Marshal([]any{map[string]any{"Name": "/sandbox", "Config": map[string]any{"Labels": labels}, "State": map[string]any{"Running": variant != "stopped"}}})
				return data, nil, err
			}}
			var stdout, stderr bytes.Buffer
			a := newFixtureApp(t, root, runner, &stdout, &stderr)
			a.deps.RuntimeAssets = nil
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			if variant == "watch valid" {
				a.deps.Stdout = agentSnapshotWriter(func(p []byte) (int, error) {
					cancel()
					return stdout.Write(p)
				})
			}
			code := a.Execute(ctx, cli.Request{Command: cli.CommandAgents, Agents: cli.AgentsRequest{Watch: strings.HasPrefix(variant, "watch ")}})
			if variant == "error" || variant == "watch error" {
				if code != 1 || stdout.Len() != 0 || stderr.Len() == 0 {
					t.Fatalf("code=%d out=%s err=%s", code, &stdout, &stderr)
				}
				return
			}
			var output struct {
				SchemaVersion int              `json:"schema_version"`
				Agents        []map[string]any `json:"agents"`
			}
			if code != 0 || json.Unmarshal(stdout.Bytes(), &output) != nil {
				t.Fatalf("code=%d out=%s err=%s", code, &stdout, &stderr)
			}
			if output.SchemaVersion != 1 {
				t.Fatalf("output=%s", &stdout)
			}
			if variant == "valid" || variant == "pi" || variant == "legacy" || variant == "watch valid" || variant == "ready" || variant == "invalid" || variant == "unsupported" {
				wantAgent := "opencode"
				if variant == "pi" {
					wantAgent = "pi"
				}
				want := map[string]any{
					"id": r.RunID, "sandbox_id": r.SandboxName, "repo": r.Repo,
					"agent": wantAgent, "state": "unknown", "reporter": variant,
				}
				if pid > 0 {
					want["host_process"] = map[string]any{"pid": float64(pid)}
				}
				if variant == "valid" || variant == "pi" || variant == "legacy" || variant == "watch valid" {
					want["reporter"] = "missing"
				}
				if variant == "ready" {
					want["state"] = "waiting"
					want["reason"] = "question"
					want["updated_at"] = "2020-01-01T00:00:00Z"
				}
				if len(output.Agents) != 1 || !reflect.DeepEqual(output.Agents[0], want) {
					t.Fatalf("output=%s", &stdout)
				}
			} else if len(output.Agents) != 0 {
				t.Fatalf("output=%s", &stdout)
			}
		})
	}
}

func TestRunRegistrationLifecycle(t *testing.T) {
	root, configPath, projectDir := applicationFixture(t)
	fake := &lifecycleFake{}
	var stdout, stderr bytes.Buffer
	a := newFixtureApp(t, root, fake.runner(t), &stdout, &stderr)
	runtimeRoot, err := lock.RuntimeRoot("", root, os.Getuid())
	if err != nil {
		t.Fatal(err)
	}
	observed := false
	fake.attachedError = func(c process.Command) error {
		entries, err := agentstatus.List(runtimeRoot, os.Getuid())
		if err != nil || len(entries) != 1 {
			t.Fatalf("entries=%+v err=%v", entries, err)
		}
		entry := entries[0]
		if entry.RunID != testEnvironmentValue(c.Env, "WISP_RUN_ID") || entry.Repo != projectDir || entry.HostPID != os.Getpid() {
			t.Fatalf("entry=%+v", entry)
		}
		if _, err := os.Stat(filepath.Join(runtimeRoot, "agents", entry.RunID, "status")); err != nil {
			t.Fatal(err)
		}
		observed = true
		return exitCodeError(23)
	}
	code := a.Execute(context.Background(), cli.Request{Command: cli.CommandRun, Run: cli.RunRequest{Directory: projectDir, ConfigPath: configPath}})
	if code != 23 || !observed {
		t.Fatalf("code=%d observed=%v stderr=%s", code, observed, &stderr)
	}
	entries, err := agentstatus.List(runtimeRoot, os.Getuid())
	if err != nil || len(entries) != 0 {
		t.Fatalf("entries after cleanup=%+v err=%v", entries, err)
	}
}

func TestRunRegistrationPrecedesDockerAndCleansEarlyFailure(t *testing.T) {
	root, configPath, projectDir := applicationFixture(t)
	var stdout, stderr bytes.Buffer
	runtimeRoot, err := lock.RuntimeRoot("", root, os.Getuid())
	if err != nil {
		t.Fatal(err)
	}
	dockerCalls := 0
	runner := &recordingRunner{capture: func(c process.Command) ([]byte, []byte, error) {
		if c.Path == "git" {
			return nil, nil, errors.New("not a worktree")
		}
		dockerCalls++
		entries, err := agentstatus.List(runtimeRoot, os.Getuid())
		if err != nil || len(entries) != 1 {
			t.Fatalf("registration missing before Docker: %+v %v", entries, err)
		}
		return nil, nil, errors.New("Docker unavailable")
	}}
	a := newFixtureApp(t, root, runner, &stdout, &stderr)
	code := a.Execute(context.Background(), cli.Request{Command: cli.CommandRun, Run: cli.RunRequest{Directory: projectDir, ConfigPath: configPath}})
	if code != 1 || dockerCalls == 0 {
		t.Fatalf("code=%d Docker calls=%d", code, dockerCalls)
	}
	entries, err := agentstatus.List(runtimeRoot, os.Getuid())
	if err != nil || len(entries) != 0 {
		t.Fatalf("leaked registration: %+v %v", entries, err)
	}
	code = a.Execute(context.Background(), cli.Request{Command: cli.CommandRun, Run: cli.RunRequest{Directory: filepath.Join(root, "missing"), ConfigPath: configPath}})
	entries, err = agentstatus.List(runtimeRoot, os.Getuid())
	if code != 1 || err != nil || len(entries) != 0 {
		t.Fatalf("invalid input: code=%d entries=%+v err=%v", code, entries, err)
	}
}
