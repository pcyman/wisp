package agent

import (
	"wisp/internal/config"
	"wisp/internal/docker"
)

const (
	openCodeConfigTarget = "/run/wisp/opencode/config"
	openCodeAuthTarget   = "/run/wisp/opencode/auth.json"
)

// OpenCode is Wisp's interactive sandbox agent.
type OpenCode struct{}

func (OpenCode) Name() string { return "OpenCode" }

// ContainerCommand deliberately accepts no host-provided arguments.
func (OpenCode) ContainerCommand() []string { return []string{"opencode"} }

// HostMounts projects paths already resolved and validated by config.Load.
// Missing default paths are omitted; config.Load supplies their warnings.
func (OpenCode) HostMounts(cfg config.Config) ([]docker.Mount, error) {
	mounts := make([]docker.Mount, 0, 2)
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
	return mounts, nil
}
