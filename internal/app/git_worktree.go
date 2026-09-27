package app

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"

	"wisp/internal/docker"
	"wisp/internal/project"
)

const gitMetadataTarget = "/run/wisp/git"

// linkedWorktree describes a standard Git linked worktree. The common Git
// directory contains the worktree-specific admin directory as well as shared
// objects and refs; Git needs write access to both for add/commit/fetch.
type linkedWorktree struct {
	commonDir string
	adminName string
}

// planLinkedWorktree inspects only the physically resolved project root. A
// regular .git file is a linked-worktree pointer; ordinary .git directories
// need no additional mounts. Reject unsupported layouts rather than launching
// a sandbox with a broken or misleading Git repository.
func planLinkedWorktree(root string) (*linkedWorktree, error) {
	gitFile := filepath.Join(root, ".git")
	info, err := os.Lstat(gitFile)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("inspect worktree .git: %w", err)
	}
	if info.IsDir() {
		return nil, nil
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("worktree .git must be a regular file or directory")
	}
	pointer, err := readGitMetadataFile(gitFile)
	if err != nil {
		return nil, err
	}
	if !strings.HasPrefix(pointer, "gitdir: ") {
		return nil, fmt.Errorf("unsupported worktree .git pointer")
	}
	admin, err := physicalGitDirectory(strings.TrimPrefix(pointer, "gitdir: "), root)
	if err != nil {
		return nil, fmt.Errorf("resolve worktree Git directory: %w", err)
	}
	commonPointer, err := readGitMetadataFile(filepath.Join(admin, "commondir"))
	if err != nil {
		return nil, err
	}
	if commonPointer == "" || filepath.IsAbs(commonPointer) {
		return nil, fmt.Errorf("unsupported worktree commondir (expected relative path)")
	}
	common, err := physicalGitDirectory(commonPointer, admin)
	if err != nil {
		return nil, fmt.Errorf("resolve common Git directory: %w", err)
	}
	adminRelative, err := filepath.Rel(common, admin)
	if err != nil || filepath.Dir(adminRelative) != "worktrees" || filepath.Base(adminRelative) == "." {
		return nil, fmt.Errorf("unsupported worktree Git directory layout")
	}
	// The unchanged commondir file must also resolve correctly after the
	// common directory is mounted at its container location.
	if filepath.Clean(filepath.Join(gitMetadataTarget, adminRelative, commonPointer)) != gitMetadataTarget {
		return nil, fmt.Errorf("worktree commondir cannot be relocated")
	}
	backlink, err := readGitMetadataFile(filepath.Join(admin, "gitdir"))
	if err != nil {
		return nil, err
	}
	if backlink == "" || !filepath.IsAbs(backlink) || filepath.Clean(backlink) != gitFile {
		return nil, fmt.Errorf("worktree Git backlink does not point to %q; run git worktree repair on the host", gitFile)
	}
	return &linkedWorktree{commonDir: common, adminName: filepath.Base(admin)}, nil
}

func physicalGitDirectory(name, base string) (string, error) {
	if name == "" || strings.IndexByte(name, 0) >= 0 {
		return "", fmt.Errorf("invalid Git directory path")
	}
	return project.PhysicalDirectory(name, base)
}

// Metadata pointers must be bounded, regular files opened without following
// their final symlink. Do not read arbitrary files named by untrusted Git data.
func readGitMetadataFile(name string) (string, error) {
	fd, err := os.OpenFile(name, os.O_RDONLY|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return "", fmt.Errorf("read Git metadata %q: %w", name, err)
	}
	defer fd.Close()
	info, err := fd.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Size() > 4096 {
		return "", fmt.Errorf("Git metadata %q is not a bounded regular file", name)
	}
	data := make([]byte, info.Size())
	if _, err := io.ReadFull(fd, data); err != nil {
		return "", fmt.Errorf("read Git metadata %q: %w", name, err)
	}
	value := strings.TrimSuffix(string(data), "\n")
	value = strings.TrimSuffix(value, "\r")
	if strings.ContainsAny(value, "\r\n\x00") {
		return "", fmt.Errorf("Git metadata %q contains invalid characters", name)
	}
	return value, nil
}

// mounts adds a generated gitfile and backlink on top of the writable shared
// metadata mount. The generated files stay in Wisp's private invocation dir;
// no host repository file is rewritten.
func (w *linkedWorktree) mounts(invocationDir string) ([]docker.Mount, error) {
	if w == nil {
		return nil, nil
	}
	gitFile := filepath.Join(invocationDir, "worktree.git")
	backlink := filepath.Join(invocationDir, "worktree.gitdir")
	adminTarget := filepath.Join(gitMetadataTarget, "worktrees", w.adminName)
	if err := os.WriteFile(gitFile, []byte("gitdir: "+adminTarget+"\n"), 0600); err != nil {
		return nil, fmt.Errorf("create worktree Git pointer: %w", err)
	}
	if err := os.WriteFile(backlink, []byte(projectTarget+"/.git\n"), 0600); err != nil {
		return nil, fmt.Errorf("create worktree Git backlink: %w", err)
	}
	var mounts []docker.Mount
	for _, spec := range []struct {
		source, target string
		readOnly       bool
	}{
		{w.commonDir, gitMetadataTarget, false},
		{gitFile, projectTarget + "/.git", true},
		{backlink, adminTarget + "/gitdir", true},
	} {
		mount, err := docker.Bind(spec.source, spec.target, spec.readOnly)
		if err != nil {
			return nil, err
		}
		mounts = append(mounts, mount)
	}
	return mounts, nil
}
