package project

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestResolveWithoutGitUsesPhysicalRequestedDirectory(t *testing.T) {
	base := t.TempDir()
	realDir := filepath.Join(base, "real project")
	mustMkdirAll(t, realDir)
	link := filepath.Join(base, "link")
	if err := os.Symlink(realDir, link); err != nil {
		t.Fatal(err)
	}

	calledWith := ""
	p, err := Resolve(context.Background(), "link", base, 1001, func(_ context.Context, dir string) (string, error) {
		calledWith = dir
		return "", errors.New("not a repository")
	})
	if err != nil {
		t.Fatal(err)
	}
	if calledWith != realDir {
		t.Fatalf("Git called with %q, want %q", calledWith, realDir)
	}
	if p.RequestedDir != realDir || p.RootDir != realDir {
		t.Fatalf("directories = %#v, want physical path %q", p, realDir)
	}
	if p.RelativeWorkdir != "" || p.Workdir() != "/workspace/current" {
		t.Fatalf("workdir fields = %q, %q", p.RelativeWorkdir, p.Workdir())
	}
}

func TestResolveGitSubdirectoryAndIdentity(t *testing.T) {
	base := t.TempDir()
	root := filepath.Join(base, "My Project")
	requested := filepath.Join(root, "services", "api")
	mustMkdirAll(t, requested)

	p, err := Resolve(context.Background(), requested, base, 42, func(context.Context, string) (string, error) {
		return root + "\n", nil
	})
	if err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256([]byte(root))
	wantHash := hex.EncodeToString(digest[:])
	if p.Hash != wantHash {
		t.Fatalf("hash = %q, want %q", p.Hash, wantHash)
	}
	if p.RelativeWorkdir != "services/api" || p.Workdir() != "/workspace/current/services/api" {
		t.Fatalf("unexpected workdir: relative=%q container=%q", p.RelativeWorkdir, p.Workdir())
	}
	if p.Slug != "My-Project" {
		t.Fatalf("slug = %q", p.Slug)
	}
	if p.ContainerName != "wisp-42-My-Project-"+wantHash[:12] {
		t.Fatalf("container name = %q", p.ContainerName)
	}
	if p.ComposeProject != "wisp-42-"+wantHash[:16] {
		t.Fatalf("compose project = %q", p.ComposeProject)
	}
	if p.LockKey(42) != "42-"+wantHash {
		t.Fatalf("lock key = %q", p.LockKey(42))
	}

	wantLabels := map[string]string{
		"wisp.managed":      "true",
		"wisp.kind":         "sandbox",
		"wisp.owner-uid":    "42",
		"wisp.project-hash": wantHash,
		"wisp.version":      "1.2.3",
	}
	if got := p.Labels("sandbox", 42, "1.2.3"); !reflect.DeepEqual(got, wantLabels) {
		t.Fatalf("labels = %#v, want %#v", got, wantLabels)
	}
}

func TestResolveSeparateRootsHaveSeparateIdentities(t *testing.T) {
	base := t.TempDir()
	first := filepath.Join(base, "worktree-one")
	second := filepath.Join(base, "worktree-two")
	mustMkdirAll(t, first)
	mustMkdirAll(t, second)

	a, err := Resolve(context.Background(), first, base, 7, func(_ context.Context, dir string) (string, error) { return dir, nil })
	if err != nil {
		t.Fatal(err)
	}
	b, err := Resolve(context.Background(), second, base, 7, func(_ context.Context, dir string) (string, error) { return dir, nil })
	if err != nil {
		t.Fatal(err)
	}
	if a.Hash == b.Hash || a.ContainerName == b.ContainerName || a.ComposeProject == b.ComposeProject {
		t.Fatalf("separate worktrees share identity: %#v %#v", a, b)
	}
}

func TestResolveRejectsInvalidInputsAndGitRoot(t *testing.T) {
	base := t.TempDir()
	requested := filepath.Join(base, "repo", "child")
	outside := filepath.Join(base, "other")
	mustMkdirAll(t, requested)
	mustMkdirAll(t, outside)
	file := filepath.Join(base, "file")
	if err := os.WriteFile(file, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name       string
		requested  string
		base       string
		uid        int
		git        GitRootFunc
		wantSubstr string
	}{
		{name: "negative UID", requested: requested, base: base, uid: -1, wantSubstr: "UID"},
		{name: "relative base", requested: ".", base: "relative", uid: 1, wantSubstr: "not absolute"},
		{name: "missing", requested: "missing", base: base, uid: 1, wantSubstr: "no such file"},
		{name: "regular file", requested: file, base: base, uid: 1, wantSubstr: "not a directory"},
		{name: "outside Git root", requested: requested, base: base, uid: 1, git: func(context.Context, string) (string, error) { return outside, nil }, wantSubstr: "outside Git worktree"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := Resolve(context.Background(), tt.requested, tt.base, tt.uid, tt.git)
			if err == nil || !strings.Contains(err.Error(), tt.wantSubstr) {
				t.Fatalf("error = %v, want substring %q", err, tt.wantSubstr)
			}
		})
	}
}

func TestSlug(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"normal_name-1.2", "normal_name-1.2"},
		{".. awkward name ..", "awkward-name"},
		{"---", "project"},
		{"éclair", "clair"},
		{"abcdefghijklmnopqrstuvwxyz0123456789", "abcdefghijklmnopqrstuvwxyz012345"},
	}
	for _, tt := range tests {
		if got := Slug(tt.input); got != tt.want {
			t.Errorf("Slug(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}
}

func mustMkdirAll(t *testing.T, path string) {
	t.Helper()
	if err := os.MkdirAll(path, 0o700); err != nil {
		t.Fatal(err)
	}
}
