package lock

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

func TestTryContentionAndRelease(t *testing.T) {
	root := t.TempDir()
	first, err := Try(root, "project")
	if err != nil {
		t.Fatal(err)
	}
	defer first.Close()
	if _, err := Try(root, "project"); !errors.Is(err, ErrContended) {
		t.Fatalf("second Try error = %v", err)
	}
	if err := first.Close(); err != nil {
		t.Fatal(err)
	}
	second, err := Acquire(context.Background(), root, "project")
	if err != nil {
		t.Fatal(err)
	}
	_ = second.Close()
}

func TestRuntimeRoot(t *testing.T) {
	temp := t.TempDir()
	uid := os.Getuid()
	got, err := RuntimeRoot("", temp, uid)
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(temp, "wisp-"+stringUID(uid))
	if got != want {
		t.Fatalf("root = %q, want %q", got, want)
	}
	info, _ := os.Stat(got)
	if info.Mode().Perm() != 0700 {
		t.Fatalf("mode = %o", info.Mode().Perm())
	}
}

func TestRuntimeRootRejectsRelativeXDGRuntimeDirectory(t *testing.T) {
	if _, err := RuntimeRoot("relative/runtime", t.TempDir(), os.Getuid()); err == nil {
		t.Fatal("relative XDG_RUNTIME_DIR was accepted")
	}
}

func TestRejectsUnsafeKey(t *testing.T) {
	if _, err := Try(t.TempDir(), "../escape"); err == nil {
		t.Fatal("unsafe key accepted")
	}
}

func TestRuntimeRootRejectsExistingSymlink(t *testing.T) {
	temp := t.TempDir()
	target := t.TempDir()
	name := filepath.Join(temp, "wisp-"+stringUID(os.Getuid()))
	if err := os.Symlink(target, name); err != nil {
		t.Fatal(err)
	}
	if _, err := RuntimeRoot("", temp, os.Getuid()); err == nil {
		t.Fatal("symlink runtime root accepted")
	}
}

func TestTryRejectsSymlinkLockFile(t *testing.T) {
	root := t.TempDir()
	target := filepath.Join(t.TempDir(), "target")
	if err := os.WriteFile(target, nil, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, filepath.Join(root, "project.lock")); err != nil {
		t.Fatal(err)
	}
	if _, err := Try(root, "project"); err == nil {
		t.Fatal("symlink lock file accepted")
	}
}

func stringUID(uid int) string {
	return fmt.Sprintf("%d", uid)
}
