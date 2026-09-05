// Package hostpath resolves existing host filesystem paths physically.
package hostpath

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
)

// Resolve follows symlinks and returns a clean absolute path and its metadata.
func Resolve(candidate string) (string, fs.FileInfo, error) {
	physical, err := filepath.EvalSymlinks(candidate)
	if err != nil {
		return "", nil, fmt.Errorf("resolve %q: %w", candidate, err)
	}
	physical, err = filepath.Abs(physical)
	if err != nil {
		return "", nil, fmt.Errorf("make %q absolute: %w", physical, err)
	}
	physical = filepath.Clean(physical)
	info, err := os.Stat(physical)
	if err != nil {
		return "", nil, fmt.Errorf("stat %q: %w", physical, err)
	}
	return physical, info, nil
}
