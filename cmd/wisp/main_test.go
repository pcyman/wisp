package main

import (
	"bytes"
	"errors"
	"reflect"
	"strings"
	"testing"

	"wisp/internal/herdr"
)

func TestRunMetaCommands(t *testing.T) {
	tests := []struct {
		name       string
		args       []string
		wantStatus int
		wantOut    string
		wantErr    string
	}{
		{name: "root help", args: []string{"--help"}, wantStatus: 0, wantOut: "Usage:\n"},
		{name: "command help", args: []string{"help", "exec"}, wantStatus: 0, wantOut: "wisp exec [DIRECTORY] -- COMMAND"},
		{name: "Herdr command help", args: []string{"help", "herdr"}, wantStatus: 0, wantOut: "wisp herdr [RUN_OPTIONS] [DIRECTORY]"},
		{name: "Herdr launcher help", args: []string{"herdr", "--help"}, wantStatus: 0, wantOut: "identifying the host-side Wisp"},
		{name: "version", args: []string{"version"}, wantStatus: 0, wantOut: "wisp dev\n"},
		{name: "version flag", args: []string{"--version"}, wantStatus: 0, wantOut: "wisp dev\n"},
		{name: "syntax error", args: []string{"help", "unknown"}, wantStatus: 2, wantErr: "error: unknown help topic"},
		{name: "run dispatch", args: []string{"run", "--config", "/definitely/missing/wisp.toml"}, wantStatus: 1, wantErr: "error: resolve config"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			status := run(tt.args, &stdout, &stderr)
			if status != tt.wantStatus {
				t.Errorf("run() status = %d, want %d", status, tt.wantStatus)
			}
			if !strings.Contains(stdout.String(), tt.wantOut) {
				t.Errorf("run() stdout = %q, want containing %q", stdout.String(), tt.wantOut)
			}
			if !strings.Contains(stderr.String(), tt.wantErr) {
				t.Errorf("run() stderr = %q, want containing %q", stderr.String(), tt.wantErr)
			}
		})
	}
}

func TestRunHerdrWithExistingHintUsesNormalRunPath(t *testing.T) {
	t.Setenv("HERDR_AGENT", "opencode")
	var stdout, stderr bytes.Buffer
	status := run([]string{"herdr", "--config", "/definitely/missing/wisp.toml"}, &stdout, &stderr)
	if status != 1 {
		t.Errorf("run() status = %d, want 1", status)
	}
	if !strings.Contains(stderr.String(), "error: resolve config") {
		t.Errorf("run() stderr = %q, want normal run-path config error", stderr.String())
	}
}

func TestRunHerdrReexecutesBeforeAppConstruction(t *testing.T) {
	var stdout, stderr bytes.Buffer
	var gotExecutable string
	var gotPlan herdr.Plan
	execErr := errors.New("exec stopped by test")
	host := hostProcess{
		argv0:       "wisp-link",
		environment: []string{"HOME=/home/test", "HERDR_AGENT=claude", "TERM=xterm"},
		executable:  func() (string, error) { return "/opt/wisp/bin/wisp", nil },
		exec: func(executable string, plan herdr.Plan) error {
			gotExecutable, gotPlan = executable, plan
			return execErr
		},
	}
	status := runWithHost([]string{"herdr", "repo", "--aws", "dev"}, &stdout, &stderr, host)
	if status != 1 {
		t.Errorf("runWithHost() status = %d, want 1", status)
	}
	if gotExecutable != "/opt/wisp/bin/wisp" {
		t.Errorf("re-exec executable = %q", gotExecutable)
	}
	if want := []string{"wisp-link", "repo", "--aws", "dev", "--agent=opencode"}; !reflect.DeepEqual(gotPlan.Argv, want) {
		t.Errorf("re-exec argv = %q, want %q", gotPlan.Argv, want)
	}
	if want := []string{"HOME=/home/test", "HERDR_AGENT=opencode", "TERM=xterm"}; !reflect.DeepEqual(gotPlan.Env, want) {
		t.Errorf("re-exec env = %q, want %q", gotPlan.Env, want)
	}
	if !strings.Contains(stderr.String(), execErr.Error()) {
		t.Errorf("runWithHost() stderr = %q, want exec error", stderr.String())
	}
	if strings.Contains(stderr.String(), "resolve config") {
		t.Errorf("runWithHost() entered app lifecycle: %q", stderr.String())
	}
}
