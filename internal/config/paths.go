package config

import (
	"fmt"
	"path/filepath"
	"strings"
)

// ResolvePath returns the absolute application config path. An override is
// resolved against invocationDir; the default follows XDG_CONFIG_HOME.
func ResolvePath(override, invocationDir string, env Environment) (string, error) {
	if override != "" {
		if filepath.IsAbs(override) {
			return filepath.Clean(override), nil
		}
		if invocationDir == "" {
			return "", fmt.Errorf("resolve relative config path: invocation directory is empty")
		}
		if !filepath.IsAbs(invocationDir) {
			return "", fmt.Errorf("resolve relative config path: invocation directory %q is not absolute", invocationDir)
		}
		return filepath.Clean(filepath.Join(invocationDir, override)), nil
	}

	base, err := xdgBase(env.XDGConfigHome, env.Home, ".config", "XDG_CONFIG_HOME")
	if err != nil {
		return "", fmt.Errorf("determine default config path: %w", err)
	}
	return filepath.Join(base, "wisp", "config.toml"), nil
}

func xdgBase(value, home, fallback, name string) (string, error) {
	if value != "" {
		if !filepath.IsAbs(value) {
			return "", fmt.Errorf("%s must be an absolute path, got %q", name, value)
		}
		return filepath.Clean(value), nil
	}
	if home == "" {
		return "", fmt.Errorf("neither %s nor HOME is set", name)
	}
	if !filepath.IsAbs(home) {
		return "", fmt.Errorf("HOME must be an absolute path, got %q", home)
	}
	return filepath.Join(filepath.Clean(home), fallback), nil
}

func resolveTOMLPath(value, configDir, home string) (string, error) {
	if strings.HasPrefix(value, "~/") {
		if home == "" {
			return "", fmt.Errorf("cannot expand %q because HOME is not set", value)
		}
		if !filepath.IsAbs(home) {
			return "", fmt.Errorf("HOME must be an absolute path, got %q", home)
		}
		value = filepath.Join(home, strings.TrimPrefix(value, "~/"))
	}
	if !filepath.IsAbs(value) {
		value = filepath.Join(configDir, value)
	}
	return filepath.Clean(value), nil
}
