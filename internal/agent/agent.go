// Package agent contains the small internal boundary between Wisp and the
// interactive agent installed in the sandbox.
package agent

import (
	"fmt"
	"strings"

	"wisp/internal/config"
	"wisp/internal/docker"
)

// DataTarget is the writable XDG data root shared by sandbox tools. Each
// harness maps project-scoped host state here without sharing it across repos.
const DataTarget = "/run/wisp/agent/data"

// Agent describes the agent-specific pieces of a sandbox run.
type Agent interface {
	Key() string
	Name() string
	ContainerCommand() []string
	HostMounts(config.Config) ([]docker.Mount, error)
}

// Select returns a supported agent using requested, configured-default
// precedence. Agent names are deliberately closed rather than executable input.
func Select(requested, configured string) (Agent, error) {
	name := requested
	if name == "" {
		name = configured
	}
	switch strings.ToLower(name) {
	case "opencode":
		return OpenCode{}, nil
	case "pi":
		return Pi{}, nil
	default:
		return nil, fmt.Errorf("unknown agent %q (expected opencode or pi)", name)
	}
}

func Default() Agent { return OpenCode{} }
