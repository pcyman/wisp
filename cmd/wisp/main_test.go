package main

import (
	"bytes"
	"strings"
	"testing"
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
