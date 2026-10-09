package config

import (
	"reflect"
	"strings"
	"testing"
)

func TestEnvPassthroughConfig(t *testing.T) {
	for _, input := range []string{
		"schema_version = 1\n",
		"schema_version = 1\nenv_passthrough = []\n",
		"schema_version = 1\nenv_passthrough = [\"GITHUB_TOKEN\", \"MY_API_KEY\", \"_custom1\"]\n",
	} {
		raw, err := Decode([]byte(input))
		if err != nil {
			t.Fatal(err)
		}
		cfg, err := Resolve(raw)
		if err != nil {
			t.Fatal(err)
		}
		if len(cfg.EnvPassthrough) != len(raw.EnvPassthrough) || (len(cfg.EnvPassthrough) > 0 && !reflect.DeepEqual(cfg.EnvPassthrough, raw.EnvPassthrough)) {
			t.Fatal("allowlist was not preserved")
		}
		if len(raw.EnvPassthrough) > 0 {
			raw.EnvPassthrough[0] = "changed"
			if cfg.EnvPassthrough[0] != "GITHUB_TOKEN" {
				t.Fatal("resolved allowlist aliases raw config")
			}
		}
	}
	for _, input := range []string{"env_passthrough = \"TOKEN\"", "env_passthrough = [1]"} {
		if _, err := Decode([]byte("schema_version = 1\n" + input)); err == nil {
			t.Fatalf("accepted wrong type: %s", input)
		}
	}
}

func TestEnvPassthroughRejectsInvalidNames(t *testing.T) {
	names := []string{"", " TOKEN", "A-B", "A=B", "1TOKEN", "TOKEN\n", "WISP_RUN_ID", "WISP_AZURE_CLIENT_SECRET",
		"AWS_ACCESS_KEY_ID", "AWS_CONFIG_FILE", "DOCKER_HOST", "COMPOSE_FILE", "GIT_CONFIG_COUNT",
		"PI_CODING_AGENT_DIR", "HOME", "PATH", "TMPDIR", "XDG_DATA_HOME", "KUBECONFIG",
		"AZURE_CONFIG_DIR", "OPENCODE_CONFIG", "OPENCODE_CONFIG_DIR", "OPENCODE_CONFIG_CONTENT",
		"GIT_DIR", "GIT_WORK_TREE", "GIT_COMMON_DIR", "GIT_INDEX_FILE", "GIT_OBJECT_DIRECTORY",
		"GIT_ALTERNATE_OBJECT_DIRECTORIES", "GIT_CEILING_DIRECTORIES", "GIT_DISCOVERY_ACROSS_FILESYSTEM",
		"PI_TIMING", "TERM", "COLORTERM", "OPENCODE_VERSION",
		"PI_VERSION", "HUNK_VERSION", "KUBECTL_VERSION", "HELM_VERSION", "TERRAFORM_VERSION",
		"YQ_VERSION", "UV_VERSION", "GO_VERSION", "BOTO3_VERSION"}
	for _, name := range names {
		t.Run(name, func(t *testing.T) {
			version := SchemaVersion
			_, err := Resolve(RawConfig{SchemaVersion: &version, EnvPassthrough: []string{name}})
			if err == nil || !strings.Contains(err.Error(), "env_passthrough") {
				t.Fatalf("name %q error = %v", name, err)
			}
		})
	}
	version := SchemaVersion
	if _, err := Resolve(RawConfig{SchemaVersion: &version, EnvPassthrough: []string{"TOKEN", "TOKEN"}}); err == nil {
		t.Fatal("accepted duplicate names")
	}
}
