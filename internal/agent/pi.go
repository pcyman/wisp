package agent

import (
	"wisp/internal/config"
	"wisp/internal/docker"
)

const (
	PiAgentTarget    = "/run/wisp/pi/agent"
	PiSessionsTarget = "/run/wisp/pi/sessions"
	PiTrustTarget    = PiAgentTarget + "/trust.json"
)

// Pi is Wisp's Pi agent-harness integration.
type Pi struct{}

func (Pi) Key() string  { return "pi" }
func (Pi) Name() string { return "Pi" }

// The bundled extension is explicit so status reporting does not depend on
// either global extension discovery or project trust.
func (Pi) ContainerCommand() []string {
	return []string{"pi", "-e", "/usr/local/share/wisp/pi-agent-status.mjs"}
}

func (Pi) HostMounts(cfg config.Config) ([]docker.Mount, error) {
	mounts := make([]docker.Mount, 0, 2)
	if cfg.Pi.ConfigPath != "" {
		planned, err := docker.Bind(cfg.Pi.ConfigPath, PiAgentTarget, false)
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
