// Package mount resolves and validates extra repository bind mounts.
package mount

import (
	"fmt"
	"os"
	"path"
	"path/filepath"
	"strings"
)

const repositoriesRoot = "/workspace/repos"

var reservedTargets = []string{
	"/workspace/current",
	"/home/sandbox",
	"/run/wisp",
}

// Mode is the access mode of a repository bind mount.
type Mode string

const (
	ReadOnly  Mode = "ro"
	ReadWrite Mode = "rw"
)

// Spec is an unresolved mount from application configuration.
type Spec struct {
	Source string
	Target string
	Mode   Mode
}

// Mount is a fully resolved extra repository mount.
type Mount struct {
	Source string
	Target string
	Mode   Mode
}

// Plan resolves config mounts relative to configDir and CLI mounts relative to
// invocationDir. Config mounts are returned first, but collisions across all
// sources are rejected.
func Plan(config []Spec, readOnlyCLI, readWriteCLI []string, configDir, invocationDir, home string) ([]Mount, error) {
	planned := make([]Mount, 0, len(config)+len(readOnlyCLI)+len(readWriteCLI))
	for i, spec := range config {
		mode := spec.Mode
		if mode == "" {
			mode = ReadOnly
		}
		if err := validateMode(mode); err != nil {
			return nil, fmt.Errorf("config mount %d: %w", i+1, err)
		}
		source, err := resolveSource(spec.Source, configDir, home, true)
		if err != nil {
			return nil, fmt.Errorf("config mount %d source: %w", i+1, err)
		}
		if err := ValidateTarget(spec.Target); err != nil {
			return nil, fmt.Errorf("config mount %d target: %w", i+1, err)
		}
		planned = append(planned, Mount{Source: source, Target: spec.Target, Mode: mode})
	}

	addCLI := func(sources []string, mode Mode, option string) error {
		for i, name := range sources {
			source, err := resolveSource(name, invocationDir, "", false)
			if err != nil {
				return fmt.Errorf("%s value %d: %w", option, i+1, err)
			}
			base := filepath.Base(source)
			target := path.Join(repositoriesRoot, base)
			if err := ValidateTarget(target); err != nil {
				return fmt.Errorf("%s value %d target derived from %q: %w", option, i+1, source, err)
			}
			planned = append(planned, Mount{Source: source, Target: target, Mode: mode})
		}
		return nil
	}
	if err := addCLI(readOnlyCLI, ReadOnly, "--mount"); err != nil {
		return nil, err
	}
	if err := addCLI(readWriteCLI, ReadWrite, "--mount-rw"); err != nil {
		return nil, err
	}

	if err := validateCollisions(planned); err != nil {
		return nil, err
	}
	return planned, nil
}

// ValidateTarget applies the complete extra-repository container target policy.
func ValidateTarget(target string) error {
	if target == "" {
		return fmt.Errorf("target is empty")
	}
	if strings.IndexByte(target, 0) >= 0 {
		return fmt.Errorf("target contains a NUL byte")
	}
	if !path.IsAbs(target) {
		return fmt.Errorf("target %q is not absolute", target)
	}
	if cleaned := path.Clean(target); cleaned != target {
		return fmt.Errorf("target %q is not clean (use %q)", target, cleaned)
	}
	if !isDescendant(target, repositoriesRoot) {
		return fmt.Errorf("target %q must be a descendant of %q", target, repositoriesRoot)
	}
	for _, reserved := range reservedTargets {
		if overlaps(target, reserved) {
			return fmt.Errorf("target %q overlaps reserved path %q", target, reserved)
		}
	}
	return nil
}

func resolveSource(source, baseDir, home string, expandHome bool) (string, error) {
	if source == "" {
		return "", fmt.Errorf("source is empty")
	}
	if strings.IndexByte(source, 0) >= 0 {
		return "", fmt.Errorf("source contains a NUL byte")
	}
	if expandHome && strings.HasPrefix(source, "~/") {
		if !filepath.IsAbs(home) {
			return "", fmt.Errorf("cannot expand %q: HOME is not absolute", source)
		}
		source = filepath.Join(home, source[2:])
	}
	if !filepath.IsAbs(source) {
		if !filepath.IsAbs(baseDir) {
			return "", fmt.Errorf("base directory %q is not absolute", baseDir)
		}
		source = filepath.Join(baseDir, source)
	}
	source = filepath.Clean(source)
	physical, err := filepath.EvalSymlinks(source)
	if err != nil {
		return "", fmt.Errorf("resolve %q: %w", source, err)
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

func validateMode(mode Mode) error {
	if mode != ReadOnly && mode != ReadWrite {
		return fmt.Errorf("mode %q must be %q or %q", mode, ReadOnly, ReadWrite)
	}
	return nil
}

func validateCollisions(mounts []Mount) error {
	for i := range mounts {
		for j := 0; j < i; j++ {
			if mounts[i].Target == mounts[j].Target {
				return fmt.Errorf("mount targets collide: %q is used more than once", mounts[i].Target)
			}
			if overlaps(mounts[i].Target, mounts[j].Target) {
				return fmt.Errorf("mount targets overlap: %q and %q", mounts[j].Target, mounts[i].Target)
			}
		}
	}
	return nil
}

func overlaps(a, b string) bool {
	return a == b || isDescendant(a, b) || isDescendant(b, a)
}

func isDescendant(candidate, parent string) bool {
	return strings.HasPrefix(candidate, parent+"/")
}
