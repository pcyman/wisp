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
	"testing"

	"wisp/internal/cli"
	"wisp/internal/docker"
	"wisp/internal/process"
	"wisp/internal/project"
)

func TestDoctorIsNonMutatingAndWarningsDoNotFail(t *testing.T) {
	root, configPath, _ := applicationFixture(t)
	cacheRoot := filepath.Join(root, "cache")
	runtimeRoot := filepath.Join(root, "wisp-"+fmt.Sprint(os.Getuid()))
	var stdout bytes.Buffer
	runner := &recordingRunner{}
	runner.capture = func(command process.Command) ([]byte, []byte, error) {
		if command.Path == "git" {
			if reflect.DeepEqual(command.Args, []string{"--version"}) {
				return []byte("git version 2.45.0\n"), nil, nil
			}
			return []byte(root + "\n"), nil, nil
		}
		switch {
		case reflect.DeepEqual(command.Args, []string{"--version"}):
			return []byte("Docker version 27.0.0\n"), nil, nil
		case reflect.DeepEqual(command.Args, []string{"version", "--format", versionFormatForTest}):
			return []byte("27.0.0\n"), nil, nil
		case reflect.DeepEqual(command.Args, []string{"compose", "version", "--short"}):
			return []byte("2.29.0\n"), nil, nil
		case reflect.DeepEqual(command.Args, []string{"buildx", "version"}):
			return []byte("github.com/docker/buildx v0.16.0\n"), nil, nil
		case len(command.Args) >= 2 && reflect.DeepEqual(command.Args[:2], []string{"image", "inspect"}):
			return nil, []byte("No such image"), errors.New("exit 1")
		case len(command.Args) >= 2 && reflect.DeepEqual(command.Args[:2], []string{"container", "inspect"}):
			return nil, []byte("No such container"), errors.New("exit 1")
		case len(command.Args) >= 2 && reflect.DeepEqual(command.Args[:2], []string{"container", "ls"}):
			return nil, nil, nil
		default:
			t.Fatalf("doctor used unexpected command: %s %v", command.Path, command.Args)
			return nil, nil, nil
		}
	}
	runner.attached = func(command process.Command) error {
		t.Fatalf("doctor used attached command: %s %v", command.Path, command.Args)
		return nil
	}
	application := newFixtureApp(t, root, runner, &stdout, &bytes.Buffer{})
	status := application.Execute(context.Background(), cli.Request{Command: cli.CommandDoctor, Config: cli.ConfigRequest{Path: configPath}})
	if status != 0 {
		t.Fatalf("status=%d output=%s", status, stdout.String())
	}
	for _, required := range []string{
		"PASS effective config:", "PASS config existence:", "PASS config validation:",
		"PASS AWS alias:", "PASS AWS config:", "PASS AWS SSO cache:",
		"WARN OpenCode config:", "WARN OpenCode auth:", "PASS mounts:", "PASS mount collisions:",
		"WARN cache directory:", "WARN runtime fallback directory:", "PASS Git:", "PASS current project:",
		"PASS Docker executable:", "PASS Docker daemon:", "PASS Compose:", "PASS Buildx:",
		"WARN sandbox image:", "WARN credentials image:", "PASS project container:",
		"PASS project resources:", "PASS Docker endpoint:", "Summary:",
	} {
		if !strings.Contains(stdout.String(), required) {
			t.Errorf("output does not contain %q:\n%s", required, stdout.String())
		}
	}
	for _, path := range []string{cacheRoot, runtimeRoot} {
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Errorf("doctor created %q", path)
		}
	}
	assertDoctorReadOnly(t, runner)
}

func TestDoctorFailureReturnsOneAndContinuesChecks(t *testing.T) {
	root, configPath, _ := applicationFixture(t)
	var stdout bytes.Buffer
	runner := doctorPrerequisiteRunner(t, root, "2.23.0")
	application := newFixtureApp(t, root, runner, &stdout, &bytes.Buffer{})
	status := application.Execute(context.Background(), cli.Request{Command: cli.CommandDoctor, Config: cli.ConfigRequest{Path: configPath}})
	if status != 1 || !strings.Contains(stdout.String(), "FAIL Compose:") || !strings.Contains(stdout.String(), "PASS Buildx:") || !strings.Contains(stdout.String(), "Summary:") {
		t.Fatalf("status=%d output=%s", status, stdout.String())
	}
	assertDoctorReadOnly(t, runner)
}

func TestDoctorResourceStatesAndKindValidation(t *testing.T) {
	root := t.TempDir()
	resolved, err := project.Resolve(context.Background(), ".", root, os.Getuid(), nil)
	if err != nil {
		t.Fatal(err)
	}
	activeLabels := composeProjectLabels(resolved.Hash, os.Getuid(), "sandbox", resolved.ComposeProject)
	activeLabels["wisp.version"] = "a-different-cli-version"
	tests := []struct {
		name       string
		running    bool
		labels     map[string]string
		list       string
		extra      []docker.Container
		wantLevel  string
		wantDetail string
	}{
		{"active with different version", true, activeLabels, "", nil, "PASS", "active"},
		{"stopped", false, composeProjectLabels(resolved.Hash, os.Getuid(), "sandbox", resolved.ComposeProject), "", nil, "WARN", "stopped managed sandbox"},
		{"mismatched", true, composeProjectLabels(resolved.Hash, os.Getuid(), "sandbox", "wrong"), "", nil, "FAIL", "unknown name collision"},
		{"orphan", true, composeProjectLabels(resolved.Hash, os.Getuid(), "sandbox", resolved.ComposeProject), "orphan", []docker.Container{{Name: "orphan", Running: true, Labels: composeProjectLabels(resolved.Hash, os.Getuid(), "credentials", resolved.ComposeProject)}}, "WARN", "orphaned active credentials"},
		{"invalid kind", true, composeProjectLabels(resolved.Hash, os.Getuid(), "sandbox", resolved.ComposeProject), "invalid", []docker.Container{{Name: "invalid", Labels: composeProjectLabels(resolved.Hash, os.Getuid(), "builder", resolved.ComposeProject)}}, "FAIL", "invalid Wisp kind"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			commands := 0
			runner := &recordingRunner{capture: func(command process.Command) ([]byte, []byte, error) {
				commands++
				if commands == 1 {
					return inspectJSON(t, docker.Container{Name: resolved.ContainerName, Running: test.running, Labels: test.labels}), nil, nil
				}
				if len(command.Args) >= 2 && reflect.DeepEqual(command.Args[:2], []string{"container", "ls"}) {
					if strings.Contains(strings.Join(command.Args, " "), "com.docker.compose.project") {
						return []byte(test.list), nil, nil
					}
					return nil, nil, nil
				}
				for _, extra := range test.extra {
					if command.Args[len(command.Args)-1] == extra.Name {
						return inspectJSON(t, extra), nil, nil
					}
				}
				return nil, nil, fmt.Errorf("unexpected command %v", command.Args)
			}}
			var output bytes.Buffer
			application, err := New(Dependencies{Runner: runner, Stdout: &output, InvocationDir: root, Environment: []string{"HOME=" + root}, UID: os.Getuid(), GID: os.Getgid()})
			if err != nil {
				t.Fatal(err)
			}
			report := &doctorReporter{out: &output}
			application.doctorResources(context.Background(), report, resolved)
			if !strings.Contains(output.String(), test.wantLevel) || !strings.Contains(output.String(), test.wantDetail) {
				t.Fatalf("output=%s", output.String())
			}
			if test.name == "active with different version" && !strings.Contains(output.String(), "PASS project container: active") {
				t.Fatalf("output=%s", output.String())
			}
		})
	}
}

func TestDoctorCommandsUseSanitizedProcessEnvironment(t *testing.T) {
	root, configPath, _ := applicationFixture(t)
	environment := []string{
		"HOME=" + filepath.Join(root, "home"), "PATH=/bin", "DOCKER_HOST=unix:///socket",
		"DOCKER_CONTEXT=default", "DOCKER_TLS_VERIFY=1", "DOCKER_CERT_PATH=/certs",
	}
	environment = append(environment, privateProcessEnvironment("private")...)
	runner := doctorPrerequisiteRunner(t, root, "2.29.0")
	application, err := New(Dependencies{Runner: runner, Stdout: &bytes.Buffer{}, Stderr: &bytes.Buffer{}, InvocationDir: root, Environment: environment, UID: os.Getuid(), GID: os.Getgid()})
	if err != nil {
		t.Fatal(err)
	}
	_ = application.Execute(context.Background(), cli.Request{Command: cli.CommandDoctor, Config: cli.ConfigRequest{Path: configPath}})
	for _, command := range runner.commands {
		joined := strings.Join(command.Env, "\n")
		for _, private := range privateProcessEnvironment("") {
			key := strings.TrimSuffix(private, "=") + "="
			if strings.Contains(joined, key) {
				t.Errorf("%s %v inherited %s in %v", command.Path, command.Args, key, command.Env)
			}
		}
		for _, preserved := range []string{"DOCKER_HOST=unix:///socket", "DOCKER_CONTEXT=default", "DOCKER_TLS_VERIFY=1", "DOCKER_CERT_PATH=/certs"} {
			if !strings.Contains(joined, preserved) {
				t.Errorf("%s %v lost %s from %v", command.Path, command.Args, preserved, command.Env)
			}
		}
	}
}

func TestDoctorReportsHostPathFailuresIndependently(t *testing.T) {
	root, configPath, _ := applicationFixture(t)
	data, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	missingMount := filepath.Join(root, "missing-mount")
	data = append(data, []byte("\n[[mounts]]\nsource = \""+missingMount+"\"\ntarget = \"/workspace/repos/missing\"\nmode = \"ro\"\n")...)
	if err := os.WriteFile(configPath, data, 0o600); err != nil {
		t.Fatal(err)
	}
	runner := doctorPrerequisiteRunner(t, root, "2.29.0")
	var output bytes.Buffer
	application := newFixtureApp(t, root, runner, &output, &bytes.Buffer{})
	status := application.Execute(context.Background(), cli.Request{Command: cli.CommandDoctor, Config: cli.ConfigRequest{Path: configPath}})
	for _, want := range []string{"FAIL config validation:", "PASS AWS alias:", "PASS AWS config:", "PASS AWS SSO cache:", "FAIL mount 1:", "PASS mount collisions:", "PASS Docker executable:"} {
		if !strings.Contains(output.String(), want) {
			t.Errorf("output does not contain %q:\n%s", want, output.String())
		}
	}
	if status != 1 {
		t.Fatalf("status=%d output=%s", status, output.String())
	}
	assertDoctorReadOnly(t, runner)
}

func TestDoctorReportsEveryMountWhenTargetsCollide(t *testing.T) {
	root, configPath, projectDir := applicationFixture(t)
	data, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	for _, target := range []string{"/workspace/repos/shared", "/workspace/repos/shared/nested"} {
		data = append(data, []byte("\n[[mounts]]\nsource = \""+projectDir+"\"\ntarget = \""+target+"\"\nmode = \"ro\"\n")...)
	}
	if err := os.WriteFile(configPath, data, 0o600); err != nil {
		t.Fatal(err)
	}
	runner := doctorPrerequisiteRunner(t, root, "2.29.0")
	var output bytes.Buffer
	application := newFixtureApp(t, root, runner, &output, &bytes.Buffer{})
	status := application.Execute(context.Background(), cli.Request{Command: cli.CommandDoctor, Config: cli.ConfigRequest{Path: configPath}})
	for _, want := range []string{"PASS mount 1:", "PASS mount 2:", "FAIL mount collisions:", "PASS AWS alias:", "PASS Docker executable:"} {
		if !strings.Contains(output.String(), want) {
			t.Errorf("output does not contain %q:\n%s", want, output.String())
		}
	}
	if status != 1 {
		t.Fatalf("status=%d output=%s", status, output.String())
	}
	assertDoctorReadOnly(t, runner)
}

const versionFormatForTest = "{{.Server.Version}}"

func doctorPrerequisiteRunner(t *testing.T, root, composeVersion string) *recordingRunner {
	t.Helper()
	runner := &recordingRunner{}
	runner.capture = func(command process.Command) ([]byte, []byte, error) {
		if command.Path == "git" {
			if reflect.DeepEqual(command.Args, []string{"--version"}) {
				return []byte("git version 2.45.0"), nil, nil
			}
			return []byte(root), nil, nil
		}
		switch {
		case reflect.DeepEqual(command.Args, []string{"--version"}):
			return []byte("Docker version 27.0.0"), nil, nil
		case reflect.DeepEqual(command.Args, []string{"version", "--format", versionFormatForTest}):
			return []byte("27.0.0"), nil, nil
		case reflect.DeepEqual(command.Args, []string{"compose", "version", "--short"}):
			return []byte(composeVersion), nil, nil
		case reflect.DeepEqual(command.Args, []string{"buildx", "version"}):
			return []byte("github.com/docker/buildx v0.16.0"), nil, nil
		case len(command.Args) >= 2 && reflect.DeepEqual(command.Args[:2], []string{"image", "inspect"}):
			return []byte("[]"), nil, nil
		case len(command.Args) >= 2 && reflect.DeepEqual(command.Args[:2], []string{"container", "inspect"}):
			return nil, []byte("No such container"), errors.New("exit 1")
		case len(command.Args) >= 2 && reflect.DeepEqual(command.Args[:2], []string{"container", "ls"}):
			return nil, nil, nil
		default:
			t.Fatalf("unexpected command: %s %v", command.Path, command.Args)
			return nil, nil, nil
		}
	}
	return runner
}

func assertDoctorReadOnly(t *testing.T, runner *recordingRunner) {
	t.Helper()
	if len(runner.attaches) != 0 {
		t.Fatalf("doctor invoked attached commands: %v", runner.attaches)
	}
	for _, command := range runner.commands {
		if command.Path != "docker" {
			continue
		}
		args := strings.Join(command.Args, " ")
		if strings.Contains(args, " build ") || strings.Contains(args, " create ") || strings.Contains(args, " rm ") || strings.Contains(args, " run ") || strings.Contains(args, " up ") || strings.Contains(args, " down ") || strings.Contains(args, " exec ") || strings.Contains(args, " start ") || strings.Contains(args, " stop ") {
			t.Errorf("doctor invoked mutating Docker command: %v", command.Args)
		}
	}
}

func inspectJSON(t *testing.T, container docker.Container) []byte {
	t.Helper()
	value := []any{map[string]any{
		"Id": container.ID, "Name": "/" + container.Name,
		"Config": map[string]any{"Labels": container.Labels},
		"State":  map[string]any{"Running": container.Running},
	}}
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return data
}
