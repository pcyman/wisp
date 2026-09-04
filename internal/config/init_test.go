package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestInitCreatesPrivateConfig(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "new", "wisp", "config.toml")
	if err := Init(path); err != nil {
		t.Fatalf("Init() error = %v", err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != 0o600 {
		t.Fatalf("config mode = %#o, want 0600", got)
	}
	parentInfo, err := os.Stat(filepath.Dir(path))
	if err != nil {
		t.Fatal(err)
	}
	if got := parentInfo.Mode().Perm(); got != 0o700 {
		t.Fatalf("parent mode = %#o, want 0700", got)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != InitialConfig || !strings.Contains(string(data), "configure") && !strings.Contains(string(data), "[aws]") {
		t.Fatal("created config does not contain the initial template")
	}
	entries, err := os.ReadDir(filepath.Dir(path))
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Name() != "config.toml" {
		t.Fatalf("temporary files remain: %v", entries)
	}
}

func TestInitRefusesExistingFile(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(path, []byte("original"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := Init(path); err == nil {
		t.Fatal("Init() overwrote an existing file")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "original" {
		t.Fatalf("existing contents changed to %q", data)
	}
}

func TestInitRefusesSymlink(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	target := filepath.Join(root, "target")
	path := filepath.Join(root, "config.toml")
	if err := os.WriteFile(target, []byte("target"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, path); err != nil {
		t.Fatal(err)
	}
	if err := Init(path); err == nil {
		t.Fatal("Init() followed or replaced a symlink")
	}
	data, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "target" {
		t.Fatalf("symlink target contents changed to %q", data)
	}
}

func TestInitConcurrentNoOverwrite(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.toml")
	results := make(chan error, 2)
	go func() { results <- Init(path) }()
	go func() { results <- Init(path) }()
	successes := 0
	for range 2 {
		if err := <-results; err == nil {
			successes++
		}
	}
	if successes != 1 {
		t.Fatalf("successful concurrent Init calls = %d, want 1", successes)
	}
}
