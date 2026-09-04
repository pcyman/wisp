package app

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"wisp/internal/cli"
	"wisp/internal/docker"
	"wisp/internal/process"
	"wisp/internal/project"
)

type recordingRunner struct {
	capture  func(process.Command) ([]byte, []byte, error)
	attached func(process.Command) error
	commands []process.Command
	attaches []process.Command
}

func (r *recordingRunner) Capture(_ context.Context, command process.Command) ([]byte, []byte, error) {
	r.commands = append(r.commands, command)
	return r.capture(command)
}

func (r *recordingRunner) Attached(_ context.Context, command process.Command) error {
	r.attaches = append(r.attaches, command)
	if r.attached == nil {
		return nil
	}
	return r.attached(command)
}

func TestConfigPathDoesNotUseRunnerOrAssets(t *testing.T) {
	var stdout bytes.Buffer
	runner := &recordingRunner{capture: func(process.Command) ([]byte, []byte, error) {
		t.Fatal("config path invoked an external process")
		return nil, nil, nil
	}}
	application, err := New(Dependencies{Runner: runner, Stdout: &stdout, Stderr: &bytes.Buffer{}, InvocationDir: t.TempDir(), Environment: []string{"HOME=/home/test"}, UID: 123, GID: 456})
	if err != nil {
		t.Fatal(err)
	}
	status := application.Execute(context.Background(), cli.Request{Command: cli.CommandConfigPath, Config: cli.ConfigRequest{Path: "local.toml"}})
	if status != 0 || !strings.HasSuffix(stdout.String(), "/local.toml\n") || len(runner.commands) != 0 {
		t.Fatalf("status=%d stdout=%q commands=%v", status, stdout.String(), runner.commands)
	}
}

func TestExecUsesExactPayloadAndTTYFlags(t *testing.T) {
	for _, test := range []struct {
		name     string
		terminal bool
		ttyArg   []string
	}{{"non-TTY", false, nil}, {"TTY", true, []string{"--tty"}}} {
		t.Run(test.name, func(t *testing.T) {
			root := t.TempDir()
			resolved, err := project.Resolve(context.Background(), ".", root, 123, nil)
			if err != nil {
				t.Fatal(err)
			}
			inspect, _ := json.Marshal([]any{map[string]any{
				"Id": "id", "Name": "/" + resolved.ContainerName,
				"Config": map[string]any{"Labels": composeProjectLabels(resolved.Hash, 123, "sandbox", resolved.ComposeProject)},
				"State":  map[string]any{"Running": true},
			}})
			runner := &recordingRunner{}
			runner.capture = func(command process.Command) ([]byte, []byte, error) {
				switch {
				case command.Path == "git":
					return nil, nil, errors.New("not a worktree")
				case reflect.DeepEqual(command.Args, []string{"version", "--format", "{{.Server.Version}}"}):
					return []byte("20.10.0"), nil, nil
				case len(command.Args) >= 2 && command.Args[0] == "container" && command.Args[1] == "inspect":
					return inspect, nil, nil
				default:
					return nil, nil, fmt.Errorf("unexpected command: %v", command.Args)
				}
			}
			application, err := New(Dependencies{Runner: runner, Stdin: bytes.NewBuffer(nil), Stdout: &bytes.Buffer{}, Stderr: &bytes.Buffer{}, InvocationDir: root, Environment: []string{"PATH=/bin", "WISP_AWS_AUTHORIZATION_TOKEN=host-secret"}, UID: 123, GID: 456, IsTerminal: func(io.Reader, io.Writer) bool { return test.terminal }})
			if err != nil {
				t.Fatal(err)
			}
			payload := []string{"sh", "-c", "literal argument", "--flag"}
			status := application.Execute(context.Background(), cli.Request{Command: cli.CommandExec, Exec: cli.ExecRequest{Directory: ".", Command: payload}})
			if status != 0 || len(runner.attaches) != 1 {
				t.Fatalf("status=%d attached=%v", status, runner.attaches)
			}
			want := []string{"exec", "--interactive", "--user", "123:456", "--workdir", "/workspace/current"}
			want = append(want, test.ttyArg...)
			want = append(want, resolved.ContainerName)
			want = append(want, payload...)
			if !reflect.DeepEqual(runner.attaches[0].Args, want) {
				t.Fatalf("args=%#v want=%#v", runner.attaches[0].Args, want)
			}
			if strings.Contains(strings.Join(runner.attaches[0].Env, "\n"), "host-secret") {
				t.Fatal("inherited broker token reached docker exec")
			}
		})
	}
}

func TestRunLifecycleAndSecretRedaction(t *testing.T) {
	root, configPath, projectDir := applicationFixture(t)
	var stderr bytes.Buffer
	step := 0
	runner := &recordingRunner{}
	runner.capture = func(command process.Command) ([]byte, []byte, error) {
		step++
		switch step {
		case 1:
			return nil, nil, errors.New("not git")
		case 2:
			return []byte("20.10.0"), nil, nil
		case 3:
			return []byte("2.24.0"), nil, nil
		case 4:
			return nil, []byte("No such container"), errors.New("exit 1")
		case 5:
			return nil, nil, nil
		case 6, 7:
			return []byte("[]"), nil, nil
		case 8:
			return nil, nil, nil
		case 9, 10, 11:
			return nil, nil, nil
		case 12:
			return nil, []byte("No such container"), errors.New("exit 1")
		default:
			return nil, nil, fmt.Errorf("unexpected step %d: %v", step, command.Args)
		}
	}
	runner.attached = func(command process.Command) error {
		for _, value := range command.Env {
			if strings.HasPrefix(value, "WISP_AWS_AUTHORIZATION_TOKEN=") {
				return errors.New("launch failed with " + strings.TrimPrefix(value, "WISP_AWS_AUTHORIZATION_TOKEN="))
			}
		}
		return errors.New("token missing")
	}
	application := newFixtureApp(t, root, runner, &bytes.Buffer{}, &stderr)
	tokenAt := -1
	application.deps.Random = observingReader{Reader: bytes.NewReader(make([]byte, 32)), observed: func() { tokenAt = len(runner.commands) }}
	status := application.Execute(context.Background(), cli.Request{Command: cli.CommandRun, Run: cli.RunRequest{Directory: projectDir, ConfigPath: configPath}})
	if status != 1 {
		t.Fatalf("status=%d stderr=%s", status, stderr.String())
	}
	if strings.Contains(stderr.String(), strings.Repeat("00", 32)) || !strings.Contains(stderr.String(), "[REDACTED]") {
		t.Fatalf("secret was not safely redacted: %s", stderr.String())
	}
	if len(runner.attaches) != 1 {
		t.Fatalf("attached=%d", len(runner.attaches))
	}
	args := runner.attaches[0].Args
	wantSuffix := []string{"run", "--rm", "--name"}
	if indexSlice(args, wantSuffix) < 0 || !reflect.DeepEqual(args[len(args)-2:], []string{"--no-TTY", "sandbox"}) {
		t.Fatalf("compose run args=%v", args)
	}
	if step != 12 {
		t.Fatalf("cleanup did not complete, step=%d", step)
	}
	if tokenAt < 5 {
		t.Fatalf("token generated before prerequisites and ownership inspection at command %d", tokenAt)
	}
	for _, command := range runner.commands[:tokenAt] {
		if len(command.Args) > 1 && command.Args[0] == "compose" && command.Args[1] != "version" {
			t.Fatalf("Compose ran before token generation: %v", command.Args)
		}
	}
}

func TestAWSCheckUsesCredentialsOnlyLifecycle(t *testing.T) {
	root, configPath, _ := applicationFixture(t)
	var stdout, stderr bytes.Buffer
	step := 0
	runner := &recordingRunner{capture: func(command process.Command) ([]byte, []byte, error) {
		step++
		switch step {
		case 1:
			return []byte("20.10.0"), nil, nil
		case 2:
			return []byte("2.24.0"), nil, nil
		case 3:
			return nil, nil, nil
		case 4:
			if !reflect.DeepEqual(command.Args[:2], []string{"image", "inspect"}) {
				t.Fatalf("unexpected image command: %v", command.Args)
			}
			return []byte("[]"), nil, nil
		case 5:
			if !reflect.DeepEqual(command.Args[len(command.Args)-4:], []string{"up", "--detach", "--wait", "credentials"}) {
				t.Fatalf("unexpected startup: %v", command.Args)
			}
			return nil, nil, nil
		case 6, 7, 8:
			return nil, nil, nil
		default:
			return nil, nil, fmt.Errorf("unexpected step %d: %v", step, command.Args)
		}
	}}
	application := newFixtureApp(t, root, runner, &stdout, &stderr)
	status := application.Execute(context.Background(), cli.Request{Command: cli.CommandAWSCheck, AWSCheck: cli.AWSCheckRequest{Alias: "dev", ConfigPath: configPath}})
	if status != 0 || !strings.Contains(stdout.String(), "succeeded for alias dev") {
		t.Fatalf("status=%d stdout=%q stderr=%q", status, stdout.String(), stderr.String())
	}
	if len(runner.attaches) != 0 {
		t.Fatal("aws check started an attached sandbox")
	}
	for _, command := range runner.commands {
		if indexSlice(command.Args, []string{"build", "sandbox"}) >= 0 || indexSlice(command.Args, []string{"run", "sandbox"}) >= 0 {
			t.Fatalf("aws check touched sandbox service: %v", command.Args)
		}
	}
}

func TestBrokerStartupLogsRedactAuthorizationToken(t *testing.T) {
	root, configPath, projectDir := applicationFixture(t)
	var stderr bytes.Buffer
	token := "Basic " + strings.Repeat("00", 32)
	runner := &recordingRunner{}
	runner.capture = func(command process.Command) ([]byte, []byte, error) {
		switch {
		case command.Path == "git":
			return nil, nil, errors.New("not git")
		case reflect.DeepEqual(command.Args, []string{"version", "--format", "{{.Server.Version}}"}):
			return []byte("20.10.0"), nil, nil
		case reflect.DeepEqual(command.Args, []string{"compose", "version", "--short"}):
			return []byte("2.24.0"), nil, nil
		case len(command.Args) >= 2 && reflect.DeepEqual(command.Args[:2], []string{"container", "inspect"}):
			return nil, []byte("No such container"), errors.New("exit 1")
		case len(command.Args) >= 2 && reflect.DeepEqual(command.Args[:2], []string{"container", "ls"}):
			return nil, nil, nil
		case len(command.Args) >= 2 && reflect.DeepEqual(command.Args[:2], []string{"image", "inspect"}):
			return []byte("[]"), nil, nil
		case indexSlice(command.Args, []string{"up", "--detach", "--wait", "credentials"}) >= 0:
			return nil, nil, errors.New("startup exposed " + token)
		case indexSlice(command.Args, []string{"logs", "--no-color", "--tail", "100", "credentials"}) >= 0:
			return []byte("broker log exposed " + token + "\n"), nil, nil
		case indexSlice(command.Args, []string{"down", "--remove-orphans"}) >= 0:
			return nil, nil, nil
		default:
			return nil, nil, fmt.Errorf("unexpected command: %v", command.Args)
		}
	}
	application := newFixtureApp(t, root, runner, &bytes.Buffer{}, &stderr)
	status := application.Execute(context.Background(), cli.Request{Command: cli.CommandRun, Run: cli.RunRequest{Directory: projectDir, ConfigPath: configPath}})
	if status != 1 || strings.Contains(stderr.String(), token) || !strings.Contains(stderr.String(), "[REDACTED]") {
		t.Fatalf("status=%d stderr=%q", status, stderr.String())
	}
}

func TestCleanupRefusesMismatchedProjectContainerBeforeDown(t *testing.T) {
	inspect := `[{"Id":"bad","Name":"/foreign","Config":{"Labels":{"com.docker.compose.project":"project","wisp.managed":"true","wisp.owner-uid":"999","wisp.project-hash":"hash","wisp.kind":"credentials"}},"State":{"Running":true}}]`
	runner := &recordingRunner{capture: func(command process.Command) ([]byte, []byte, error) {
		if reflect.DeepEqual(command.Args[:2], []string{"container", "ls"}) {
			return []byte("foreign\n"), nil, nil
		}
		if reflect.DeepEqual(command.Args[:2], []string{"container", "inspect"}) {
			return []byte(inspect), nil, nil
		}
		return nil, nil, fmt.Errorf("cleanup must not run command: %v", command.Args)
	}}
	application, err := New(Dependencies{Runner: runner, InvocationDir: t.TempDir(), Environment: []string{"HOME=/home/test"}, UID: 123, GID: 456})
	if err != nil {
		t.Fatal(err)
	}
	invocation := docker.ComposeInvocation{ProjectName: "project", RuntimeRoot: "/runtime", Override: "/override", Environment: []string{"WISP_AWS_AUTHORIZATION_TOKEN=secret"}}
	err = application.cleanupComposeProject(context.Background(), invocation, "hash", 123, map[string]bool{"credentials": true}, "")
	if err == nil || !strings.Contains(err.Error(), "refusing Compose cleanup") {
		t.Fatalf("cleanup error = %v", err)
	}
	for _, command := range runner.commands {
		if len(command.Args) > 0 && command.Args[0] == "compose" {
			t.Fatalf("mismatched resource was passed to Compose cleanup: %v", command.Args)
		}
	}
}

type observingReader struct {
	io.Reader
	observed func()
}

func (r observingReader) Read(buffer []byte) (int, error) {
	r.observed()
	return r.Reader.Read(buffer)
}

func applicationFixture(t *testing.T) (root, configPath, projectDir string) {
	t.Helper()
	root = t.TempDir()
	home := filepath.Join(root, "home")
	projectDir = filepath.Join(root, "project")
	awsConfig := filepath.Join(home, ".aws", "config")
	awsCache := filepath.Join(home, ".aws", "sso", "cache")
	for _, directory := range []string{projectDir, filepath.Dir(awsConfig), awsCache} {
		if err := os.MkdirAll(directory, 0700); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(awsConfig, nil, 0600); err != nil {
		t.Fatal(err)
	}
	configPath = filepath.Join(root, "config.toml")
	contents := fmt.Sprintf("schema_version = 1\n[aws]\nhost_config_path = %q\nsso_cache_path = %q\n[aws.aliases.dev]\nprofile = %q\nrole_arn = %q\n", awsConfig, awsCache, "dev-profile", "arn:aws:iam::123456789012:role/Wisp")
	if err := os.WriteFile(configPath, []byte(contents), 0600); err != nil {
		t.Fatal(err)
	}
	return root, configPath, projectDir
}

func newFixtureApp(t *testing.T, root string, runner process.Runner, stdout, stderr *bytes.Buffer) *App {
	t.Helper()
	repositoryRoot, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	application, err := New(Dependencies{
		RuntimeAssets: os.DirFS(repositoryRoot), Runner: runner, Stdin: bytes.NewBuffer(nil), Stdout: stdout, Stderr: stderr,
		InvocationDir: root, Environment: []string{"HOME=" + filepath.Join(root, "home"), "XDG_CACHE_HOME=" + filepath.Join(root, "cache"), "TMPDIR=" + root},
		UID: os.Getuid(), GID: os.Getgid(), Random: bytes.NewReader(make([]byte, 32)), IsTerminal: func(io.Reader, io.Writer) bool { return false },
	})
	if err != nil {
		t.Fatal(err)
	}
	return application
}

func indexSlice(values, target []string) int {
	for i := 0; i+len(target) <= len(values); i++ {
		if reflect.DeepEqual(values[i:i+len(target)], target) {
			return i
		}
	}
	return -1
}
