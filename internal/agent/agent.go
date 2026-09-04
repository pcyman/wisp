// Package agent contains the small internal boundary between Wisp and the
// interactive agent installed in the sandbox.
package agent

import (
	"wisp/internal/config"
	"wisp/internal/docker"
)

// Agent describes the agent-specific pieces of a sandbox run.
type Agent interface {
	Name() string
	ContainerCommand() []string
	HostMounts(config.Config) ([]docker.Mount, error)
}

// Default returns the only agent supported by Wisp version 1.
func Default() Agent { return OpenCode{} }
