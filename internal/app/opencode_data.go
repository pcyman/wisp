package app

import (
	"fmt"
	"os"
	"path/filepath"
	"syscall"

	"wisp/internal/agent"
	"wisp/internal/docker"
)

// prepareOpenCodeDataMount creates Wisp's private, project-scoped OpenCode
// data directory and resolves it physically before exposing it to Docker.
func prepareOpenCodeDataMount(root, projectHash string, uid int) (docker.Mount, error) {
	if !filepath.IsAbs(root) {
		return docker.Mount{}, fmt.Errorf("OpenCode data root %q is not absolute", root)
	}
	if projectHash == "" || filepath.Base(projectHash) != projectHash {
		return docker.Mount{}, fmt.Errorf("invalid project hash %q", projectHash)
	}

	base := filepath.Dir(root)
	if err := os.MkdirAll(base, 0o700); err != nil {
		return docker.Mount{}, fmt.Errorf("create OpenCode data base %q: %w", base, err)
	}
	physicalBase, err := filepath.EvalSymlinks(base)
	if err != nil {
		return docker.Mount{}, fmt.Errorf("resolve OpenCode data base %q: %w", base, err)
	}
	physicalBase, err = filepath.Abs(physicalBase)
	if err != nil {
		return docker.Mount{}, fmt.Errorf("make OpenCode data base %q absolute: %w", physicalBase, err)
	}

	current := filepath.Join(filepath.Clean(physicalBase), filepath.Base(root))
	for _, component := range []string{"projects", projectHash, "opencode"} {
		if err := ensurePrivateDataDirectory(current, uid); err != nil {
			return docker.Mount{}, err
		}
		current = filepath.Join(current, component)
	}
	if err := ensurePrivateDataDirectory(current, uid); err != nil {
		return docker.Mount{}, err
	}

	physical, err := filepath.EvalSymlinks(current)
	if err != nil {
		return docker.Mount{}, fmt.Errorf("resolve OpenCode data directory %q: %w", current, err)
	}
	mount, err := docker.Bind(physical, agent.OpenCodeDataTarget, false)
	if err != nil {
		return docker.Mount{}, fmt.Errorf("mount OpenCode data directory: %w", err)
	}
	return mount, nil
}

func ensurePrivateDataDirectory(name string, uid int) error {
	if err := os.Mkdir(name, 0o700); err != nil && !os.IsExist(err) {
		return fmt.Errorf("create OpenCode data directory %q: %w", name, err)
	}
	info, err := os.Lstat(name)
	if err != nil {
		return fmt.Errorf("stat OpenCode data directory %q: %w", name, err)
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || !ok || int(stat.Uid) != uid {
		return fmt.Errorf("OpenCode data directory %q must be a real directory owned by UID %d", name, uid)
	}
	if err := os.Chmod(name, 0o700); err != nil {
		return fmt.Errorf("secure OpenCode data directory %q: %w", name, err)
	}
	return nil
}
