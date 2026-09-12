package agent

import (
	"reflect"
	"testing"

	"wisp/internal/config"
	"wisp/internal/docker"
)

func TestOpenCode(t *testing.T) {
	a := Default()
	if a.Name() != "OpenCode" {
		t.Fatalf("Name() = %q", a.Name())
	}
	if got := a.ContainerCommand(); !reflect.DeepEqual(got, []string{"opencode"}) {
		t.Fatalf("ContainerCommand() = %#v", got)
	}

	cfg := config.Config{
		OpenCode: config.OpenCodeConfig{
			ConfigPath: "/host/opencode",
			AuthPath:   "/host/auth.json",
		},
		Hunk: config.HunkConfig{ConfigPath: "/host/hunk/config.toml"},
	}
	want := []docker.Mount{
		{Type: "bind", Source: "/host/opencode", Target: "/run/wisp/opencode/config", ReadOnly: true, Bind: docker.BindMount{}},
		{Type: "bind", Source: "/host/auth.json", Target: "/run/wisp/agent/data/opencode/auth.json", ReadOnly: true, Bind: docker.BindMount{}},
		{Type: "bind", Source: "/host/hunk/config.toml", Target: "/run/wisp/hunk/config.toml", ReadOnly: true, Bind: docker.BindMount{}},
	}
	if got, err := a.HostMounts(cfg); err != nil || !reflect.DeepEqual(got, want) {
		t.Fatalf("HostMounts() = %#v, want %#v", got, want)
	}
}

func TestOpenCodeOmitsUnavailableDefaultMounts(t *testing.T) {
	got, err := (OpenCode{}).HostMounts(config.Config{})
	if err != nil || len(got) != 0 {
		t.Fatalf("HostMounts() = %#v, want no mounts", got)
	}
}

func TestSelectPi(t *testing.T) {
	a, err := Select("pi", "opencode")
	if err != nil {
		t.Fatal(err)
	}
	if a.Key() != "pi" || a.Name() != "Pi" || !reflect.DeepEqual(a.ContainerCommand(), []string{"pi", "-e", "/usr/local/share/wisp/pi-agent-status.mjs"}) {
		t.Fatalf("selected agent = %q %q %#v", a.Key(), a.Name(), a.ContainerCommand())
	}
	cfg := config.Config{Pi: config.PiConfig{ConfigPath: "/host/.pi/agent"}}
	mounts, err := a.HostMounts(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if len(mounts) != 1 || mounts[0].Target != PiAgentTarget || mounts[0].ReadOnly {
		t.Fatalf("Pi mounts = %#v", mounts)
	}
}

func TestSelectUsesConfiguredDefaultAndRejectsUnknown(t *testing.T) {
	if a, err := Select("", "pi"); err != nil || a.Key() != "pi" {
		t.Fatalf("Select configured Pi = %#v, %v", a, err)
	}
	if _, err := Select("other", "opencode"); err == nil {
		t.Fatal("Select accepted unknown agent")
	}
}
