package config

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"

	toml "github.com/pelletier/go-toml/v2"
)

// Decode strictly decodes one TOML v1 document.
func Decode(data []byte) (RawConfig, error) {
	var raw RawConfig
	decoder := toml.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&raw); err != nil {
		return RawConfig{}, err
	}
	return raw, nil
}

// Load reads a config once, strictly decodes it, validates it, and retains the
// exact input bytes in the returned result.
func Load(configPath string, env Environment) (Result, error) {
	if !filepath.IsAbs(configPath) {
		return Result{}, fmt.Errorf("config path %q is not absolute", configPath)
	}
	configPath = filepath.Clean(configPath)
	physicalPath, err := filepath.EvalSymlinks(configPath)
	if err != nil {
		return Result{}, fmt.Errorf("resolve config %q: %w", configPath, err)
	}
	configPath, err = filepath.Abs(physicalPath)
	if err != nil {
		return Result{}, fmt.Errorf("make config path absolute: %w", err)
	}
	configPath = filepath.Clean(configPath)
	data, err := os.ReadFile(configPath)
	if err != nil {
		return Result{}, fmt.Errorf("read config %q: %w", configPath, err)
	}
	raw, err := Decode(data)
	if err != nil {
		return Result{}, fmt.Errorf("parse config %q: %w", configPath, err)
	}
	cfg, err := Resolve(raw)
	if err != nil {
		return Result{}, fmt.Errorf("validate config %q: %w", configPath, err)
	}
	warnings, err := ValidateHostPaths(&cfg, configPath, env)
	if err != nil {
		return Result{}, fmt.Errorf("validate config %q: %w", configPath, err)
	}
	return Result{Path: configPath, Snapshot: data, Config: cfg, Warnings: warnings}, nil
}

// Validate is the config-command API. It has no Docker, network, or AWS side
// effects and otherwise performs the same checks as Load.
func Validate(configPath string, env Environment) (Result, error) {
	return Load(configPath, env)
}
