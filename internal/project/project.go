// Package project resolves host projects and derives their stable runtime identity.
package project

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
)

const containerRoot = "/workspace/current"

var invalidSlugCharacters = regexp.MustCompile(`[^A-Za-z0-9_.-]`)

// GitRootFunc returns Git's worktree root for dir. An error means that Git is
// unavailable or dir is not in a worktree, in which case project resolution
// falls back to dir.
type GitRootFunc func(ctx context.Context, dir string) (string, error)

// Project is a physically resolved project and its deterministic identity.
type Project struct {
	RequestedDir    string
	RootDir         string
	RelativeWorkdir string
	Hash            string
	Slug            string
	ContainerName   string
	ComposeProject  string
}

// Resolve resolves requested relative to invocationDir. If gitRoot succeeds,
// its result becomes the project root and must contain the requested directory.
func Resolve(ctx context.Context, requested, invocationDir string, uid int, gitRoot GitRootFunc) (Project, error) {
	if uid < 0 {
		return Project{}, fmt.Errorf("UID must not be negative")
	}
	if requested == "" {
		requested = "."
	}

	requestedDir, err := PhysicalDirectory(requested, invocationDir)
	if err != nil {
		return Project{}, fmt.Errorf("resolve requested project directory: %w", err)
	}
	rootDir := requestedDir
	if gitRoot != nil {
		candidate, gitErr := gitRoot(ctx, requestedDir)
		candidate = strings.TrimRight(candidate, "\r\n")
		if gitErr == nil && candidate != "" {
			rootDir, err = PhysicalDirectory(candidate, requestedDir)
			if err != nil {
				return Project{}, fmt.Errorf("resolve Git worktree root %q: %w", candidate, err)
			}
		}
	}

	relative, err := filepath.Rel(rootDir, requestedDir)
	if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return Project{}, fmt.Errorf("requested directory %q is outside Git worktree root %q", requestedDir, rootDir)
	}
	if relative == "." {
		relative = ""
	} else {
		relative = filepath.ToSlash(relative)
	}

	digest := sha256.Sum256([]byte(rootDir))
	hash := hex.EncodeToString(digest[:])
	slug := Slug(filepath.Base(rootDir))
	uidString := strconv.Itoa(uid)

	return Project{
		RequestedDir:    requestedDir,
		RootDir:         rootDir,
		RelativeWorkdir: relative,
		Hash:            hash,
		Slug:            slug,
		ContainerName:   "wisp-" + uidString + "-" + slug + "-" + hash[:12],
		ComposeProject:  strings.ToLower("wisp-" + uidString + "-" + hash[:16]),
	}, nil
}

// PhysicalDirectory returns an absolute, symlink-free directory path. Relative
// names are interpreted from baseDir, which must itself be absolute.
func PhysicalDirectory(name, baseDir string) (string, error) {
	if name == "" {
		return "", fmt.Errorf("path is empty")
	}
	if strings.IndexByte(name, 0) >= 0 {
		return "", fmt.Errorf("path contains a NUL byte")
	}

	resolved := name
	if !filepath.IsAbs(resolved) {
		if !filepath.IsAbs(baseDir) {
			return "", fmt.Errorf("base directory %q is not absolute", baseDir)
		}
		resolved = filepath.Join(baseDir, resolved)
	}
	resolved = filepath.Clean(resolved)

	physical, err := filepath.EvalSymlinks(resolved)
	if err != nil {
		return "", fmt.Errorf("%q: %w", resolved, err)
	}
	physical, err = filepath.Abs(physical)
	if err != nil {
		return "", fmt.Errorf("make %q absolute: %w", physical, err)
	}
	physical = filepath.Clean(physical)

	info, err := os.Stat(physical)
	if err != nil {
		return "", fmt.Errorf("stat %q: %w", physical, err)
	}
	if !info.IsDir() {
		return "", fmt.Errorf("%q is not a directory", physical)
	}
	return physical, nil
}

// Slug converts a project basename to the bounded component used in a
// container name.
func Slug(base string) string {
	slug := invalidSlugCharacters.ReplaceAllString(base, "-")
	slug = strings.Trim(slug, "_.-")
	if slug == "" {
		slug = "project"
	}
	if len(slug) > 32 {
		slug = slug[:32]
	}
	return slug
}

// Workdir returns the requested directory's path in the sandbox.
func (p Project) Workdir() string {
	if p.RelativeWorkdir == "" {
		return containerRoot
	}
	return path.Join(containerRoot, p.RelativeWorkdir)
}

// LockKey is stable for a host user and physical project root.
func (p Project) LockKey(uid int) string {
	return strconv.Itoa(uid) + "-" + p.Hash
}

// Labels returns the labels required on a managed project container.
func (p Project) Labels(kind string, uid int, version string) map[string]string {
	return map[string]string{
		"wisp.managed":      "true",
		"wisp.kind":         kind,
		"wisp.owner-uid":    strconv.Itoa(uid),
		"wisp.project-hash": p.Hash,
		"wisp.version":      version,
	}
}
