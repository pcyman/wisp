// Package herdr prepares Wisp's host process for Herdr agent detection.
package herdr

import (
	"fmt"
	"strings"
	"syscall"
)

const (
	AgentOpenCode = "opencode"
	agentVariable = "HERDR_AGENT"
)

// Plan describes replacement of a `wisp herdr` process with a normal Wisp
// invocation carrying Herdr's OpenCode agent hint.
type Plan struct {
	Required bool
	Argv     []string
	Env      []string
}

// BuildPlan removes the command-position "herdr" token and normalizes the
// process environment. argv includes argv[0].
func BuildPlan(argv, env []string) (Plan, error) {
	if len(argv) < 2 || argv[1] != "herdr" {
		return Plan{}, fmt.Errorf("expected herdr in command position")
	}

	plan := Plan{
		Argv: make([]string, 0, len(argv)-1),
		Env:  make([]string, 0, len(env)+1),
	}
	plan.Argv = append(plan.Argv, argv[0])
	plan.Argv = append(plan.Argv, argv[2:]...)
	hasAgent := false
	for i := 1; i < len(plan.Argv); i++ {
		arg := plan.Argv[i]
		if arg == "--" {
			break
		}
		if arg == "--agent" {
			if i+1 >= len(plan.Argv) || plan.Argv[i+1] != AgentOpenCode {
				return Plan{}, fmt.Errorf("herdr only supports the %s agent", AgentOpenCode)
			}
			hasAgent = true
			i++
		} else if strings.HasPrefix(arg, "--agent=") {
			if strings.TrimPrefix(arg, "--agent=") != AgentOpenCode {
				return Plan{}, fmt.Errorf("herdr only supports the %s agent", AgentOpenCode)
			}
			hasAgent = true
		}
	}
	if !hasAgent {
		insert := len(plan.Argv)
		for i, arg := range plan.Argv[1:] {
			if arg == "--" {
				insert = i + 1
				break
			}
		}
		plan.Argv = append(plan.Argv, "")
		copy(plan.Argv[insert+1:], plan.Argv[insert:])
		plan.Argv[insert] = "--agent=" + AgentOpenCode
	}

	prefix := agentVariable + "="
	count := 0
	matching := false
	for _, value := range env {
		if strings.HasPrefix(value, prefix) {
			count++
			if count == 1 {
				plan.Env = append(plan.Env, prefix+AgentOpenCode)
				matching = value == prefix+AgentOpenCode
			}
			continue
		}
		plan.Env = append(plan.Env, value)
	}
	if count == 0 {
		plan.Env = append(plan.Env, prefix+AgentOpenCode)
	}
	plan.Required = count != 1 || !matching
	return plan, nil
}

// Exec replaces the current process without adding a wrapper process or shell.
func Exec(executable string, plan Plan) error {
	return syscall.Exec(executable, plan.Argv, plan.Env)
}
