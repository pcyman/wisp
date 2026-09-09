// Package runtimeassets materializes the embedded Docker runtime into an
// immutable, content-addressed cache directory.
package runtimeassets

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"

	"wisp/internal/lock"
)

const directoryMode fs.FileMode = 0o700

type assetSpec struct {
	name string
	mode fs.FileMode
}

// Keep this list lexically ordered so hashing and writing never depend on
// filesystem iteration order.
var requiredAssets = []assetSpec{
	{name: ".dockerignore", mode: 0o644},
	{name: "Dockerfile", mode: 0o644},
	{name: "compose.yaml", mode: 0o644},
	{name: "container/agent-status.js", mode: 0o644},
	{name: "container/entrypoint.sh", mode: 0o755},
	{name: "container/opencode.json", mode: 0o644},
	{name: "credentials/.dockerignore", mode: 0o644},
	{name: "credentials/Dockerfile", mode: 0o644},
	{name: "credentials/broker.py", mode: 0o644},
}

var requiredDirectories = map[string]bool{
	".":           true,
	"container":   true,
	"credentials": true,
}

// Environment contains the environment values used to locate the runtime
// cache. XDGCacheHome takes precedence over Home.
type Environment struct {
	XDGCacheHome string
	Home         string
}

type asset struct {
	assetSpec
	data []byte
}

// Materialize returns the immutable directory containing assets. It creates a
// content-addressed materialization when absent and rejects an existing entry
// that does not exactly match the expected contents and modes.
func Materialize(source fs.FS, env Environment) (dir string, err error) {
	assets, err := load(source)
	if err != nil {
		return "", err
	}
	hash := contentHash(assets)

	cache, err := cacheDirectory(env)
	if err != nil {
		return "", err
	}
	if err := ensureCacheDirectories(cache); err != nil {
		return "", err
	}

	target := filepath.Join(cache, hash)
	assetLock, err := lock.Acquire(context.Background(), cache, "."+hash)
	if err != nil {
		return "", fmt.Errorf("lock runtime assets %s: %w", hash, err)
	}
	defer func() {
		if closeErr := assetLock.Close(); err == nil && closeErr != nil {
			dir = ""
			err = fmt.Errorf("unlock runtime assets %s: %w", hash, closeErr)
		}
	}()

	exists, err := verify(target, assets, hash)
	if err != nil {
		return "", err
	}
	if exists {
		return target, nil
	}
	if err := create(cache, target, assets, hash); err != nil {
		return "", err
	}
	return target, nil
}

func load(source fs.FS) ([]asset, error) {
	if source == nil {
		return nil, fmt.Errorf("load runtime assets: filesystem is nil")
	}
	assets := make([]asset, 0, len(requiredAssets))
	for _, spec := range requiredAssets {
		info, err := fs.Stat(source, spec.name)
		if err != nil {
			return nil, fmt.Errorf("load runtime asset %q: %w", spec.name, err)
		}
		if !info.Mode().IsRegular() {
			return nil, fmt.Errorf("load runtime asset %q: not a regular file", spec.name)
		}
		data, err := fs.ReadFile(source, spec.name)
		if err != nil {
			return nil, fmt.Errorf("load runtime asset %q: %w", spec.name, err)
		}
		assets = append(assets, asset{assetSpec: spec, data: data})
	}
	return assets, nil
}

func contentHash(assets []asset) string {
	h := sha256.New()
	var encoded [8]byte
	for _, current := range assets {
		binary.BigEndian.PutUint64(encoded[:], uint64(len(current.name)))
		_, _ = h.Write(encoded[:])
		_, _ = h.Write([]byte(current.name))
		binary.BigEndian.PutUint64(encoded[:], uint64(current.mode.Perm()))
		_, _ = h.Write(encoded[:])
		binary.BigEndian.PutUint64(encoded[:], uint64(len(current.data)))
		_, _ = h.Write(encoded[:])
		_, _ = h.Write(current.data)
	}
	return hex.EncodeToString(h.Sum(nil))
}

func cacheDirectory(env Environment) (string, error) {
	base := env.XDGCacheHome
	name := "XDG_CACHE_HOME"
	if base == "" {
		if env.Home == "" {
			return "", fmt.Errorf("determine runtime cache: neither XDG_CACHE_HOME nor HOME is set")
		}
		base = filepath.Join(env.Home, ".cache")
		name = "HOME"
	}
	if !filepath.IsAbs(base) {
		return "", fmt.Errorf("determine runtime cache: %s must be an absolute path, got %q", name, base)
	}
	return filepath.Join(filepath.Clean(base), "wisp", "runtime"), nil
}

func ensureCacheDirectories(cache string) error {
	base := filepath.Dir(filepath.Dir(cache))
	if err := os.MkdirAll(base, directoryMode); err != nil {
		return fmt.Errorf("create cache base %q: %w", base, err)
	}
	for _, dir := range []string{filepath.Join(base, "wisp"), cache} {
		if err := ensurePrivateDirectory(dir); err != nil {
			return err
		}
	}
	return nil
}

func ensurePrivateDirectory(name string) error {
	if err := os.Mkdir(name, directoryMode); err != nil && !errors.Is(err, fs.ErrExist) {
		return fmt.Errorf("create private cache directory %q: %w", name, err)
	}
	info, err := os.Lstat(name)
	if err != nil {
		return fmt.Errorf("inspect private cache directory %q: %w", name, err)
	}
	if !info.IsDir() {
		return fmt.Errorf("private cache path %q is not a directory", name)
	}
	if err := os.Chmod(name, directoryMode); err != nil {
		return fmt.Errorf("set private cache directory mode on %q: %w", name, err)
	}
	return nil
}

func verify(target string, expected []asset, expectedHash string) (bool, error) {
	info, err := os.Lstat(target)
	if errors.Is(err, fs.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("inspect runtime materialization %q: %w", target, err)
	}
	if !info.IsDir() {
		return false, fmt.Errorf("runtime materialization %q is corrupt: root is not a directory", target)
	}

	expectedFiles := make(map[string]asset, len(expected))
	for _, current := range expected {
		expectedFiles[current.name] = current
	}
	seen := make(map[string]bool, len(expected))
	err = fs.WalkDir(os.DirFS(target), ".", func(name string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		entryInfo, err := entry.Info()
		if err != nil {
			return err
		}
		if entry.IsDir() {
			if !requiredDirectories[name] {
				return fmt.Errorf("unexpected directory %q", name)
			}
			if entryInfo.Mode().Perm() != directoryMode {
				return fmt.Errorf("directory %q has mode %04o, want %04o", name, entryInfo.Mode().Perm(), directoryMode)
			}
			return nil
		}
		wanted, ok := expectedFiles[name]
		if !ok {
			return fmt.Errorf("unexpected file %q", name)
		}
		if !entryInfo.Mode().IsRegular() {
			return fmt.Errorf("asset %q is not a regular file", name)
		}
		if entryInfo.Mode().Perm() != wanted.mode {
			return fmt.Errorf("asset %q has mode %04o, want %04o", name, entryInfo.Mode().Perm(), wanted.mode)
		}
		seen[name] = true
		return nil
	})
	if err != nil {
		return false, fmt.Errorf("runtime materialization %q is corrupt: %w", target, err)
	}

	actual := make([]asset, 0, len(expected))
	for _, wanted := range expected {
		if !seen[wanted.name] {
			return false, fmt.Errorf("runtime materialization %q is corrupt: missing asset %q", target, wanted.name)
		}
		data, err := os.ReadFile(filepath.Join(target, filepath.FromSlash(wanted.name)))
		if err != nil {
			return false, fmt.Errorf("runtime materialization %q is corrupt: read asset %q: %w", target, wanted.name, err)
		}
		actual = append(actual, asset{assetSpec: wanted.assetSpec, data: data})
	}
	if actualHash := contentHash(actual); actualHash != expectedHash {
		return false, fmt.Errorf("runtime materialization %q is corrupt: content hash is %s, want %s", target, actualHash, expectedHash)
	}
	return true, nil
}

func create(cache, target string, assets []asset, hash string) (err error) {
	temporary, err := os.MkdirTemp(cache, "."+hash+".tmp-")
	if err != nil {
		return fmt.Errorf("create temporary runtime directory: %w", err)
	}
	defer func() {
		if temporary != "" {
			_ = os.RemoveAll(temporary)
		}
	}()
	if err := os.Chmod(temporary, directoryMode); err != nil {
		return fmt.Errorf("set temporary runtime directory mode: %w", err)
	}
	for _, name := range []string{"container", "credentials"} {
		if err := os.Mkdir(filepath.Join(temporary, name), directoryMode); err != nil {
			return fmt.Errorf("create runtime directory %q: %w", name, err)
		}
	}
	for _, current := range assets {
		name := filepath.Join(temporary, filepath.FromSlash(current.name))
		if err := writeFile(name, current.data, current.mode); err != nil {
			return fmt.Errorf("write runtime asset %q: %w", current.name, err)
		}
	}
	for _, name := range []string{"container", "credentials", "."} {
		if err := syncDirectory(filepath.Join(temporary, name)); err != nil {
			return fmt.Errorf("sync runtime directory %q: %w", name, err)
		}
	}
	if err := os.Rename(temporary, target); err != nil {
		if exists, verifyErr := verify(target, assets, hash); exists && verifyErr == nil {
			return nil
		}
		return fmt.Errorf("publish runtime materialization %q: %w", target, err)
	}
	temporary = ""
	if err := syncDirectory(cache); err != nil {
		return fmt.Errorf("sync runtime cache directory: %w", err)
	}
	return nil
}

func writeFile(name string, data []byte, mode fs.FileMode) (err error) {
	file, err := os.OpenFile(name, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	defer func() {
		if closeErr := file.Close(); err == nil && closeErr != nil {
			err = closeErr
		}
	}()
	if _, err := file.Write(data); err != nil {
		return err
	}
	if err := file.Chmod(mode); err != nil {
		return err
	}
	return file.Sync()
}

func syncDirectory(name string) error {
	dir, err := os.Open(name)
	if err != nil {
		return err
	}
	syncErr := dir.Sync()
	closeErr := dir.Close()
	if syncErr != nil {
		return syncErr
	}
	return closeErr
}
