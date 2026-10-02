package wisp

import (
	"bytes"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestCentralRuntimePackaging(t *testing.T) {
	for path, required := range map[string][]string{
		"container/central-session.sh": {"export LANG=C.UTF-8", "new-session -d -s wisp", `-- "$@"`, "pane_dead_status", "trap stop_session TERM INT HUP"},
		"container/central.tmux.conf":  {"set -s extended-keys on", "set -g remain-on-exit on", "set -g default-terminal tmux-256color"},
		"Dockerfile":                   {"        tmux", "        ncurses-term", "COPY --chmod=0755 container/central-session.sh /usr/local/bin/wisp-central-session", "COPY --chmod=0644 container/central.tmux.conf /usr/local/share/wisp/central.tmux.conf"},
		".dockerignore":                {"!container/central-session.sh", "!container/central.tmux.conf"},
	} {
		data, err := RuntimeAssets.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		for _, text := range required {
			if !strings.Contains(string(data), text) {
				t.Errorf("%s missing %q", path, text)
			}
		}
	}
}

// Exercise the shell supervisor without Docker or a tmux dependency. The fake
// preserves argv, retains a dead pane, and reports the agent's exit status.
func TestCentralSessionPreservesArgumentsAndAgentExit(t *testing.T) {
	bash, err := exec.LookPath("bash")
	if err != nil {
		t.Skip("bash unavailable")
	}
	root := t.TempDir()
	fake := `#!/usr/bin/env bash
set -eu
if [[ "${1:-}" == -u ]]; then
    shift
    [[ "$1" == -f ]]
    shift 2
fi
case "$1" in
    new-session)
        printf '%s\0' "$@" > "$CENTRAL_TEST_LOG"
        ;;
    has-session) exit 0 ;;
    display-message)
        if [[ "${*: -1}" == '#{pane_dead}' ]]; then
            printf '1\n'
        else
            printf '7\n'
        fi
        ;;
    kill-server) exit 0 ;;
    *) exit 99 ;;
esac
`
	if err := os.WriteFile(filepath.Join(root, "tmux"), []byte(fake), 0700); err != nil {
		t.Fatal(err)
	}
	script, err := RuntimeAssets.ReadFile("container/central-session.sh")
	if err != nil {
		t.Fatal(err)
	}
	scriptPath := filepath.Join(root, "session.sh")
	if err := os.WriteFile(scriptPath, script, 0700); err != nil {
		t.Fatal(err)
	}
	log := filepath.Join(root, "args")
	args := []string{"agent", "argument with spaces", "$(touch SHOULD_NOT_EXIST)", "a;b"}
	command := exec.Command(bash, append([]string{scriptPath}, args...)...)
	command.Env = append(os.Environ(), "PATH="+root+":"+os.Getenv("PATH"), "CENTRAL_TEST_LOG="+log)
	command.Dir = root
	output, err := command.CombinedOutput()
	var exit *exec.ExitError
	if !errors.As(err, &exit) || exit.ExitCode() != 7 {
		t.Fatalf("exit=%v output=%s", err, output)
	}
	got, err := os.ReadFile(log)
	if err != nil {
		t.Fatal(err)
	}
	parts := bytes.Split(bytes.TrimSuffix(got, []byte{0}), []byte{0})
	separator := -1
	for i, part := range parts {
		if string(part) == "--" {
			separator = i
			break
		}
	}
	if separator < 0 || len(parts[separator+1:]) != len(args) {
		t.Fatalf("argv=%q", parts)
	}
	for i, arg := range args {
		if string(parts[separator+1+i]) != arg {
			t.Fatalf("arg %d=%q want %q", i, parts[separator+1+i], arg)
		}
	}
	if _, err := os.Stat(filepath.Join(root, "SHOULD_NOT_EXIST")); !os.IsNotExist(err) {
		t.Fatal("agent arguments were evaluated as a shell command")
	}
}
