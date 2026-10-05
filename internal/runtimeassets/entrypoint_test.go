package runtimeassets

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestEntrypointAzureLogin(t *testing.T) {
	bash, err := exec.LookPath("bash")
	if err != nil {
		t.Skip("bash unavailable")
	}
	data, err := os.ReadFile("../../container/entrypoint.sh")
	if err != nil {
		t.Fatal(err)
	}
	for _, mode := range []string{"absent", "partial", "complete", "login-failure", "subscription-failure"} {
		t.Run(mode, func(t *testing.T) {
			root := t.TempDir()
			script := filepath.Join(root, "entrypoint.sh")
			// Isolate optional fixed-path mounts from the machine running tests.
			isolated := strings.ReplaceAll(string(data), "/run/wisp/", root+"/mounts/")
			if err := os.WriteFile(script, []byte(isolated), 0600); err != nil {
				t.Fatal(err)
			}
			stub := `#!/usr/bin/env bash
set -eu
[[ "$AZURE_CONFIG_DIR" == "$HOME/.azure" ]]
if [[ "$1" == login ]]; then
    [[ "$#" == 9 && "$2" == --service-principal && "$3" == --username && "$4" == "$WISP_AZURE_CLIENT_ID" && "$5" == "--password=$WISP_AZURE_CLIENT_SECRET" && "$6" == --tenant && "$7" == "$WISP_AZURE_TENANT_ID" && "$8" == --output && "$9" == none ]]
    echo login >> "$TEST_CALLS"
    if [[ "$TEST_MODE" == login-failure ]]; then
        echo "$WISP_AZURE_CLIENT_SECRET" >&2
        exit 1
    fi
else
    [[ "$#" == 4 && "$1" == account && "$2" == set && "$3" == --subscription && "$4" == "$WISP_AZURE_SUBSCRIPTION_ID" ]]
    echo subscription >> "$TEST_CALLS"
    if [[ "$TEST_MODE" == subscription-failure ]]; then
        echo "$WISP_AZURE_CLIENT_SECRET" >&2
        exit 1
    fi
fi
`
			if err := os.WriteFile(filepath.Join(root, "az"), []byte(stub), 0700); err != nil {
				t.Fatal(err)
			}
			calls := filepath.Join(root, "calls")
			cmd := exec.Command(bash, script, bash, "-c", `echo exec >> "$TEST_CALLS"`)
			cmd.Env = []string{"PATH=" + root + ":" + os.Getenv("PATH"), "HOME=" + filepath.Join(root, "home"), "TEST_CALLS=" + calls, "TEST_MODE=" + mode}
			if mode != "absent" {
				cmd.Env = append(cmd.Env, "WISP_AZURE_CLIENT_ID=client")
			}
			if mode != "absent" && mode != "partial" {
				cmd.Env = append(cmd.Env, "WISP_AZURE_TENANT_ID=tenant", "WISP_AZURE_SUBSCRIPTION_ID=subscription", "WISP_AZURE_CLIENT_SECRET=-secret with spaces;$literal")
			}
			output, err := cmd.CombinedOutput()
			want := ""
			switch mode {
			case "absent":
				want = "exec\n"
			case "complete":
				want = "login\nsubscription\nexec\n"
			case "login-failure":
				want = "login\n"
			case "subscription-failure":
				want = "login\nsubscription\n"
			}
			shouldSucceed := mode == "absent" || mode == "complete"
			if (err == nil) != shouldSucceed {
				t.Fatalf("unexpected exit: %v: %s", err, output)
			}
			got, readErr := os.ReadFile(calls)
			if readErr != nil && !os.IsNotExist(readErr) {
				t.Fatal(readErr)
			}
			if string(got) != want {
				t.Fatalf("calls = %q, want %q; output: %s", got, want, output)
			}
			if strings.Contains(string(output), "secret with spaces") {
				t.Fatal("secret leaked in startup output")
			}
		})
	}
}
