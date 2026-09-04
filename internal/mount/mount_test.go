package mount

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestPlanResolvesConfigAndCLIMounts(t *testing.T) {
	base := t.TempDir()
	configDir := filepath.Join(base, "config")
	invocationDir := filepath.Join(base, "invocation")
	home := filepath.Join(base, "home")
	shared := filepath.Join(base, "shared: library")
	homeRepo := filepath.Join(home, "src", "editable")
	cliRO := filepath.Join(invocationDir, "read only")
	cliRWReal := filepath.Join(base, "write-real")
	cliRWLink := filepath.Join(invocationDir, "write-link")
	for _, dir := range []string{configDir, invocationDir, homeRepo, shared, cliRO, cliRWReal} {
		mustMkdirAll(t, dir)
	}
	if err := os.Symlink(cliRWReal, cliRWLink); err != nil {
		t.Fatal(err)
	}

	got, err := Plan(
		[]Spec{
			{Source: "../shared: library", Target: "/workspace/repos/shared", Mode: ReadOnly},
			{Source: "~/src/editable", Target: "/workspace/repos/editable", Mode: ReadWrite},
		},
		[]string{"read only"},
		[]string{"write-link"},
		configDir,
		invocationDir,
		home,
	)
	if err != nil {
		t.Fatal(err)
	}
	want := []Mount{
		{Source: shared, Target: "/workspace/repos/shared", Mode: ReadOnly},
		{Source: homeRepo, Target: "/workspace/repos/editable", Mode: ReadWrite},
		{Source: cliRO, Target: "/workspace/repos/read only", Mode: ReadOnly},
		{Source: cliRWReal, Target: "/workspace/repos/write-real", Mode: ReadWrite},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Plan() = %#v, want %#v", got, want)
	}
}

func TestPlanDefaultsConfigModeAndAllowsRepeatedSource(t *testing.T) {
	base := t.TempDir()
	source := filepath.Join(base, "source")
	mustMkdirAll(t, source)
	got, err := Plan([]Spec{
		{Source: "source", Target: "/workspace/repos/one"},
		{Source: "source", Target: "/workspace/repos/two", Mode: ReadWrite},
	}, nil, nil, base, base, base)
	if err != nil {
		t.Fatal(err)
	}
	if got[0].Mode != ReadOnly || got[1].Mode != ReadWrite || got[0].Source != got[1].Source {
		t.Fatalf("unexpected mounts: %#v", got)
	}
}

func TestValidateTarget(t *testing.T) {
	tests := []struct {
		target string
		ok     bool
	}{
		{target: "/workspace/repos/repo", ok: true},
		{target: "/workspace/repos/name with spaces", ok: true},
		{target: "workspace/repos/repo"},
		{target: "/workspace/repos"},
		{target: "/workspace/repos/../current"},
		{target: "/workspace/repos/./repo"},
		{target: "/workspace/repos/repo/"},
		{target: "/workspace/repos//repo"},
		{target: "/workspace/repos-other/repo"},
		{target: "/workspace/current"},
		{target: "/workspace"},
		{target: "/home/sandbox/repo"},
		{target: "/run/wisp/repo"},
		{target: "/workspace/repos/x\x00y"},
	}
	for _, tt := range tests {
		t.Run(strings.ReplaceAll(tt.target, "/", "_"), func(t *testing.T) {
			err := ValidateTarget(tt.target)
			if (err == nil) != tt.ok {
				t.Fatalf("ValidateTarget(%q) error = %v, ok=%v", tt.target, err, tt.ok)
			}
		})
	}
}

func TestConfigMountRejectsNULTarget(t *testing.T) {
	base := t.TempDir()
	mustMkdirAll(t, filepath.Join(base, "source"))
	_, err := Plan([]Spec{{Source: "source", Target: "/workspace/repos/bad\x00target"}}, nil, nil, base, base, base)
	if err == nil || !strings.Contains(err.Error(), "NUL") {
		t.Fatalf("error = %v, want NUL target rejection", err)
	}
}

func TestPlanRejectsDuplicateAndNestedTargetsAcrossOrigins(t *testing.T) {
	base := t.TempDir()
	for _, dir := range []string{"a", "b", "same"} {
		mustMkdirAll(t, filepath.Join(base, dir))
	}
	tests := []struct {
		name   string
		config []Spec
		roCLI  []string
		want   string
	}{
		{
			name: "duplicate config targets",
			config: []Spec{
				{Source: "a", Target: "/workspace/repos/x"},
				{Source: "b", Target: "/workspace/repos/x"},
			},
			want: "used more than once",
		},
		{
			name: "nested config targets",
			config: []Spec{
				{Source: "a", Target: "/workspace/repos/x"},
				{Source: "b", Target: "/workspace/repos/x/child"},
			},
			want: "overlap",
		},
		{
			name: "config and CLI collision",
			config: []Spec{
				{Source: "a", Target: "/workspace/repos/same"},
			},
			roCLI: []string{"same"},
			want:  "used more than once",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := Plan(tt.config, tt.roCLI, nil, base, base, base)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("error = %v, want substring %q", err, tt.want)
			}
		})
	}
}

func TestPlanRejectsInvalidSourcesModesAndTargets(t *testing.T) {
	base := t.TempDir()
	mustMkdirAll(t, filepath.Join(base, "dir"))
	file := filepath.Join(base, "file")
	if err := os.WriteFile(file, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name      string
		config    []Spec
		roCLI     []string
		configDir string
		home      string
		want      string
	}{
		{name: "empty source", config: []Spec{{Target: "/workspace/repos/x"}}, configDir: base, home: base, want: "source is empty"},
		{name: "missing source", config: []Spec{{Source: "missing", Target: "/workspace/repos/x"}}, configDir: base, home: base, want: "no such file"},
		{name: "file source", config: []Spec{{Source: "file", Target: "/workspace/repos/x"}}, configDir: base, home: base, want: "not a directory"},
		{name: "bad mode", config: []Spec{{Source: "dir", Target: "/workspace/repos/x", Mode: "read-only"}}, configDir: base, home: base, want: "must be"},
		{name: "bad target", config: []Spec{{Source: "dir", Target: "/workspace/current"}}, configDir: base, home: base, want: "must be a descendant"},
		{name: "relative config base", config: []Spec{{Source: "dir", Target: "/workspace/repos/x"}}, configDir: "relative", home: base, want: "not absolute"},
		{name: "relative HOME", config: []Spec{{Source: "~/dir", Target: "/workspace/repos/x"}}, configDir: base, home: "relative", want: "HOME is not absolute"},
		{name: "CLI does not expand tilde", roCLI: []string{"~/dir"}, configDir: base, home: base, want: "no such file"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			configDir := tt.configDir
			if configDir == "" {
				configDir = base
			}
			_, err := Plan(tt.config, tt.roCLI, nil, configDir, base, tt.home)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("error = %v, want substring %q", err, tt.want)
			}
		})
	}
}

func TestCLIRootSourceCannotDeriveRepositoriesRoot(t *testing.T) {
	_, err := Plan(nil, []string{"/"}, nil, t.TempDir(), t.TempDir(), "")
	if err == nil || !strings.Contains(err.Error(), "descendant") {
		t.Fatalf("error = %v, want descendant error", err)
	}
}

func mustMkdirAll(t *testing.T, path string) {
	t.Helper()
	if err := os.MkdirAll(path, 0o700); err != nil {
		t.Fatal(err)
	}
}
