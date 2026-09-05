// Package mountpolicy defines container target rules for extra repositories.
package mountpolicy

import (
	"fmt"
	"path"
	"strings"
)

const RepositoriesRoot = "/workspace/repos"

// ValidateTarget requires a clean absolute descendant of RepositoriesRoot.
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
	if !IsDescendant(target, RepositoriesRoot) {
		return fmt.Errorf("target %q must be a descendant of %q", target, RepositoriesRoot)
	}
	return nil
}

// Overlaps reports whether either clean path contains the other.
func Overlaps(a, b string) bool {
	return a == b || IsDescendant(a, b) || IsDescendant(b, a)
}

// IsDescendant reports whether candidate is strictly below parent.
func IsDescendant(candidate, parent string) bool {
	return strings.HasPrefix(candidate, parent+"/")
}
