// Package lock implements Wisp's Unix advisory process locks.
package lock

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// ErrContended indicates that another process holds a nonblocking lock.
var ErrContended = errors.New("lock is held by another process")

// Lock is an advisory lock backed by a harmless persistent lock file.
type Lock struct {
	file *os.File
}

// RuntimeRoot selects the private directory used for invocation and lock state.
func RuntimeRoot(xdgRuntimeDir, tempDir string, uid int) (string, error) {
	if uid < 0 {
		return "", fmt.Errorf("UID must not be negative")
	}
	if xdgRuntimeDir != "" && !filepath.IsAbs(xdgRuntimeDir) {
		return "", fmt.Errorf("XDG_RUNTIME_DIR %q is not absolute", xdgRuntimeDir)
	}
	if xdgRuntimeDir != "" {
		if usableRuntimeDir(xdgRuntimeDir, uid) {
			return ensurePrivate(filepath.Join(xdgRuntimeDir, "wisp"), uid)
		}
	}
	if tempDir == "" {
		tempDir = os.TempDir()
	}
	if !filepath.IsAbs(tempDir) {
		return "", fmt.Errorf("temporary directory %q is not absolute", tempDir)
	}
	return ensurePrivate(filepath.Join(tempDir, "wisp-"+strconv.Itoa(uid)), uid)
}

// Try acquires key without waiting.
func Try(root, key string) (*Lock, error) {
	file, err := open(root, key)
	if err != nil {
		return nil, err
	}
	if err := syscall.Flock(int(file.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		_ = file.Close()
		if errors.Is(err, syscall.EWOULDBLOCK) || errors.Is(err, syscall.EAGAIN) {
			return nil, ErrContended
		}
		return nil, fmt.Errorf("lock %q: %w", key, err)
	}
	return &Lock{file: file}, nil
}

// Acquire waits for key until ctx is canceled.
func Acquire(ctx context.Context, root, key string) (*Lock, error) {
	for {
		locked, err := Try(root, key)
		if !errors.Is(err, ErrContended) {
			return locked, err
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(25 * time.Millisecond):
		}
	}
}

// Close releases the lock.
func (l *Lock) Close() error {
	if l == nil || l.file == nil {
		return nil
	}
	err := syscall.Flock(int(l.file.Fd()), syscall.LOCK_UN)
	closeErr := l.file.Close()
	l.file = nil
	if err != nil {
		return err
	}
	return closeErr
}

func open(root, key string) (*os.File, error) {
	if key == "" || strings.ContainsAny(key, `/\\`) || strings.IndexByte(key, 0) >= 0 || key == "." || key == ".." {
		return nil, fmt.Errorf("invalid lock key %q", key)
	}
	if err := os.MkdirAll(root, 0700); err != nil {
		return nil, fmt.Errorf("create lock directory: %w", err)
	}
	info, err := os.Lstat(root)
	if err != nil {
		return nil, fmt.Errorf("stat lock directory: %w", err)
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return nil, fmt.Errorf("lock directory %q is not a real directory", root)
	}
	if err := os.Chmod(root, 0700); err != nil {
		return nil, fmt.Errorf("secure lock directory: %w", err)
	}
	name := filepath.Join(root, key+".lock")
	fd, err := syscall.Open(name, syscall.O_CREAT|syscall.O_RDWR|syscall.O_CLOEXEC|syscall.O_NOFOLLOW, 0600)
	if err != nil {
		return nil, fmt.Errorf("open lock file: %w", err)
	}
	file := os.NewFile(uintptr(fd), name)
	return file, nil
}

func usableRuntimeDir(name string, uid int) bool {
	info, err := os.Stat(name)
	if err != nil || !info.IsDir() || info.Mode().Perm()&0200 == 0 {
		return false
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	return ok && int(stat.Uid) == uid
}

func ensurePrivate(name string, uid int) (string, error) {
	if err := os.MkdirAll(name, 0700); err != nil {
		return "", fmt.Errorf("create runtime directory %q: %w", name, err)
	}
	info, err := os.Lstat(name)
	if err != nil {
		return "", fmt.Errorf("stat runtime directory %q: %w", name, err)
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return "", fmt.Errorf("runtime directory %q must not be a symlink", name)
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !info.IsDir() || !ok || int(stat.Uid) != uid {
		return "", fmt.Errorf("runtime directory %q is not owned by UID %d", name, uid)
	}
	if err := os.Chmod(name, 0700); err != nil {
		return "", fmt.Errorf("secure runtime directory %q: %w", name, err)
	}
	return name, nil
}
