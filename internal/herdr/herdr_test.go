package herdr

import (
	"reflect"
	"strings"
	"testing"
)

func TestBuildPlan(t *testing.T) {
	tests := []struct {
		name     string
		env      []string
		required bool
	}{
		{name: "unset", env: []string{"HOME=/home/test", "TERM=xterm"}, required: true},
		{name: "matching", env: []string{"HERDR_AGENT=opencode", "HOME=/home/test"}, required: false},
		{name: "conflicting", env: []string{"HOME=/home/test", "HERDR_AGENT=claude"}, required: true},
		{name: "duplicate matching", env: []string{"HERDR_AGENT=opencode", "HOME=/home/test", "HERDR_AGENT=opencode"}, required: true},
		{name: "duplicate conflicting", env: []string{"HERDR_AGENT=claude", "HOME=/home/test", "HERDR_AGENT=codex"}, required: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			argv := []string{"wisp-link", "herdr", "repo", "--aws", "dev"}
			plan, err := BuildPlan(argv, tt.env)
			if err != nil {
				t.Fatalf("BuildPlan() error = %v", err)
			}
			if plan.Required != tt.required {
				t.Errorf("BuildPlan() Required = %v, want %v", plan.Required, tt.required)
			}
			wantArgv := []string{"wisp-link", "repo", "--aws", "dev", "--agent=opencode"}
			if !reflect.DeepEqual(plan.Argv, wantArgv) {
				t.Errorf("BuildPlan() Argv = %q, want %q", plan.Argv, wantArgv)
			}

			agentEntries := 0
			for _, value := range plan.Env {
				if strings.HasPrefix(value, "HERDR_AGENT=") {
					agentEntries++
					if value != "HERDR_AGENT=opencode" {
						t.Errorf("BuildPlan() agent entry = %q", value)
					}
				}
			}
			if agentEntries != 1 {
				t.Errorf("BuildPlan() agent entry count = %d, want 1", agentEntries)
			}
			for _, value := range tt.env {
				if !strings.HasPrefix(value, "HERDR_AGENT=") && !contains(plan.Env, value) {
					t.Errorf("BuildPlan() dropped unrelated environment entry %q", value)
				}
			}
		})
	}
}

func TestBuildPlanOnlyRemovesCommandPosition(t *testing.T) {
	plan, err := BuildPlan([]string{"wisp", "herdr", "herdr", "--", "herdr"}, nil)
	if err != nil {
		t.Fatalf("BuildPlan() error = %v", err)
	}
	want := []string{"wisp", "herdr", "--agent=opencode", "--", "herdr"}
	if !reflect.DeepEqual(plan.Argv, want) {
		t.Fatalf("BuildPlan() Argv = %q, want %q", plan.Argv, want)
	}
}

func TestBuildPlanRejectsPi(t *testing.T) {
	for _, argv := range [][]string{
		{"wisp", "herdr", "--agent", "pi"},
		{"wisp", "herdr", "--agent=pi"},
	} {
		if _, err := BuildPlan(argv, nil); err == nil || !strings.Contains(err.Error(), "only supports") {
			t.Fatalf("BuildPlan(%q) error = %v", argv, err)
		}
	}
}

func TestBuildPlanRejectsUnexpectedArgv(t *testing.T) {
	for _, argv := range [][]string{nil, {"wisp"}, {"wisp", "run", "herdr"}} {
		if _, err := BuildPlan(argv, nil); err == nil {
			t.Fatalf("BuildPlan(%q) succeeded, want error", argv)
		}
	}
}

func contains(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}
