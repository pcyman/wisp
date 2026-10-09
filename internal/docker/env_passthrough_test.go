package docker

import (
	"encoding/json"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestRunOverridePassthrough(t *testing.T) {
	mount, _ := Bind("/config", "/run/wisp/config.toml", true)
	values := map[string]string{"TOKEN": "${HOST_SECRET} $$ $plain\nquotes: \"x\" = yes", "EMPTY": ""}
	for _, credentials := range [][]Mount{nil, {mount}} {
		override, err := NewRunOverride([]string{"pi"}, credentials, nil, values)
		if err != nil {
			t.Fatal(err)
		}
		sandbox := override.Services["sandbox"].Environment
		if sandbox["TOKEN"] != strings.ReplaceAll(values["TOKEN"], "$", "$$") {
			t.Fatal("dollar signs were not escaped for Compose")
		}
		if value, exists := sandbox["EMPTY"]; !exists || value != "" {
			t.Fatal("empty value was not preserved")
		}
		if len(override.Services["credentials"].Environment) != 0 {
			t.Fatal("passthrough included in broker environment")
		}
		if len(credentials) > 0 && sandbox["AWS_CONTAINER_CREDENTIALS_FULL_URI"] == "" {
			t.Fatal("AWS plumbing was replaced")
		}
		sandbox["TOKEN"] = "changed"
		if values["TOKEN"] == "changed" {
			t.Fatal("override aliases input map")
		}
	}
	if _, err := NewRunOverride([]string{"pi"}, []Mount{mount}, nil, map[string]string{"AWS_CONTAINER_AUTHORIZATION_TOKEN": "override"}); err == nil {
		t.Fatal("allowed overwrite of AWS token")
	}
}

// Compose config needs the CLI/plugin but not a running Docker daemon. Verify
// actual interpolation when available, especially for dollars and multiline values.
func TestComposePassthroughLiteralValues(t *testing.T) {
	docker, err := exec.LookPath("docker")
	if err != nil {
		t.Skip("Docker CLI not installed")
	}
	if err := exec.Command(docker, "compose", "version").Run(); err != nil {
		t.Skip("Docker Compose plugin not available")
	}
	values := map[string]string{
		"TOKEN": "${HOST_SECRET} $$ $plain\nquotes: \"x\" = yes",
		"EMPTY": "", "UNICODE": "héllo 世界", "DOLLARS": "$$$${X:-default} $X $$X",
	}
	override, err := NewRunOverride([]string{"pi"}, nil, nil, values)
	if err != nil {
		t.Fatal(err)
	}
	files, err := WriteInvocationFiles(t.TempDir(), override, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer files.Cleanup()
	base, err := filepath.Abs("../../compose.yaml")
	if err != nil {
		t.Fatal(err)
	}
	command := exec.Command(docker, "compose", "--project-name", "wisp-env-test", "--file", base, "--file", files.OverridePath, "config", "--format", "json")
	command.Env = []string{"HOST_SECRET=must-not-interpolate", "X=must-not-interpolate"}
	data, err := command.Output()
	if err != nil {
		t.Fatalf("Compose config failed: %v", err)
	}
	var config struct {
		Services map[string]struct {
			Environment map[string]string `json:"environment"`
		} `json:"services"`
	}
	if err := json.Unmarshal(data, &config); err != nil {
		t.Fatal(err)
	}
	got := make(map[string]string)
	for key := range values {
		got[key] = config.Services["sandbox"].Environment[key]
		if _, exists := config.Services["credentials"].Environment[key]; exists {
			t.Fatal("passthrough reached broker")
		}
	}
	if !reflect.DeepEqual(got, values) {
		t.Fatalf("Compose literal values = %#v, want %#v", got, values)
	}
}
