package app

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"wisp/internal/agentstatus"
	"wisp/internal/cli"
	"wisp/internal/lock"
	"wisp/internal/process"
)

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
	for _, variant := range []string{"valid", "ready", "invalid", "unsupported", "stale", "foreign", "stopped", "missing", "error"} {
		t.Run(variant, func(t *testing.T) {
			root := t.TempDir()
			runtimeRoot, err := lock.RuntimeRoot("", root, os.Getuid())
			if err != nil {
				t.Fatal(err)
			}
			r, err := agentstatus.Register(runtimeRoot, agentstatus.Metadata{UID: os.Getuid(), Repo: "/repo", SandboxName: "sandbox", ProjectHash: strings.Repeat("a", 64), ComposeProject: "compose"})
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
				if variant == "error" {
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
			code := a.Execute(context.Background(), cli.Request{Command: cli.CommandAgents})
			if variant == "error" {
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
			if variant == "valid" || variant == "ready" || variant == "invalid" || variant == "unsupported" {
				want := map[string]any{
					"id": r.RunID, "sandbox_id": r.SandboxName, "repo": r.Repo,
					"agent": "opencode", "state": "unknown", "reporter": variant,
				}
				if variant == "valid" {
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
		if entry.RunID != testEnvironmentValue(c.Env, "WISP_RUN_ID") || entry.Repo != projectDir {
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
