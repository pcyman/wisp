package agentstatus

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/sys/unix"
)

func TestRegistryMetadataRoundTrip(t *testing.T) {
	for _, pid := range []int{0, 12345} {
		root := t.TempDir()
		if err := os.Chmod(root, 0700); err != nil {
			t.Fatal(err)
		}
		r, err := Register(root, Metadata{UID: os.Getuid(), HostPID: pid, Repo: "/repo", SandboxName: "sandbox", ProjectHash: strings.Repeat("a", 64), ComposeProject: "compose"})
		if err != nil {
			t.Fatal(err)
		}
		defer r.Cleanup()
		data, err := os.ReadFile(filepath.Join(r.dir, "metadata.json"))
		if err != nil {
			t.Fatal(err)
		}
		var stored map[string]any
		if err := json.Unmarshal(data, &stored); err != nil {
			t.Fatal(err)
		}
		if pid == 0 {
			if _, ok := stored["host_pid"]; ok {
				t.Fatal("legacy registration should omit host_pid")
			}
		} else if stored["host_pid"] != float64(pid) {
			t.Fatalf("metadata=%s", data)
		}
		entries, err := List(root, os.Getuid())
		if err != nil || len(entries) != 1 || entries[0].Metadata != r.Metadata {
			t.Fatalf("entries=%+v registration=%+v err=%v", entries, r.Metadata, err)
		}
	}
}

func TestRegistryReports(t *testing.T) {
	root := t.TempDir()
	// Test temp directories may have permissive modes on some hosts.
	if err := os.Chmod(root, 0700); err != nil {
		t.Fatal(err)
	}
	r, err := Register(root, Metadata{UID: os.Getuid(), Repo: "/repo", SandboxName: "sandbox", ProjectHash: strings.Repeat("a", 64), ComposeProject: "compose"})
	if err != nil {
		t.Fatal(err)
	}
	defer r.Cleanup()
	file := filepath.Join(r.StatusDir, "agent.json")
	check := func(want, reporter string) {
		t.Helper()
		entries, err := List(root, os.Getuid())
		if err != nil || len(entries) != 1 || entries[0].Report.State != want || entries[0].Report.Reporter != reporter {
			t.Fatalf("entries=%+v err=%v", entries, err)
		}
		if reporter != "ready" && (entries[0].Report.Reason != "" || entries[0].Report.UpdatedAt != "") {
			t.Fatalf("invalid report leaked fields: %+v", entries[0].Report)
		}
	}
	check("unknown", "missing")
	for _, test := range []struct{ body, want, reporter string }{
		{`{"schema_version":1,"state":"working","reason":"retry","updated_at":"2020-01-01T00:00:00Z","summary":"secret","reporter":"spoofed"}`, "working", "ready"},
		{`{"schema_version":2,"state":"idle","updated_at":"2020-01-01T00:00:00Z"}`, "unknown", "unsupported"},
		{`{"schema_version":2,"state":{"future":"format"}}`, "unknown", "unsupported"},
		{`{"schema_version":1,"state":"working","updated_at":"bad"}`, "unknown", "invalid"},
		{`{"schema_version":1,"state":"secret","updated_at":"2020-01-01T00:00:00Z"}`, "unknown", "invalid"},
		{`{"schema_version":1,"state":"waiting","reason":"secret","updated_at":"2020-01-01T00:00:00Z"}`, "unknown", "invalid"},
		{`{"state":"idle"}`, "unknown", "invalid"},
		{`{"schema_version":null}`, "unknown", "invalid"},
		{`{"schema_version":"2"}`, "unknown", "invalid"},
		{`{"schema_version":0}`, "unknown", "invalid"},
		{`{"schema_version":2`, "unknown", "invalid"},
		{strings.Repeat(" ", maxFileSize+1), "unknown", "invalid"},
	} {
		if err := os.WriteFile(file, []byte(test.body), 0600); err != nil {
			t.Fatal(err)
		}
		check(test.want, test.reporter)
	}
	if err := os.Remove(file); err != nil {
		t.Fatal(err)
	}
	if err := unix.Mkfifo(file, 0600); err != nil {
		t.Fatal(err)
	}
	check("unknown", "invalid")
	os.Remove(file)
	if err := os.Symlink("/etc/passwd", file); err != nil {
		t.Fatal(err)
	}
	check("unknown", "invalid")
	os.Remove(file)
	if err := os.Remove(r.StatusDir); err != nil {
		t.Fatal(err)
	}
	check("unknown", "missing")
	if err := os.Symlink(root, r.StatusDir); err != nil {
		t.Fatal(err)
	}
	check("unknown", "invalid")
	if err := r.Cleanup(); err != nil {
		t.Fatal(err)
	}
	entries, err := List(root, os.Getuid())
	if err != nil || len(entries) != 0 {
		t.Fatalf("cleanup: %+v %v", entries, err)
	}
}

func TestRegistryRejectsSymlinkAndPublicDirectory(t *testing.T) {
	root := t.TempDir()
	link := filepath.Join(root, "link")
	if err := os.Symlink(root, link); err != nil {
		t.Fatal(err)
	}
	if _, err := List(link, os.Getuid()); err == nil {
		t.Fatal("accepted symlink")
	}
	os.Chmod(root, 0755)
	if _, err := List(root, os.Getuid()); err == nil {
		t.Fatal("accepted public directory")
	}
}
