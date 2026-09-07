package agent

import (
	"wisp/internal/config"
	"wisp/internal/docker"
)

const (
	openCodeConfigTarget = "/run/wisp/opencode/config"
	// OpenCodeDataTarget is OpenCode's writable application directory beneath
	// the sandbox's XDG_DATA_HOME. Keep it outside HOME so Docker does not
	// create root-owned parents that block OpenCode from creating XDG state.
	OpenCodeDataTarget = "/run/wisp/opencode/data/opencode"
	openCodeAuthTarget = OpenCodeDataTarget + "/auth.json"
	hunkConfigTarget   = "/run/wisp/hunk/config.toml"
)

// OpenCode is Wisp's interactive sandbox agent.
type OpenCode struct{}

func (OpenCode) Name() string { return "OpenCode" }

// ContainerCommand deliberately accepts no host-provided arguments.
func (OpenCode) ContainerCommand() []string { return []string{"opencode"} }

// HostMounts projects paths already resolved and validated by config.Load.
// Missing default paths are omitted; config.Load supplies their warnings.
func (OpenCode) HostMounts(cfg config.Config) ([]docker.Mount, error) {
	mounts := make([]docker.Mount, 0, 3)
	if cfg.OpenCode.ConfigPath != "" {
		planned, err := docker.Bind(cfg.OpenCode.ConfigPath, openCodeConfigTarget, true)
		if err != nil {
			return nil, err
		}
		mounts = append(mounts, planned)
	}
	if cfg.OpenCode.AuthPath != "" {
		planned, err := docker.Bind(cfg.OpenCode.AuthPath, openCodeAuthTarget, true)
		if err != nil {
			return nil, err
		}
		mounts = append(mounts, planned)
	}
	if cfg.Hunk.ConfigPath != "" {
		planned, err := docker.Bind(cfg.Hunk.ConfigPath, hunkConfigTarget, true)
		if err != nil {
			return nil, err
		}
		mounts = append(mounts, planned)
	}
	return mounts, nil
}
