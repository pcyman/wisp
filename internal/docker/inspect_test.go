package docker

import (
	"context"
	"errors"
	"strings"
	"testing"
)

func TestImageExists(t *testing.T) {
	runner := &fakeRunner{captureResults: []fakeResult{
		{stdout: "[]"},
		{stderr: "Error: No such image: missing", err: errors.New("exit 1")},
		{stderr: "permission denied", err: errors.New("exit 1")},
	}}
	client := NewClient(runner)
	if exists, err := client.ImageExists(context.Background(), "present"); err != nil || !exists {
		t.Fatalf("present = %v, %v", exists, err)
	}
	if exists, err := client.ImageExists(context.Background(), "missing"); err != nil || exists {
		t.Fatalf("missing = %v, %v", exists, err)
	}
	if _, err := client.ImageExists(context.Background(), "broken"); err == nil || !strings.Contains(err.Error(), "permission denied") {
		t.Fatalf("failure = %v", err)
	}
}

func TestInspectContainerAndLabels(t *testing.T) {
	runner := &fakeRunner{captureResults: []fakeResult{{stdout: `[{
      "Id":"abc", "Name":"/wisp-project",
      "Config":{"Labels":{"wisp.managed":"true","wisp.kind":"sandbox"}},
      "State":{"Running":true}
    }]`}}}
	container, err := NewClient(runner).InspectContainer(context.Background(), "wisp-project")
	if err != nil {
		t.Fatal(err)
	}
	if container.ID != "abc" || container.Name != "wisp-project" || !container.Running {
		t.Fatalf("container = %#v", container)
	}
	if err := VerifyLabels(container.Labels, map[string]string{"wisp.managed": "true", "wisp.kind": "sandbox"}); err != nil {
		t.Fatal(err)
	}
	if err := VerifyLabels(container.Labels, map[string]string{"wisp.kind": "credentials"}); err == nil {
		t.Fatal("mismatched kind accepted")
	}
	if err := VerifyLabels(container.Labels, map[string]string{"wisp.owner-uid": "1000"}); err == nil {
		t.Fatal("missing owner label accepted")
	}
}

func TestMissingContainer(t *testing.T) {
	runner := &fakeRunner{captureResults: []fakeResult{{stderr: "No such container: absent", err: errors.New("exit 1")}}}
	container, err := NewClient(runner).InspectContainer(context.Background(), "absent")
	if err != nil || container != nil {
		t.Fatalf("container = %#v, error = %v", container, err)
	}
}

func TestManagedProjectContainersUsesOwnershipFilters(t *testing.T) {
	runner := &fakeRunner{captureResults: []fakeResult{{stdout: ""}}}
	containers, err := NewClient(runner).ManagedProjectContainers(context.Background(), "abc123", 42)
	if err != nil || len(containers) != 0 {
		t.Fatalf("containers=%v error=%v", containers, err)
	}
	want := []string{
		"container", "ls", "--all",
		"--filter", "label=wisp.managed=true",
		"--filter", "label=wisp.owner-uid=42",
		"--filter", "label=wisp.project-hash=abc123",
		"--format", "{{.Names}}",
	}
	if got := runner.captured[0].Args; strings.Join(got, "\x00") != strings.Join(want, "\x00") {
		t.Fatalf("args=%v want=%v", got, want)
	}
}
