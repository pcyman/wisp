package app

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"wisp/internal/cli"
	"wisp/internal/docker"
	"wisp/internal/lock"
	"wisp/internal/process"
	"wisp/internal/project"
)

func TestCentralRequiresTTYBeforePlanningOrDocker(t *testing.T) {
	runner := &recordingRunner{capture: func(process.Command) ([]byte, []byte, error) { t.Fatal("unexpected process"); return nil, nil, nil }}
	a, err := New(Dependencies{Runner: runner, Stdin: &bytes.Buffer{}, Stdout: &bytes.Buffer{}, Stderr: &bytes.Buffer{}, InvocationDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	status, err := a.central(context.Background(), "")
	if status != 1 || err == nil || !strings.Contains(err.Error(), "interactive terminal") {
		t.Fatalf("status=%d err=%v", status, err)
	}
}

func TestCentralPlansRejectDuplicateWorktreesBeforeDocker(t *testing.T) {
	root, path, dir := applicationFixture(t)
	alias := filepath.Join(root, "alias")
	if err := os.Symlink(dir, alias); err != nil {
		t.Fatal(err)
	}
	contents := fmt.Sprintf("schema_version=1\n[[central.projects]]\nname='one'\npath=%q\n[[central.projects]]\nname='two'\npath=%q\n", dir, alias)
	if err := os.WriteFile(path, []byte(contents), 0600); err != nil {
		t.Fatal(err)
	}
	runner := &recordingRunner{capture: func(c process.Command) ([]byte, []byte, error) {
		if c.Path != "git" {
			t.Fatalf("planning invoked %s", c.Path)
		}
		return nil, nil, errors.New("not a repository")
	}}
	a := newFixtureApp(t, root, runner, &bytes.Buffer{}, &bytes.Buffer{})
	if _, err := a.centralPlans(context.Background(), path); err == nil || !strings.Contains(err.Error(), "same worktree") {
		t.Fatalf("err=%v", err)
	}
}

func TestCentralDetachedLifecycle(t *testing.T) {
	for _, agent := range []string{"pi", "opencode"} {
		for _, aws := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/aws=%t", agent, aws), func(t *testing.T) {
				root, path, dir := applicationFixture(t)
				if !aws {
					if err := os.WriteFile(path, []byte("schema_version=1\n"), 0600); err != nil {
						t.Fatal(err)
					}
				}
				fake := &lifecycleFake{}
				runner := fake.runner(t)
				a := newFixtureApp(t, root, runner, &bytes.Buffer{}, &bytes.Buffer{})
				plan, err := PlanRun(context.Background(), cli.RunRequest{Directory: dir, ConfigPath: path, Agent: agent}, a.planOptions())
				if err != nil {
					t.Fatal(err)
				}
				originalCommand := append([]string(nil), plan.Command...)
				fake.project = plan.Project
				original := runner.capture
				started := false
				runID := ""
				runner.capture = func(c process.Command) ([]byte, []byte, error) {
					if i := indexSlice(c.Args, []string{"run", "--detach", "--no-TTY"}); i >= 0 {
						if indexSlice(c.Args, []string{"--rm"}) >= 0 {
							t.Fatal("detached container removed before exit inspection")
						}
						data, err := os.ReadFile(c.Args[i-1])
						if err != nil {
							return nil, nil, err
						}
						var override struct {
							Services map[string]struct {
								Command []string `json:"command"`
							} `json:"services"`
						}
						if err := json.Unmarshal(data, &override); err != nil {
							return nil, nil, err
						}
						want := append([]string{"/usr/local/bin/wisp-central-session"}, originalCommand...)
						if !reflect.DeepEqual(override.Services["sandbox"].Command, want) {
							t.Fatalf("command=%v want %v", override.Services["sandbox"].Command, want)
						}
						runID = testEnvironmentValue(c.Env, "WISP_RUN_ID")
						if runID == "" {
							t.Fatal("missing run ID")
						}
						started = true
						return []byte("sandbox-id\n"), nil, nil
					}
					if started && reflect.DeepEqual(c.Args, []string{"container", "inspect", plan.Project.ContainerName}) {
						labels := composeProjectLabels(plan.Project.Hash, plan.UID, "sandbox", plan.Project.ComposeProject)
						labels["wisp.run-id"] = runID
						data, err := json.Marshal([]any{map[string]any{"Id": "sandbox-id", "Name": "/" + plan.Project.ContainerName, "Config": map[string]any{"Labels": labels}, "State": map[string]any{"Running": true}}})
						return data, nil, err
					}
					if reflect.DeepEqual(c.Args, []string{"exec", "sandbox-id", "tmux", "has-session", "-t", "wisp"}) {
						return nil, nil, nil
					}
					if indexSlice(c.Args, []string{"down", "--remove-orphans"}) >= 0 {
						started = false
					}
					return original(c)
				}
				ctx, cancel := context.WithCancel(context.Background())
				defer cancel()
				readyCount := 0
				code, err := a.runPlan(ctx, plan, func(s centralSandbox) {
					readyCount++
					if s.ID != "sandbox-id" || s.Labels["wisp.run-id"] != runID {
						t.Fatalf("sandbox=%#v", s)
					}
					runtimeRoot, err := lock.RuntimeRoot(a.env.XDGRuntimeDir, a.env.TempDir, plan.UID)
					if err != nil {
						t.Fatal(err)
					}
					if held, err := lock.Try(runtimeRoot, plan.Project.LockKey(plan.UID)); !errors.Is(err, lock.ErrContended) {
						if held != nil {
							_ = held.Close()
						}
						t.Fatalf("project lock not retained: %v", err)
					}
					if fake.downCount != 0 {
						t.Fatal("cleaned while agent alive")
					}
					cancel()
				})
				if code != 0 || err != nil || readyCount != 1 || fake.downCount != 1 {
					t.Fatalf("code=%d err=%v ready=%d down=%d", code, err, readyCount, fake.downCount)
				}
				if !reflect.DeepEqual(plan.Command, originalCommand) || plan.Environment["WISP_RUN_ID"] != "" {
					t.Fatal("central mutated its reusable plan")
				}
				if len(runner.attaches) != 0 {
					t.Fatal("startup took interactive terminal")
				}
				if (fake.upCount == 1) != aws {
					t.Fatalf("broker starts=%d aws=%t", fake.upCount, aws)
				}
			})
		}
	}
}

func TestCentralCancellationDuringStartupPreservesCleanupFailure(t *testing.T) {
	for _, cleanupFails := range []bool{false, true} {
		t.Run(fmt.Sprint(cleanupFails), func(t *testing.T) {
			root, path, dir := applicationFixture(t)
			if err := os.WriteFile(path, []byte("schema_version=1\n"), 0600); err != nil {
				t.Fatal(err)
			}
			fake := &lifecycleFake{}
			if cleanupFails {
				fake.cleanupError = func(process.Command) error { return errors.New("cleanup failed") }
			}
			runner := fake.runner(t)
			a := newFixtureApp(t, root, runner, &bytes.Buffer{}, &bytes.Buffer{})
			plan, err := PlanRun(context.Background(), cli.RunRequest{Directory: dir, ConfigPath: path}, a.planOptions())
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			original := runner.capture
			runner.capture = func(c process.Command) ([]byte, []byte, error) {
				if indexSlice(c.Args, []string{"run", "--detach"}) >= 0 {
					cancel()
					return nil, nil, context.Canceled
				}
				return original(c)
			}
			code, err := a.runPlan(ctx, plan, func(centralSandbox) { t.Fatal("ready after failed start") })
			if (err != nil) != cleanupFails || (code != 0) != cleanupFails || fake.downCount != 1 {
				t.Fatalf("code=%d err=%v down=%d", code, err, fake.downCount)
			}
		})
	}
}

func TestCentralEarlyAgentExitAndStartupLogs(t *testing.T) {
	for _, code := range []int{0, 7, 127} {
		t.Run(fmt.Sprint(code), func(t *testing.T) {
			labels := composeProjectLabels("hash", 123, "sandbox", "compose")
			labels["wisp.run-id"] = "run"
			runner := &recordingRunner{capture: func(c process.Command) ([]byte, []byte, error) {
				if reflect.DeepEqual(c.Args, []string{"container", "inspect", "sandbox"}) {
					data, err := json.Marshal([]any{map[string]any{"Id": "id", "Name": "/sandbox", "Config": map[string]any{"Labels": labels}, "State": map[string]any{"Running": false, "ExitCode": code}}})
					return data, nil, err
				}
				if reflect.DeepEqual(c.Args, []string{"logs", "--tail", "30", "id"}) {
					return []byte("startup error secret-token"), nil, nil
				}
				t.Fatalf("unexpected command %v", c.Args)
				return nil, nil, nil
			}}
			var stderr bytes.Buffer
			a, err := New(Dependencies{Runner: runner, InvocationDir: t.TempDir(), Stdin: &bytes.Buffer{}, Stdout: &bytes.Buffer{}, Stderr: &stderr})
			if err != nil {
				t.Fatal(err)
			}
			plan := SandboxPlan{Project: project.Project{Hash: "hash", ComposeProject: "compose", ContainerName: "sandbox"}, UID: 123, Environment: map[string]string{"WISP_AWS_AUTHORIZATION_TOKEN": "secret-token"}}
			status, err := a.waitCentralSandbox(context.Background(), plan, "run", func(centralSandbox) { t.Fatal("ready for stopped agent") })
			want := code
			if want == 0 {
				want = 1
			}
			if status != want || err == nil {
				t.Fatalf("status=%d err=%v", status, err)
			}
			if strings.Contains(stderr.String(), "secret-token") || !strings.Contains(stderr.String(), "[REDACTED]") {
				t.Fatalf("unsafe log %q", stderr.String())
			}
			if code == 127 && !strings.Contains(err.Error(), "rebuild") {
				t.Fatalf("missing rebuild guidance: %v", err)
			}
		})
	}
}

func TestCentralAttachRequiresSpecificRunAndUsesContainerID(t *testing.T) {
	for _, mismatch := range []bool{false, true} {
		t.Run(fmt.Sprint(mismatch), func(t *testing.T) {
			labels := composeProjectLabels("hash", 123, "sandbox", "compose")
			labels["wisp.run-id"] = "expected"
			actual := cloneEnvironment(labels)
			if mismatch {
				actual["wisp.run-id"] = "other"
			}
			runner := &recordingRunner{capture: func(c process.Command) ([]byte, []byte, error) {
				if !reflect.DeepEqual(c.Args, []string{"container", "inspect", "immutable-id"}) {
					t.Fatalf("args=%v", c.Args)
				}
				data, err := json.Marshal([]any{map[string]any{"Id": "immutable-id", "Name": "/sandbox", "Config": map[string]any{"Labels": actual}, "State": map[string]any{"Running": true}}})
				return data, nil, err
			}}
			a, err := New(Dependencies{Runner: runner, InvocationDir: t.TempDir(), UID: 123, GID: 456, Stdin: &bytes.Buffer{}, Stdout: &bytes.Buffer{}, Stderr: &bytes.Buffer{}})
			if err != nil {
				t.Fatal(err)
			}
			err = a.attachCentral(context.Background(), centralSandbox{ID: "immutable-id", Labels: labels})
			if (err != nil) != mismatch {
				t.Fatalf("err=%v", err)
			}
			if mismatch {
				if len(runner.attaches) != 0 {
					t.Fatal("attached to replacement")
				}
				return
			}
			args := runner.attaches[0].Args
			if indexSlice(args, []string{"immutable-id", "tmux", "-u", "attach-session", "-t", "wisp"}) < 0 || indexSlice(args, []string{"--user", "123:456"}) < 0 {
				t.Fatalf("args=%v", args)
			}
		})
	}
}

func TestCentralViewAndBoundedLog(t *testing.T) {
	projects := []centralProject{{name: "Alpha", plan: SandboxPlan{Project: project.Project{RootDir: "/one"}}, state: "running", log: &centralLog{}}, {name: "Beta", plan: SandboxPlan{Project: project.Project{RootDir: "/two"}}, state: "closed", log: &centralLog{}}}
	if got := centralVisible(projects, "ALPHA"); !reflect.DeepEqual(got, []int{0}) {
		t.Fatal(got)
	}
	if got := centralVisible(projects, "two"); !reflect.DeepEqual(got, []int{1}) {
		t.Fatal(got)
	}
	var out bytes.Buffer
	if err := renderCentral(&out, projects, []int{0, 1}, 0, "", "test\x1b[31m", false, false, false, 80, 24); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out.String(), "\x1b[31m") || !strings.Contains(out.String(), "> Alpha") {
		t.Fatalf("render=%q", out.String())
	}
	log := &centralLog{}
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 50; j++ {
				_, _ = log.Write([]byte(strings.Repeat("x", 1000)))
				_ = log.String()
			}
		}()
	}
	wg.Wait()
	if len(log.String()) > 16*1024 {
		t.Fatal("unbounded log")
	}
	if got := centralText("hi\x1b\n\u009b", 80); got != "hi   " {
		t.Fatalf("unsafe text=%q", got)
	}
}

type scriptedCentralUI struct {
	read      func() (string, error)
	suspended bool
	resumes   int
}

func (u *scriptedCentralUI) resume() error        { u.suspended = false; u.resumes++; return nil }
func (u *scriptedCentralUI) suspend() error       { u.suspended = true; return nil }
func (u *scriptedCentralUI) size() (int, int)     { return 100, 24 }
func (u *scriptedCentralUI) key() (string, error) { return u.read() }

type contextCentralRunner struct {
	capture  func(context.Context, process.Command) ([]byte, []byte, error)
	attached func(context.Context, process.Command) error
}

func (r contextCentralRunner) Capture(ctx context.Context, c process.Command) ([]byte, []byte, error) {
	return r.capture(ctx, c)
}
func (r contextCentralRunner) Attached(ctx context.Context, c process.Command) error {
	return r.attached(ctx, c)
}

func TestCentralQuitRetainsConsumedWorkerFailure(t *testing.T) {
	root, path, dir := applicationFixture(t)
	contents := fmt.Sprintf("schema_version=1\n[[central.projects]]\nname='test'\npath=%q\n", dir)
	if err := os.WriteFile(path, []byte(contents), 0600); err != nil {
		t.Fatal(err)
	}
	fake := &lifecycleFake{}
	runner := fake.runner(t)
	original := runner.capture
	runner.capture = func(c process.Command) ([]byte, []byte, error) {
		if indexSlice(c.Args, []string{"run", "--detach"}) >= 0 {
			return nil, []byte("launch failed"), errors.New("launch failed")
		}
		return original(c)
	}
	var stdout bytes.Buffer
	a := newFixtureApp(t, root, runner, &stdout, &bytes.Buffer{})
	opened := false
	ui := &scriptedCentralUI{read: func() (string, error) {
		if !opened {
			opened = true
			return "o", nil
		}
		if strings.Contains(stdout.String(), "failed") {
			return "q", nil
		}
		time.Sleep(time.Millisecond)
		return "", nil
	}}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	code, err := a.centralWithTerminal(ctx, path, ui)
	if code != 1 || err == nil || !strings.Contains(err.Error(), "launch failed") || fake.downCount != 1 {
		t.Fatalf("code=%d err=%v down=%d", code, err, fake.downCount)
	}
	if !ui.suspended {
		t.Fatal("terminal not restored")
	}
}

func TestCentralSIGTERMCancelsStartupAndCleans(t *testing.T) {
	root, path, dir := applicationFixture(t)
	contents := fmt.Sprintf("schema_version=1\n[[central.projects]]\nname='test'\npath=%q\n", dir)
	if err := os.WriteFile(path, []byte(contents), 0600); err != nil {
		t.Fatal(err)
	}
	fake := &lifecycleFake{}
	base := fake.runner(t)
	var starting atomic.Bool
	runner := contextCentralRunner{capture: func(ctx context.Context, c process.Command) ([]byte, []byte, error) {
		if indexSlice(c.Args, []string{"run", "--detach"}) >= 0 {
			starting.Store(true)
			<-ctx.Done()
			return nil, nil, ctx.Err()
		}
		return base.Capture(ctx, c)
	}}
	a := newFixtureApp(t, root, runner, &bytes.Buffer{}, &bytes.Buffer{})
	opened, sent := false, false
	ui := &scriptedCentralUI{read: func() (string, error) {
		if !opened {
			opened = true
			return "o", nil
		}
		if starting.Load() && !sent {
			sent = true
			if err := syscall.Kill(os.Getpid(), syscall.SIGTERM); err != nil {
				return "", err
			}
		}
		time.Sleep(time.Millisecond)
		return "", nil
	}}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	code, err := a.centralWithTerminal(ctx, path, ui)
	if code != 143 || err != nil || fake.downCount != 1 || !sent {
		t.Fatalf("code=%d err=%v down=%d signal=%t", code, err, fake.downCount, sent)
	}
	if !ui.suspended {
		t.Fatal("terminal not restored")
	}
}

func TestCentralHostToolHandoff(t *testing.T) {
	for _, tool := range []struct{ key, path string }{{"e", "nvim"}, {"g", "lazygit"}} {
		t.Run(tool.path, func(t *testing.T) {
			root, path, dir := applicationFixture(t)
			contents := fmt.Sprintf("schema_version=1\n[[central.projects]]\nname='test'\npath=%q\n", dir)
			if err := os.WriteFile(path, []byte(contents), 0600); err != nil {
				t.Fatal(err)
			}
			fake := &lifecycleFake{}
			base := fake.runner(t)
			ui := &scriptedCentralUI{}
			calls := 0
			runner := contextCentralRunner{
				capture: func(ctx context.Context, c process.Command) ([]byte, []byte, error) {
					if indexSlice(c.Args, []string{"run", "--detach"}) >= 0 {
						<-ctx.Done()
						return nil, nil, ctx.Err()
					}
					return base.Capture(ctx, c)
				},
				attached: func(_ context.Context, c process.Command) error {
					calls++
					if !ui.suspended || c.Path != tool.path || c.Dir != dir || len(c.Args) != 0 {
						t.Errorf("bad handoff: suspended=%t command=%#v", ui.suspended, c)
					}
					return nil
				},
			}
			a := newFixtureApp(t, root, runner, &bytes.Buffer{}, &bytes.Buffer{})
			keys := []string{tool.key, "q", "y"}
			ui.read = func() (string, error) { key := keys[0]; keys = keys[1:]; return key, nil }
			code, err := a.centralWithTerminal(context.Background(), path, ui)
			if code != 0 || err != nil || calls != 1 || ui.resumes != 2 || !ui.suspended {
				t.Fatalf("code=%d err=%v calls=%d resumes=%d restored=%t", code, err, calls, ui.resumes, ui.suspended)
			}
		})
	}
}

func TestCentralSmallScreenShowsQuitConfirmation(t *testing.T) {
	var out bytes.Buffer
	p := centralProject{name: "app", state: "running", log: &centralLog{}}
	if err := renderCentral(&out, []centralProject{p}, []int{0}, 0, "", "", false, true, true, 50, 6); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "Stop all and quit?") {
		t.Fatalf("hidden confirmation: %q", out.String())
	}
}

func TestCentralCleanupRejectsReplacementRun(t *testing.T) {
	labels := composeProjectLabels("hash", 123, "sandbox", "compose")
	labels["wisp.run-id"] = "replacement"
	runner := &recordingRunner{capture: func(c process.Command) ([]byte, []byte, error) {
		if len(c.Args) >= 2 && reflect.DeepEqual(c.Args[:2], []string{"container", "ls"}) {
			return []byte("sandbox\n"), nil, nil
		}
		if reflect.DeepEqual(c.Args, []string{"container", "inspect", "sandbox"}) {
			data, err := json.Marshal([]any{map[string]any{"Id": "replacement", "Name": "/sandbox", "Config": map[string]any{"Labels": labels}, "State": map[string]any{"Running": true}}})
			return data, nil, err
		}
		t.Fatalf("cleanup mutated resources: %v", c.Args)
		return nil, nil, nil
	}}
	a, err := New(Dependencies{Runner: runner, InvocationDir: t.TempDir(), Stdin: &bytes.Buffer{}, Stdout: &bytes.Buffer{}, Stderr: &bytes.Buffer{}})
	if err != nil {
		t.Fatal(err)
	}
	err = a.cleanupComposeRun(context.Background(), docker.ComposeInvocation{ProjectName: "compose"}, "hash", 123, map[string]bool{"sandbox": true}, "sandbox", "owned-run")
	if err == nil || !strings.Contains(err.Error(), "refusing Compose cleanup") {
		t.Fatalf("err=%v", err)
	}
}
