package cli

import (
	"strings"
	"testing"
)

func TestParseCentral(t *testing.T) {
	for _, args := range [][]string{{"central"}, {"central", "--config", "/tmp/wisp.toml"}, {"central", "--config=/tmp/wisp.toml"}} {
		req, err := Parse(args)
		if err != nil || req.Command != CommandCentral || req.Run.Directory != "" || len(req.Payload()) != 0 {
			t.Fatalf("Parse(%v) = %#v, %v", args, req, err)
		}
		if len(args) > 1 && req.Config.Path != "/tmp/wisp.toml" {
			t.Fatalf("config = %q", req.Config.Path)
		}
	}
	for _, args := range [][]string{{"central", "--help"}, {"central", "-h"}, {"help", "central"}} {
		req, err := Parse(args)
		if err != nil || !req.ShowHelp || req.Command != CommandCentral {
			t.Fatalf("help %v: %#v %v", args, req, err)
		}
	}
	for _, args := range [][]string{
		{"central", "repo"}, {"central", "--", "pi"}, {"central", "--config"},
		{"central", "--config", "a", "--config", "b"}, {"central", "--agent", "pi"},
	} {
		if _, err := Parse(args); err == nil {
			t.Errorf("accepted %v", args)
		}
	}
	for _, text := range []string{"Inside host tmux", "focuses the visible tool", "no splits", "Outside host tmux", "save editor changes"} {
		if !strings.Contains(Help(CommandCentral), text) {
			t.Errorf("help missing %q", text)
		}
	}
}
