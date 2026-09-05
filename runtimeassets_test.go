package wisp

import (
	"strings"
	"testing"
)

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
