package runtimeassets

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"testing/fstest"
	"time"

	"github.com/gofrs/flock"
)

func TestMaterializeContentAddressedTreeAndModes(t *testing.T) {
	cacheBase := t.TempDir()
	source := testAssets("first")
	dir, err := Materialize(source, Environment{XDGCacheHome: cacheBase})
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Dir(dir) != filepath.Join(cacheBase, "wisp", "runtime") {
		t.Fatalf("Materialize() directory = %q", dir)
	}
	if hash := filepath.Base(dir); len(hash) != 64 || strings.Trim(hash, "0123456789abcdef") != "" {
		t.Fatalf("materialization hash = %q, want 64 lowercase hexadecimal characters", hash)
	}

	for _, name := range []string{filepath.Join(cacheBase, "wisp"), filepath.Join(cacheBase, "wisp", "runtime"), dir, filepath.Join(dir, "container"), filepath.Join(dir, "credentials")} {
		assertMode(t, name, 0o700)
	}
	for _, spec := range requiredAssets {
		name := filepath.Join(dir, filepath.FromSlash(spec.name))
		assertMode(t, name, spec.mode)
		got, err := os.ReadFile(name)
		if err != nil {
			t.Fatal(err)
		}
		want, err := fs.ReadFile(source, spec.name)
		if err != nil {
			t.Fatal(err)
		}
		if string(got) != string(want) {
			t.Fatalf("%s = %q, want %q", spec.name, got, want)
		}
	}
}

func TestContentHashIncludesNamesBytesAndModes(t *testing.T) {
	assets, err := load(testAssets("one"))
	if err != nil {
		t.Fatal(err)
	}
	original := contentHash(assets)

	changedData := cloneAssets(assets)
	changedData[0].data = []byte("different")
	changedName := cloneAssets(assets)
	changedName[0].name = "different-name"
	changedMode := cloneAssets(assets)
	changedMode[0].mode = 0o600
	reordered := cloneAssets(assets)
	reordered[0], reordered[1] = reordered[1], reordered[0]
	for name, changed := range map[string][]asset{
		"bytes": changedData,
		"names": changedName,
		"modes": changedMode,
		"order": reordered,
	} {
		if got := contentHash(changed); got == original {
			t.Errorf("hash did not change with %s", name)
		}
	}
	if again := contentHash(assets); again != original {
		t.Fatalf("hash is not deterministic: %q then %q", original, again)
	}
}

func TestMaterializeCacheResolution(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	tests := []struct {
		name    string
		env     Environment
		want    string
		wantErr string
	}{
		{name: "XDG takes precedence", env: Environment{XDGCacheHome: filepath.Join(root, "xdg"), Home: "relative"}, want: filepath.Join(root, "xdg", "wisp", "runtime")},
		{name: "HOME fallback", env: Environment{Home: filepath.Join(root, "home")}, want: filepath.Join(root, "home", ".cache", "wisp", "runtime")},
		{name: "relative XDG", env: Environment{XDGCacheHome: "cache", Home: root}, wantErr: "XDG_CACHE_HOME must be an absolute path"},
		{name: "relative HOME", env: Environment{Home: "home"}, wantErr: "HOME must be an absolute path"},
		{name: "missing environment", wantErr: "neither XDG_CACHE_HOME nor HOME is set"},
	}
	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			dir, err := Materialize(testAssets(test.name), test.env)
			if test.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), test.wantErr) {
					t.Fatalf("Materialize() error = %v, want containing %q", err, test.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if filepath.Dir(dir) != test.want {
				t.Fatalf("Materialize() parent = %q, want %q", filepath.Dir(dir), test.want)
			}
		})
	}
}

func TestMaterializeRequiresCanonicalRegularFiles(t *testing.T) {
	t.Parallel()
	missing := testAssets("missing")
	delete(missing, "compose.yaml")
	if _, err := Materialize(missing, Environment{XDGCacheHome: t.TempDir()}); err == nil || !strings.Contains(err.Error(), "compose.yaml") {
		t.Fatalf("missing asset error = %v", err)
	}

	nonRegular := testAssets("directory")
	nonRegular["compose.yaml"] = &fstest.MapFile{Mode: fs.ModeDir}
	if _, err := Materialize(nonRegular, Environment{XDGCacheHome: t.TempDir()}); err == nil || !strings.Contains(err.Error(), "not a regular file") {
		t.Fatalf("non-regular asset error = %v", err)
	}
	if _, err := Materialize(nil, Environment{XDGCacheHome: t.TempDir()}); err == nil || !strings.Contains(err.Error(), "filesystem is nil") {
		t.Fatalf("nil filesystem error = %v", err)
	}
}

func TestMaterializeReusesVerifiedEntryAndPreservesOldHashes(t *testing.T) {
	cacheBase := t.TempDir()
	first, err := Materialize(testAssets("first"), Environment{XDGCacheHome: cacheBase})
	if err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(first, "Dockerfile")
	before, err := os.Stat(marker)
	if err != nil {
		t.Fatal(err)
	}
	time.Sleep(10 * time.Millisecond)
	again, err := Materialize(testAssets("first"), Environment{XDGCacheHome: cacheBase})
	if err != nil {
		t.Fatal(err)
	}
	if again != first {
		t.Fatalf("second Materialize() = %q, want %q", again, first)
	}
	after, err := os.Stat(marker)
	if err != nil {
		t.Fatal(err)
	}
	if !after.ModTime().Equal(before.ModTime()) {
		t.Fatalf("verified asset was rewritten: modtime %v became %v", before.ModTime(), after.ModTime())
	}

	second, err := Materialize(testAssets("second"), Environment{XDGCacheHome: cacheBase})
	if err != nil {
		t.Fatal(err)
	}
	if second == first {
		t.Fatal("different bytes produced the same materialization path")
	}
	for _, dir := range []string{first, second} {
		if info, err := os.Stat(dir); err != nil || !info.IsDir() {
			t.Fatalf("materialization %q was deleted or replaced: info=%v error=%v", dir, info, err)
		}
	}
}

func TestMaterializeDetectsCorruptionWithoutRepair(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(t *testing.T, dir string)
	}{
		{name: "content", mutate: func(t *testing.T, dir string) {
			writeExisting(t, filepath.Join(dir, "compose.yaml"), []byte("corrupt"), 0o644)
		}},
		{name: "file mode", mutate: func(t *testing.T, dir string) {
			if err := os.Chmod(filepath.Join(dir, "Dockerfile"), 0o600); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "directory mode", mutate: func(t *testing.T, dir string) {
			if err := os.Chmod(filepath.Join(dir, "credentials"), 0o755); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "missing", mutate: func(t *testing.T, dir string) {
			if err := os.Remove(filepath.Join(dir, ".dockerignore")); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "extra", mutate: func(t *testing.T, dir string) {
			writeExisting(t, filepath.Join(dir, "extra"), []byte("extra"), 0o644)
		}},
		{name: "symlink", mutate: func(t *testing.T, dir string) {
			name := filepath.Join(dir, "Dockerfile")
			if err := os.Remove(name); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink("compose.yaml", name); err != nil {
				t.Fatal(err)
			}
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			cacheBase := t.TempDir()
			source := testAssets(test.name)
			dir, err := Materialize(source, Environment{XDGCacheHome: cacheBase})
			if err != nil {
				t.Fatal(err)
			}
			test.mutate(t, dir)
			_, err = Materialize(source, Environment{XDGCacheHome: cacheBase})
			if err == nil || !strings.Contains(err.Error(), "corrupt") {
				t.Fatalf("Materialize() error = %v, want corruption error", err)
			}
			if _, statErr := os.Lstat(dir); statErr != nil {
				t.Fatalf("corrupt materialization was deleted: %v", statErr)
			}
		})
	}
}

func TestMaterializeConcurrentFirstUse(t *testing.T) {
	cacheBase := t.TempDir()
	source := testAssets(strings.Repeat("concurrent", 1024))
	const workers = 32
	start := make(chan struct{})
	paths := make(chan string, workers)
	errs := make(chan error, workers)
	var group sync.WaitGroup
	for i := 0; i < workers; i++ {
		group.Add(1)
		go func() {
			defer group.Done()
			<-start
			dir, err := Materialize(source, Environment{XDGCacheHome: cacheBase})
			if err != nil {
				errs <- err
				return
			}
			paths <- dir
		}()
	}
	close(start)
	group.Wait()
	close(paths)
	close(errs)
	for err := range errs {
		t.Error(err)
	}
	var expected string
	for path := range paths {
		if expected == "" {
			expected = path
		}
		if path != expected {
			t.Errorf("concurrent path = %q, want %q", path, expected)
		}
	}
	if expected == "" {
		t.Fatal("no concurrent materialization succeeded")
	}
	assertCompleteTree(t, expected, source)
	entries, err := os.ReadDir(filepath.Dir(expected))
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if strings.Contains(entry.Name(), ".tmp-") {
			t.Fatalf("temporary directory remains after concurrent materialization: %s", entry.Name())
		}
	}
}

func TestMaterializeWaitsForPerHashAdvisoryLock(t *testing.T) {
	cacheBase := t.TempDir()
	source := testAssets("locked")
	assets, err := load(source)
	if err != nil {
		t.Fatal(err)
	}
	cache, err := cacheDirectory(Environment{XDGCacheHome: cacheBase})
	if err != nil {
		t.Fatal(err)
	}
	if err := ensureCacheDirectories(cache); err != nil {
		t.Fatal(err)
	}
	lock := flock.New(filepath.Join(cache, "."+contentHash(assets)+".lock"), flock.SetPermissions(0o600))
	if err := lock.Lock(); err != nil {
		t.Fatal(err)
	}

	done := make(chan error, 1)
	go func() {
		_, err := Materialize(source, Environment{XDGCacheHome: cacheBase})
		done <- err
	}()
	select {
	case err := <-done:
		t.Fatalf("Materialize() returned while advisory lock was held: %v", err)
	case <-time.After(100 * time.Millisecond):
	}
	if err := lock.Close(); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Materialize() did not continue after advisory lock release")
	}
}

func TestMaterializePublishesOnlyCompleteTarget(t *testing.T) {
	cacheBase := t.TempDir()
	source := testAssets(strings.Repeat("atomic", 1<<20))
	assets, err := load(source)
	if err != nil {
		t.Fatal(err)
	}
	cache, err := cacheDirectory(Environment{XDGCacheHome: cacheBase})
	if err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(cache, contentHash(assets))
	done := make(chan error, 1)
	go func() {
		_, err := Materialize(source, Environment{XDGCacheHome: cacheBase})
		done <- err
	}()

	for {
		select {
		case err := <-done:
			if err != nil {
				t.Fatal(err)
			}
			assertCompleteTree(t, target, source)
			return
		default:
			if _, err := os.Lstat(target); err == nil {
				assertCompleteTree(t, target, source)
			} else if !errors.Is(err, fs.ErrNotExist) {
				t.Fatal(err)
			}
		}
	}
}

func testAssets(seed string) fstest.MapFS {
	assets := make(fstest.MapFS, len(requiredAssets))
	for _, spec := range requiredAssets {
		assets[spec.name] = &fstest.MapFile{Data: []byte(fmt.Sprintf("%s:%s\n", seed, spec.name)), Mode: 0o444}
	}
	return assets
}

func cloneAssets(input []asset) []asset {
	result := make([]asset, len(input))
	for i, current := range input {
		result[i] = current
		result[i].data = append([]byte(nil), current.data...)
	}
	return result
}

func assertMode(t *testing.T, name string, want fs.FileMode) {
	t.Helper()
	info, err := os.Stat(name)
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != want {
		t.Fatalf("%s mode = %04o, want %04o", name, got, want)
	}
}

func assertCompleteTree(t *testing.T, dir string, source fs.FS) {
	t.Helper()
	for _, spec := range requiredAssets {
		got, err := os.ReadFile(filepath.Join(dir, filepath.FromSlash(spec.name)))
		if err != nil {
			t.Fatal(err)
		}
		want, err := fs.ReadFile(source, spec.name)
		if err != nil {
			t.Fatal(err)
		}
		if string(got) != string(want) {
			t.Fatalf("asset %q is incomplete", spec.name)
		}
		assertMode(t, filepath.Join(dir, filepath.FromSlash(spec.name)), spec.mode)
	}
}

func writeExisting(t *testing.T, name string, data []byte, mode fs.FileMode) {
	t.Helper()
	if err := os.WriteFile(name, data, mode); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(name, mode); err != nil {
		t.Fatal(err)
	}
}
