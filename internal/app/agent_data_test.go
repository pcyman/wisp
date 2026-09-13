package app

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"wisp/internal/agent"
)

func TestPrepareOpenCodeDataMountCreatesPrivateProjectDirectory(t *testing.T) {
	base := t.TempDir()
	root := filepath.Join(base, "data", "wisp")
	mount, err := prepareOpenCodeDataMount(root, "project-hash", os.Getuid())
	if err != nil {
		t.Fatal(err)
	}
	wantSource := filepath.Join(root, "projects", "project-hash", "opencode")
	if mount.Source != wantSource || mount.Target != agent.OpenCodeDataTarget || mount.ReadOnly || mount.Bind.CreateHostPath {
		t.Fatalf("mount = %#v, want writable bind from %q to %q", mount, wantSource, agent.OpenCodeDataTarget)
	}
	for _, current := range []string{
		root,
		filepath.Join(root, "projects"),
		filepath.Join(root, "projects", "project-hash"),
		wantSource,
	} {
		info, statErr := os.Stat(current)
		if statErr != nil {
			t.Fatal(statErr)
		}
		if info.Mode().Perm() != 0o700 {
			t.Fatalf("mode of %q = %#o, want 0700", current, info.Mode().Perm())
		}
	}
	marker := filepath.Join(wantSource, "opencode.db")
	if err := os.WriteFile(marker, []byte("session data"), 0o600); err != nil {
		t.Fatal(err)
	}
	second, err := prepareOpenCodeDataMount(root, "project-hash", os.Getuid())
	if err != nil {
		t.Fatal(err)
	}
	if contents, readErr := os.ReadFile(marker); readErr != nil || string(contents) != "session data" || second.Source != mount.Source {
		t.Fatalf("second mount did not preserve session data: mount=%#v contents=%q err=%v", second, contents, readErr)
	}
}

func TestPrepareOpenCodeDataMountResolvesDataBaseSymlink(t *testing.T) {
	root := t.TempDir()
	realData := filepath.Join(root, "real-data")
	if err := os.Mkdir(realData, 0o700); err != nil {
		t.Fatal(err)
	}
	linkedData := filepath.Join(root, "linked-data")
	if err := os.Symlink(realData, linkedData); err != nil {
		t.Fatal(err)
	}
	mount, err := prepareOpenCodeDataMount(filepath.Join(linkedData, "wisp"), "hash", os.Getuid())
	if err != nil {
		t.Fatal(err)
	}
	if want := filepath.Join(realData, "wisp", "projects", "hash", "opencode"); mount.Source != want {
		t.Fatalf("mount source = %q, want physical path %q", mount.Source, want)
	}
}

func TestPrepareOpenCodeDataMountRejectsManagedSymlink(t *testing.T) {
	root := t.TempDir()
	data := filepath.Join(root, "data")
	outside := filepath.Join(root, "outside")
	if err := os.MkdirAll(data, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(outside, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(data, "wisp")); err != nil {
		t.Fatal(err)
	}
	_, err := prepareOpenCodeDataMount(filepath.Join(data, "wisp"), "hash", os.Getuid())
	if err == nil || !strings.Contains(err.Error(), "real directory") {
		t.Fatalf("error = %v, want managed symlink rejection", err)
	}
}

func TestPiJITICacheKeyChangesWithImageIdentity(t *testing.T) {
	first, err := piJITICacheKey("sha256:first")
	if err != nil {
		t.Fatal(err)
	}
	second, err := piJITICacheKey("sha256:second")
	if err != nil {
		t.Fatal(err)
	}
	if first == second || len(first) != 64 || len(second) != 64 {
		t.Fatalf("Pi Jiti cache keys = %q and %q", first, second)
	}
}

func TestPreparePiJITICacheMountScopesByProfile(t *testing.T) {
	base := filepath.Join(t.TempDir(), "cache", "wisp")
	if err := os.MkdirAll(base, 0o700); err != nil {
		t.Fatal(err)
	}
	first, err := preparePiJITICacheMount(base, "/profiles/main", os.Getuid())
	if err != nil {
		t.Fatal(err)
	}
	same, err := preparePiJITICacheMount(base, "/profiles/main", os.Getuid())
	if err != nil {
		t.Fatal(err)
	}
	otherProfile, err := preparePiJITICacheMount(base, "/profiles/other", os.Getuid())
	if err != nil {
		t.Fatal(err)
	}
	if first.Target != agent.PiJITICacheTarget || first.ReadOnly || first.Bind.CreateHostPath {
		t.Fatalf("Pi Jiti cache mount = %#v", first)
	}
	if same.Source != first.Source || otherProfile.Source == first.Source {
		t.Fatalf("cache scoping failed: first=%q same=%q profile=%q", first.Source, same.Source, otherProfile.Source)
	}
	runtimeKey, err := piJITICacheKey("sha256:runtime")
	if err != nil {
		t.Fatal(err)
	}
	if err := preparePiJITIRuntimeCache(first.Source, runtimeKey, os.Getuid()); err != nil {
		t.Fatal(err)
	}
	if info, statErr := os.Lstat(filepath.Join(first.Source, runtimeKey)); statErr != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm() != 0o700 {
		t.Fatalf("runtime cache directory is not private and real: info=%v err=%v", info, statErr)
	}
	for current := first.Source; current != base; current = filepath.Dir(current) {
		info, statErr := os.Stat(current)
		if statErr != nil {
			t.Fatal(statErr)
		}
		if info.Mode().Perm() != 0o700 {
			t.Fatalf("mode of %q = %#o, want 0700", current, info.Mode().Perm())
		}
	}
}

func TestPreparePiDataMountsWithSharedProfile(t *testing.T) {
	root := filepath.Join(t.TempDir(), "data", "wisp")
	mounts, err := preparePiDataMounts(root, "hash", os.Getuid(), true)
	if err != nil {
		t.Fatal(err)
	}
	if len(mounts) != 3 || mounts[0].Target != agent.PiSessionsTarget || mounts[1].Target != agent.PiTrustTarget || mounts[2].Target != agent.DataTarget {
		t.Fatalf("Pi mounts = %#v", mounts)
	}
	if mounts[0].ReadOnly || mounts[1].ReadOnly || mounts[2].ReadOnly {
		t.Fatalf("Pi project state is not writable: %#v", mounts)
	}
	trust, err := os.ReadFile(mounts[1].Source)
	if err != nil || string(trust) != "{}\n" {
		t.Fatalf("trust state = %q, %v", trust, err)
	}
}

func TestPreparePiDataMountsWithoutSharedProfile(t *testing.T) {
	root := filepath.Join(t.TempDir(), "data", "wisp")
	mounts, err := preparePiDataMounts(root, "hash", os.Getuid(), false)
	if err != nil {
		t.Fatal(err)
	}
	if len(mounts) != 3 || mounts[0].Target != agent.PiAgentTarget || mounts[1].Target != agent.PiSessionsTarget || mounts[2].Target != agent.DataTarget {
		t.Fatalf("Pi mounts = %#v", mounts)
	}
	if filepath.Dir(mounts[1].Source) != mounts[0].Source {
		t.Fatalf("session source %q is not in local profile %q", mounts[1].Source, mounts[0].Source)
	}
}
