package wisp

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestStatusReporterPackaging(t *testing.T) {
	for path, required := range map[string]string{
		"container/agent-status.js": "export const AgentStatusPlugin",
		".dockerignore":             "!container/agent-status.js",
		"Dockerfile":                "COPY --chmod=0644 container/agent-status.js /usr/local/share/wisp/agent-status.js",
		"container/entrypoint.sh":   "export OPENCODE_CONFIG=/usr/local/share/wisp/opencode.json",
		"compose.yaml":              "wisp.run-id: \"${WISP_RUN_ID:-}\"",
	} {
		data, err := RuntimeAssets.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(data), required) {
			t.Errorf("%s missing reporter integration %q", path, required)
		}
	}
}

func TestReporterUsesDirectPluginConfig(t *testing.T) {
	data, err := RuntimeAssets.ReadFile("container/opencode.json")
	if err != nil {
		t.Fatal(err)
	}
	var config struct {
		Schema string   `json:"$schema"`
		Plugin []string `json:"plugin"`
	}
	if err := json.Unmarshal(data, &config); err != nil {
		t.Fatal(err)
	}
	if config.Schema != "https://opencode.ai/config.json" || len(config.Plugin) != 1 || config.Plugin[0] != "file:///usr/local/share/wisp/agent-status.js" {
		t.Fatalf("unexpected reporter config: %+v", config)
	}
	for path, required := range map[string]string{
		".dockerignore": "!container/opencode.json",
		"Dockerfile":    "COPY --chmod=0644 container/opencode.json /usr/local/share/wisp/opencode.json",
	} {
		data, err := RuntimeAssets.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(data), required) {
			t.Errorf("%s missing %q", path, required)
		}
	}
	entrypoint, err := RuntimeAssets.ReadFile("container/entrypoint.sh")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(entrypoint), "OPENCODE_CONFIG_DIR") || strings.Contains(string(entrypoint), "cp -- /usr/local/share/wisp/agent-status.js") {
		t.Fatal("reporter must not create a fresh plugin-discovery directory")
	}
}

func TestSandboxComposeHardening(t *testing.T) {
	data, err := RuntimeAssets.ReadFile("compose.yaml")
	if err != nil {
		t.Fatal(err)
	}
	compose := string(data)
	marker := "\n  sandbox:\n"
	index := strings.Index(compose, marker)
	if index < 0 {
		t.Fatal("compose.yaml has no sandbox service")
	}
	sandbox := compose[index+len(marker):]
	for _, required := range []string{
		"      TMPDIR: /run/wisp/tmp\n",
		"      XDG_DATA_HOME: /run/wisp/opencode/data\n",
		"    read_only: true\n",
		"    cap_drop:\n      - ALL\n",
		"    security_opt:\n      - no-new-privileges:true\n",
		"      - /home/sandbox:",
		"      - /tmp:mode=1777\n",
		"      - /var/tmp:mode=1777\n",
		"      - /run/wisp/tmp:exec,uid=${WISP_UID:-1000},gid=${WISP_GID:-1000},mode=0700\n",
	} {
		if !strings.Contains(sandbox, required) {
			t.Errorf("sandbox service is missing %q", strings.TrimSpace(required))
		}
	}
}

func TestReporterReadableBySandboxUser(t *testing.T) {
	data, err := RuntimeAssets.ReadFile("Dockerfile")
	if err != nil {
		t.Fatal(err)
	}
	dockerfile := string(data)
	for _, required := range []string{
		"RUN install -d -m 0755 /usr/local/share/wisp\n",
		"RUN chmod 0755 /usr/local/share/wisp\n",
		"USER sandbox\nRUN test -r /usr/local/share/wisp/agent-status.js\n",
	} {
		if !strings.Contains(dockerfile, required) {
			t.Errorf("Dockerfile missing reporter permission safeguard %q", required)
		}
	}
}
