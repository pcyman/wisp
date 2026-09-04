package config

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestResolvePath(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	tests := []struct {
		name     string
		override string
		cwd      string
		env      Environment
		want     string
		wantErr  string
	}{
		{name: "xdg", cwd: root, env: Environment{XDGConfigHome: filepath.Join(root, "xdg")}, want: filepath.Join(root, "xdg", "wisp", "config.toml")},
		{name: "home fallback", cwd: root, env: Environment{Home: root}, want: filepath.Join(root, ".config", "wisp", "config.toml")},
		{name: "relative override", override: "configs/wisp.toml", cwd: root, want: filepath.Join(root, "configs", "wisp.toml")},
		{name: "absolute override ignores invalid environment", override: filepath.Join(root, "wisp.toml"), cwd: root, env: Environment{XDGConfigHome: "relative"}, want: filepath.Join(root, "wisp.toml")},
		{name: "relative xdg", cwd: root, env: Environment{XDGConfigHome: "relative", Home: root}, wantErr: "XDG_CONFIG_HOME must be an absolute path"},
		{name: "relative home", cwd: root, env: Environment{Home: "relative"}, wantErr: "HOME must be an absolute path"},
		{name: "no base", cwd: root, wantErr: "neither XDG_CONFIG_HOME nor HOME is set"},
		{name: "relative cwd", override: "config.toml", cwd: "relative", wantErr: "invocation directory"},
	}
	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			got, err := ResolvePath(test.override, test.cwd, test.env)
			if test.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), test.wantErr) {
					t.Fatalf("ResolvePath() error = %v, want containing %q", err, test.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("ResolvePath() error = %v", err)
			}
			if got != test.want {
				t.Fatalf("ResolvePath() = %q, want %q", got, test.want)
			}
		})
	}
}
