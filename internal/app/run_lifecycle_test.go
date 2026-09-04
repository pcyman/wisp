package app

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"reflect"
	"strings"
	"testing"

	"wisp/internal/cli"
	"wisp/internal/process"
	"wisp/internal/project"
)

type lifecycleFake struct {
	project       project.Project
	stale         bool
	staleCleaned  bool
	staleDownEnv  []string
	downCount     int
	upError       func(process.Command) error
	logOutput     func(process.Command) string
	attachedError func(process.Command) error
	cleanupError  func(process.Command) error
}

func (f *lifecycleFake) runner(t *testing.T) *recordingRunner {
	t.Helper()
	runner := &recordingRunner{}
	runner.capture = func(command process.Command) ([]byte, []byte, error) {
		switch {
		case command.Path == "git":
			return nil, nil, errors.New("not a worktree")
		case reflect.DeepEqual(command.Args, []string{"version", "--format", "{{.Server.Version}}"}):
			return []byte("20.10.0"), nil, nil
		case reflect.DeepEqual(command.Args, []string{"compose", "version", "--short"}):
			return []byte("2.24.0"), nil, nil
		case len(command.Args) == 3 && reflect.DeepEqual(command.Args[:2], []string{"container", "inspect"}):
			if f.stale && !f.staleCleaned && command.Args[2] == "stale-credentials" {
				value, err := json.Marshal([]any{map[string]any{
					"Id": "stale", "Name": "/stale-credentials",
					"Config": map[string]any{"Labels": composeProjectLabels(f.project.Hash, os.Getuid(), "credentials", f.project.ComposeProject)},
					"State":  map[string]any{"Running": true},
				}})
				return value, nil, err
			}
			return nil, []byte("No such container"), errors.New("exit 1")
		case len(command.Args) >= 2 && reflect.DeepEqual(command.Args[:2], []string{"container", "ls"}):
			if f.stale && !f.staleCleaned {
				return []byte("stale-credentials\n"), nil, nil
			}
			return nil, nil, nil
		case len(command.Args) >= 2 && reflect.DeepEqual(command.Args[:2], []string{"image", "inspect"}):
			return []byte("[]"), nil, nil
		case indexSlice(command.Args, []string{"up", "--detach", "--wait", "credentials"}) >= 0:
			if f.upError != nil {
				return nil, nil, f.upError(command)
			}
			return nil, nil, nil
		case indexSlice(command.Args, []string{"logs", "--no-color", "--tail", "100", "credentials"}) >= 0:
			if f.logOutput != nil {
				return []byte(f.logOutput(command)), nil, nil
			}
			return nil, nil, nil
		case indexSlice(command.Args, []string{"down", "--remove-orphans"}) >= 0:
			if f.stale && !f.staleCleaned {
				f.staleDownEnv = append([]string(nil), command.Env...)
				f.staleCleaned = true
				return nil, nil, nil
			}
			f.downCount++
			if f.cleanupError != nil {
				return nil, nil, f.cleanupError(command)
			}
			return nil, nil, nil
		default:
			return nil, nil, fmt.Errorf("unexpected command: %v", command.Args)
		}
	}
	runner.attached = func(command process.Command) error {
		if f.attachedError != nil {
			return f.attachedError(command)
		}
		return nil
	}
	return runner
}

func TestStaleCleanupPrecedesTokenGenerationAndUsesPlaceholder(t *testing.T) {
	root, configPath, projectDir := applicationFixture(t)
	resolved, err := project.Resolve(context.Background(), projectDir, root, os.Getuid(), nil)
	if err != nil {
		t.Fatal(err)
	}
	fake := &lifecycleFake{project: resolved, stale: true}
	runner := fake.runner(t)
	application := newFixtureApp(t, root, runner, &bytes.Buffer{}, &bytes.Buffer{})
	randomReads := 0
	generatedBeforeCleanup := false
	application.deps.Random = observingReader{Reader: bytes.NewReader(bytes.Repeat([]byte{0xab}, 32)), observed: func() {
		randomReads++
		generatedBeforeCleanup = !fake.staleCleaned
	}}

	status := application.Execute(context.Background(), cli.Request{Command: cli.CommandRun, Run: cli.RunRequest{Directory: projectDir, ConfigPath: configPath}})
	if status != 0 || randomReads == 0 || generatedBeforeCleanup {
		t.Fatalf("status=%d random reads=%d generated before cleanup=%v", status, randomReads, generatedBeforeCleanup)
	}
	if got := testEnvironmentValue(fake.staleDownEnv, "WISP_AWS_AUTHORIZATION_TOKEN"); got != cleanupAuthorizationPlaceholder {
		t.Fatalf("stale cleanup token = %q, want placeholder", got)
	}
	if strings.Contains(strings.Join(fake.staleDownEnv, "\n"), strings.Repeat("ab", 32)) {
		t.Fatal("real random token reached stale cleanup")
	}
}

func TestRunCleanupAndExitStatusPrecedence(t *testing.T) {
	for _, test := range []struct {
		name          string
		upError       error
		attachedError error
		cleanupError  error
		wantStatus    int
		wantAttached  bool
	}{
		{"broker startup failure still cleans up", errors.New("broker failed"), nil, nil, 1, false},
		{"attached child status wins over cleanup failure", nil, exitCodeError(37), errors.New("cleanup failed"), 37, true},
		{"successful child and cleanup failure returns one", nil, nil, errors.New("cleanup failed"), 1, true},
		{"attached failure still cleans up", nil, exitCodeError(29), nil, 29, true},
	} {
		t.Run(test.name, func(t *testing.T) {
			root, configPath, projectDir := applicationFixture(t)
			fake := &lifecycleFake{
				upError:       func(process.Command) error { return test.upError },
				attachedError: func(process.Command) error { return test.attachedError },
				cleanupError:  func(process.Command) error { return test.cleanupError },
			}
			runner := fake.runner(t)
			application := newFixtureApp(t, root, runner, &bytes.Buffer{}, &bytes.Buffer{})

			status := application.Execute(context.Background(), cli.Request{Command: cli.CommandRun, Run: cli.RunRequest{Directory: projectDir, ConfigPath: configPath}})
			if status != test.wantStatus {
				t.Fatalf("status=%d, want %d", status, test.wantStatus)
			}
			if fake.downCount != 1 {
				t.Fatalf("cleanup down calls=%d, want 1", fake.downCount)
			}
			if got := len(runner.attaches) > 0; got != test.wantAttached {
				t.Fatalf("attached=%v, want %v", got, test.wantAttached)
			}
		})
	}
}

func TestComposeRunTTYFlagMatrix(t *testing.T) {
	for _, test := range []struct {
		name     string
		terminal bool
		wantTail []string
	}{
		{"TTY", true, []string{"--no-deps", "sandbox"}},
		{"non-TTY", false, []string{"--no-deps", "--no-TTY", "sandbox"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			root, configPath, projectDir := applicationFixture(t)
			resolved, err := project.Resolve(context.Background(), projectDir, root, os.Getuid(), nil)
			if err != nil {
				t.Fatal(err)
			}
			fake := &lifecycleFake{}
			runner := fake.runner(t)
			application := newFixtureApp(t, root, runner, &bytes.Buffer{}, &bytes.Buffer{})
			application.deps.IsTerminal = func(io.Reader, io.Writer) bool { return test.terminal }

			status := application.Execute(context.Background(), cli.Request{Command: cli.CommandRun, Run: cli.RunRequest{Directory: projectDir, ConfigPath: configPath}})
			if status != 0 || len(runner.attaches) != 1 {
				t.Fatalf("status=%d attached=%d", status, len(runner.attaches))
			}
			args := runner.attaches[0].Args
			want := []string{"run", "--rm", "--name", resolved.ContainerName, "--user", fmt.Sprintf("%d:%d", os.Getuid(), os.Getgid()), "--workdir", "/workspace/current"}
			want = append(want, test.wantTail...)
			start := indexSlice(args, []string{"run", "--rm", "--name"})
			if start < 0 || !reflect.DeepEqual(args[start:], want) {
				t.Fatalf("compose argv=%#v, want run argv %#v", args, want)
			}
			if got := countArgument(args, "--no-TTY"); got != btoi(!test.terminal) {
				t.Fatalf("--no-TTY count=%d", got)
			}
		})
	}
}

func TestAuthorizationTokenIsRedactedFromLifecycleOutput(t *testing.T) {
	token := "Basic " + strings.Repeat("ab", 32)
	for _, source := range []string{"broker error and logs", "attached error", "cleanup error"} {
		t.Run(source, func(t *testing.T) {
			root, configPath, projectDir := applicationFixture(t)
			var stderr bytes.Buffer
			fake := &lifecycleFake{}
			switch source {
			case "broker error and logs":
				fake.upError = func(command process.Command) error {
					return errors.New("broker exposed " + testEnvironmentValue(command.Env, "WISP_AWS_AUTHORIZATION_TOKEN"))
				}
				fake.logOutput = func(command process.Command) string {
					return "logs exposed " + testEnvironmentValue(command.Env, "WISP_AWS_AUTHORIZATION_TOKEN")
				}
			case "attached error":
				fake.attachedError = func(command process.Command) error {
					return errors.New("attached exposed " + testEnvironmentValue(command.Env, "WISP_AWS_AUTHORIZATION_TOKEN"))
				}
			case "cleanup error":
				fake.cleanupError = func(command process.Command) error {
					return errors.New("cleanup exposed " + testEnvironmentValue(command.Env, "WISP_AWS_AUTHORIZATION_TOKEN"))
				}
			}
			application := newFixtureApp(t, root, fake.runner(t), &bytes.Buffer{}, &stderr)
			application.deps.Random = bytes.NewReader(bytes.Repeat([]byte{0xab}, 32))

			status := application.Execute(context.Background(), cli.Request{Command: cli.CommandRun, Run: cli.RunRequest{Directory: projectDir, ConfigPath: configPath}})
			if status != 1 || strings.Contains(stderr.String(), token) || !strings.Contains(stderr.String(), "[REDACTED]") {
				t.Fatalf("status=%d stderr=%q", status, stderr.String())
			}
		})
	}
}

type exitCodeError int

func (e exitCodeError) Error() string { return fmt.Sprintf("exit %d", e) }
func (e exitCodeError) ExitCode() int { return int(e) }

func testEnvironmentValue(environment []string, name string) string {
	prefix := name + "="
	for _, value := range environment {
		if strings.HasPrefix(value, prefix) {
			return strings.TrimPrefix(value, prefix)
		}
	}
	return ""
}

func countArgument(args []string, value string) int {
	count := 0
	for _, arg := range args {
		if arg == value {
			count++
		}
	}
	return count
}

func btoi(value bool) int {
	if value {
		return 1
	}
	return 0
}
