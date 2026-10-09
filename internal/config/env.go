package config

import (
	"fmt"
	"regexp"
	"strings"
)

var environmentNamePattern = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

// validateEnvPassthrough protects the broker boundary and sandbox-managed
// paths/settings. Values are intentionally never part of configuration.
func validateEnvPassthrough(names []string) error {
	seen := make(map[string]bool, len(names))
	for _, name := range names {
		if !environmentNamePattern.MatchString(name) {
			return fmt.Errorf("env_passthrough name %q must match %s", name, environmentNamePattern)
		}
		if reservedEnvironmentName(name) {
			return fmt.Errorf("env_passthrough name %q is reserved by Wisp", name)
		}
		if seen[name] {
			return fmt.Errorf("env_passthrough name %q is duplicated", name)
		}
		seen[name] = true
	}
	return nil
}

func reservedEnvironmentName(name string) bool {
	for _, prefix := range []string{"WISP_", "AWS_", "DOCKER_", "COMPOSE_", "GIT_CONFIG_", "PI_CODING_AGENT_"} {
		if strings.HasPrefix(name, prefix) {
			return true
		}
	}
	switch name {
	case "HOME", "PATH", "TMPDIR", "XDG_DATA_HOME", "KUBECONFIG", "AZURE_CONFIG_DIR",
		"OPENCODE_CONFIG", "OPENCODE_CONFIG_DIR", "OPENCODE_CONFIG_CONTENT", "PI_TIMING", "TERM", "COLORTERM",
		"GIT_DIR", "GIT_WORK_TREE", "GIT_COMMON_DIR", "GIT_INDEX_FILE", "GIT_OBJECT_DIRECTORY",
		"GIT_ALTERNATE_OBJECT_DIRECTORIES", "GIT_CEILING_DIRECTORIES", "GIT_DISCOVERY_ACROSS_FILESYSTEM",
		"OPENCODE_VERSION", "PI_VERSION", "HUNK_VERSION", "KUBECTL_VERSION",
		"HELM_VERSION", "TERRAFORM_VERSION", "YQ_VERSION", "UV_VERSION", "GO_VERSION", "BOTO3_VERSION":
		return true
	}
	return false
}
