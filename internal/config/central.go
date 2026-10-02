package config

import (
	"fmt"
	"path/filepath"
	"strings"
	"unicode"
)

type RawCentralConfig struct {
	Projects []RawCentralProject `toml:"projects"`
}

type RawCentralProject struct {
	Name *string `toml:"name"`
	Path *string `toml:"path"`
}

type CentralConfig struct {
	Projects []CentralProject
}

type CentralProject struct {
	Name string
	Path string
}

func resolveCentral(raw *RawCentralConfig) (CentralConfig, error) {
	var result CentralConfig
	if raw == nil {
		return result, nil
	}
	names := make(map[string]bool)
	for i, entry := range raw.Projects {
		if entry.Name == nil || strings.TrimSpace(*entry.Name) == "" || strings.IndexFunc(*entry.Name, unicode.IsControl) >= 0 {
			return result, fmt.Errorf("central.projects[%d].name must be nonempty and contain no control characters", i)
		}
		if names[*entry.Name] {
			return result, fmt.Errorf("duplicate central project name %q", *entry.Name)
		}
		if entry.Path == nil || strings.TrimSpace(*entry.Path) == "" {
			return result, fmt.Errorf("central.projects[%d].path must be nonempty", i)
		}
		names[*entry.Name] = true
		result.Projects = append(result.Projects, CentralProject{Name: *entry.Name, Path: *entry.Path})
	}
	return result, nil
}

// CentralProjects resolves catalog directories only when Central consumes them.
// An unavailable bookmark must not prevent ordinary runs in other projects.
func CentralProjects(cfg Config, configPath string, env Environment) ([]CentralProject, error) {
	result := make([]CentralProject, 0, len(cfg.Central.Projects))
	for i, entry := range cfg.Central.Projects {
		candidate, err := resolveTOMLPath(entry.Path, filepath.Dir(configPath), env.Home)
		if err != nil {
			return nil, fmt.Errorf("central.projects[%d].path: %w", i, err)
		}
		physical, err := requirePath(candidate, pathDirectory)
		if err != nil {
			return nil, fmt.Errorf("central project %q: %w", entry.Name, err)
		}
		result = append(result, CentralProject{Name: entry.Name, Path: physical})
	}
	return result, nil
}
