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

	cfg := config.Config{OpenCode: config.OpenCodeConfig{
		ConfigPath: "/host/opencode",
		AuthPath:   "/host/auth.json",
	}}
	want := []docker.Mount{
		{Type: "bind", Source: "/host/opencode", Target: "/run/wisp/opencode/config", ReadOnly: true, Bind: docker.BindMount{}},
		{Type: "bind", Source: "/host/auth.json", Target: "/run/wisp/opencode/data/opencode/auth.json", ReadOnly: true, Bind: docker.BindMount{}},
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
