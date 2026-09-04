package docker

import (
	"context"
	"errors"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"

	"wisp/internal/process"
)

func TestComposeInvocation(t *testing.T) {
	root := filepath.Join(string(filepath.Separator), "cache", "wisp runtime")
	override := filepath.Join(root, "invocation", "override.yaml")
	invocation := ComposeInvocation{ProjectName: "wisp-1-abc", RuntimeRoot: root, Override: override, Environment: []string{"PATH=/bin"}}
	runner := &fakeRunner{captureResults: []fakeResult{{stdout: "ok"}}}
	_, _, err := NewClient(runner).ComposeCapture(context.Background(), invocation, "up", "--detach", "--wait", "credentials")
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"compose", "--project-name", "wisp-1-abc", "--project-directory", root, "--file", filepath.Join(root, "compose.yaml"), "--file", override, "up", "--detach", "--wait", "credentials"}
	if !reflect.DeepEqual(runner.captured[0].Args, want) {
		t.Fatalf("args = %#v\nwant %#v", runner.captured[0].Args, want)
	}
	if runner.captured[0].Dir != root || !reflect.DeepEqual(runner.captured[0].Env, invocation.Environment) {
		t.Fatalf("command = %#v", runner.captured[0])
	}
}

func TestSanitizeEnvironment(t *testing.T) {
	inherited := []string{"PATH=/bin", "WISP_UID=evil", "DOCKER_HOST=unix:///socket", "OPENCODE_VERSION=evil", "TERM=xterm"}
	planned := map[string]string{"WISP_UID": "42", "OPENCODE_VERSION": "", "WISP_IMAGE": "wisp:test", "TERM": "screen"}
	want := []string{"PATH=/bin", "DOCKER_HOST=unix:///socket", "OPENCODE_VERSION=", "TERM=screen", "WISP_IMAGE=wisp:test", "WISP_UID=42"}
	if got := SanitizeEnvironment(inherited, planned); !reflect.DeepEqual(got, want) {
		t.Fatalf("environment = %#v\nwant %#v", got, want)
	}
}

func TestDirectDockerCommandsStripInheritedPrivateEnvironment(t *testing.T) {
	private := make([]string, 0, len(privateEnvironment))
	for key := range privateEnvironment {
		private = append(private, key+"=inherited")
	}
	sort.Strings(private)
	preserved := []string{"PATH=/bin", "DOCKER_HOST=unix:///socket", "DOCKER_CONTEXT=local", "DOCKER_TLS_VERIFY=1", "DOCKER_CERT_PATH=/certs"}
	runner := &fakeRunner{captureResults: []fakeResult{
		{stdout: "Docker version 27.0.0"}, {stdout: "27.0.0"}, {stdout: "2.29.0"}, {stdout: "github.com/docker/buildx v0.16.0"},
		{stdout: "[]"}, {stderr: "No such container", err: errors.New("exit 1")}, {stdout: ""}, {stdout: ""}, {stdout: ""},
	}, attachedResults: []error{nil}}
	client := NewClientWithEnvironment(runner, append(append([]string(nil), preserved...), private...))
	if _, err := client.CheckCLI(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := client.CheckDocker(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := client.CheckCompose(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := client.CheckBuildx(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := client.ImageExists(context.Background(), "wisp:local"); err != nil {
		t.Fatal(err)
	}
	if container, err := client.InspectContainer(context.Background(), "missing"); err != nil || container != nil {
		t.Fatalf("container=%v err=%v", container, err)
	}
	if containers, err := client.ProjectContainers(context.Background(), "project"); err != nil || len(containers) != 0 {
		t.Fatalf("containers=%v err=%v", containers, err)
	}
	if containers, err := client.ManagedProjectContainers(context.Background(), "hash", 123); err != nil || len(containers) != 0 {
		t.Fatalf("managed containers=%v err=%v", containers, err)
	}
	if err := client.RemoveContainer(context.Background(), "stopped"); err != nil {
		t.Fatal(err)
	}
	if err := client.Attached(context.Background(), []string{"exec", "container", "true"}, append(append([]string(nil), preserved...), private...), nil, nil, nil); err != nil {
		t.Fatal(err)
	}
	commands := append(append([]process.Command(nil), runner.captured...), runner.attached...)
	for _, command := range commands {
		if !reflect.DeepEqual(command.Env, preserved) {
			t.Errorf("direct Docker environment for %v = %#v, want %#v", command.Args, command.Env, preserved)
		}
		for key := range privateEnvironment {
			if strings.Contains(strings.Join(command.Env, "\n"), key+"=") {
				t.Errorf("direct Docker command %v inherited private %s", command.Args, key)
			}
		}
	}
}
