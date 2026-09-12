package app

import (
	"fmt"
	"os"
	"path/filepath"
	"syscall"

	"wisp/internal/agent"
	"wisp/internal/docker"
)

// prepareAgentDataMounts creates private project-scoped writable state and
// returns it in parent-before-child mount order.
func prepareAgentDataMounts(root, projectHash string, uid int, agentKey string, sharedPiProfile bool) ([]docker.Mount, error) {
	switch agentKey {
	case "opencode":
		mount, err := prepareOpenCodeDataMount(root, projectHash, uid)
		if err != nil {
			return nil, err
		}
		return []docker.Mount{mount}, nil
	case "pi":
		return preparePiDataMounts(root, projectHash, uid, sharedPiProfile)
	default:
		return nil, fmt.Errorf("unsupported agent data plan %q", agentKey)
	}
}

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

func preparePiDataMounts(root, projectHash string, uid int, sharedProfile bool) ([]docker.Mount, error) {
	projectRoot, err := preparePrivateAgentDirectory(root, projectHash, "pi", uid)
	if err != nil {
		return nil, err
	}
	sessions := filepath.Join(projectRoot, "sessions")
	if err := ensurePrivateDataDirectory(sessions, uid); err != nil {
		return nil, err
	}
	xdgData := filepath.Join(projectRoot, "xdg-data")
	if err := ensurePrivateDataDirectory(xdgData, uid); err != nil {
		return nil, err
	}
	trust := filepath.Join(projectRoot, "trust.json")
	if err := ensurePrivateJSONFile(trust, uid); err != nil {
		return nil, err
	}

	mounts := make([]docker.Mount, 0, 3)
	if !sharedProfile {
		mount, err := docker.Bind(projectRoot, agent.PiAgentTarget, false)
		if err != nil {
			return nil, err
		}
		mounts = append(mounts, mount)
	}
	sessionMount, err := docker.Bind(sessions, agent.PiSessionsTarget, false)
	if err != nil {
		return nil, err
	}
	mounts = append(mounts, sessionMount)
	if sharedProfile {
		trustMount, err := docker.Bind(trust, agent.PiTrustTarget, false)
		if err != nil {
			return nil, err
		}
		mounts = append(mounts, trustMount)
	}
	xdgMount, err := docker.Bind(xdgData, agent.DataTarget, false)
	if err != nil {
		return nil, err
	}
	mounts = append(mounts, xdgMount)
	return mounts, nil
}

func preparePrivateAgentDirectory(root, projectHash, name string, uid int) (string, error) {
	if !filepath.IsAbs(root) {
		return "", fmt.Errorf("agent data root %q is not absolute", root)
	}
	if projectHash == "" || filepath.Base(projectHash) != projectHash {
		return "", fmt.Errorf("invalid project hash %q", projectHash)
	}
	base := filepath.Dir(root)
	if err := os.MkdirAll(base, 0o700); err != nil {
		return "", fmt.Errorf("create agent data base %q: %w", base, err)
	}
	physicalBase, err := filepath.EvalSymlinks(base)
	if err != nil {
		return "", fmt.Errorf("resolve agent data base %q: %w", base, err)
	}
	current := filepath.Join(filepath.Clean(physicalBase), filepath.Base(root))
	for _, component := range []string{"projects", projectHash, name} {
		if err := ensurePrivateDataDirectory(current, uid); err != nil {
			return "", err
		}
		current = filepath.Join(current, component)
	}
	if err := ensurePrivateDataDirectory(current, uid); err != nil {
		return "", err
	}
	return filepath.EvalSymlinks(current)
}

func ensurePrivateJSONFile(name string, uid int) error {
	file, err := os.OpenFile(name, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err == nil {
		if _, writeErr := file.WriteString("{}\n"); writeErr != nil {
			file.Close()
			return fmt.Errorf("initialize agent state file %q: %w", name, writeErr)
		}
		if closeErr := file.Close(); closeErr != nil {
			return fmt.Errorf("close agent state file %q: %w", name, closeErr)
		}
	} else if !os.IsExist(err) {
		return fmt.Errorf("create agent state file %q: %w", name, err)
	}
	info, err := os.Lstat(name)
	if err != nil {
		return fmt.Errorf("stat agent state file %q: %w", name, err)
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 || !ok || int(stat.Uid) != uid {
		return fmt.Errorf("agent state file %q must be a regular file owned by UID %d", name, uid)
	}
	return os.Chmod(name, 0o600)
}

func ensurePrivateDataDirectory(name string, uid int) error {
	if err := os.Mkdir(name, 0o700); err != nil && !os.IsExist(err) {
		return fmt.Errorf("create agent data directory %q: %w", name, err)
	}
	info, err := os.Lstat(name)
	if err != nil {
		return fmt.Errorf("stat agent data directory %q: %w", name, err)
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || !ok || int(stat.Uid) != uid {
		return fmt.Errorf("agent data directory %q must be a real directory owned by UID %d", name, uid)
	}
	if err := os.Chmod(name, 0o700); err != nil {
		return fmt.Errorf("secure agent data directory %q: %w", name, err)
	}
	return nil
}
