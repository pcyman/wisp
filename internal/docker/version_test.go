package docker

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
)

func TestCheckPrerequisites(t *testing.T) {
	runner := &fakeRunner{captureResults: []fakeResult{{stdout: "20.10.7\n"}, {stdout: "v2.24.0\n"}}}
	got, err := NewClient(runner).CheckPrerequisites(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if got.Docker != "20.10.7" || got.Compose != "v2.24.0" {
		t.Fatalf("versions = %#v", got)
	}
	want := [][]string{{"version", "--format", versionFormat}, {"compose", "version", "--short"}}
	for i := range want {
		if !reflect.DeepEqual(runner.captured[i].Args, want[i]) {
			t.Fatalf("command %d = %v, want %v", i, runner.captured[i].Args, want[i])
		}
	}
}

func TestVersionMinimums(t *testing.T) {
	tests := []struct {
		name    string
		value   string
		major   int
		minor   int
		wantErr bool
	}{
		{"exact", "v2.24.0-desktop.1", 2, 24, false},
		{"new major", "3.0.0", 2, 24, false},
		{"old compose", "2.23.9", 2, 24, true},
		{"buildx output", "github.com/docker/buildx v0.11.2 9872040", 0, 11, false},
		{"old buildx", "github.com/docker/buildx v0.10.5", 0, 11, true},
		{"garbage", "unknown", 2, 24, true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := requireVersion(test.value, test.major, test.minor, "component")
			if (err != nil) != test.wantErr {
				t.Fatalf("error = %v", err)
			}
		})
	}
}

func TestPrerequisiteErrorRetainsStderr(t *testing.T) {
	runner := &fakeRunner{captureResults: []fakeResult{{stderr: "daemon unavailable", err: errors.New("exit 1")}}}
	_, err := NewClient(runner).CheckPrerequisites(context.Background())
	if err == nil || !strings.Contains(err.Error(), "daemon unavailable") {
		t.Fatalf("error = %v", err)
	}
}
