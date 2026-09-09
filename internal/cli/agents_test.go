package cli

import (
	"strings"
	"testing"
)

func TestAgents(t *testing.T) {
	for _, args := range [][]string{{"agents", "--json"}, {"agents", "--help"}, {"agents", "-h"}, {"help", "agents"}, {"agents", "--json", "--watch"}, {"agents", "--watch", "--json"}} {
		r, err := Parse(args)
		if err != nil || r.Command != CommandAgents {
			t.Fatalf("%v: %+v %v", args, r, err)
		}
		wantWatch := strings.Contains(strings.Join(args, " "), "--watch")
		wantHelp := args[0] == "help" || args[1] == "--help" || args[1] == "-h"
		if r.Agents.Watch != wantWatch || r.ShowHelp != wantHelp {
			t.Fatalf("%v: %+v", args, r)
		}
	}
	for _, args := range [][]string{{"agents"}, {"agents", "--json", "."}, {"agents", "--config", "x"}, {"agents", "--json", "--json"}, {"agents", "--watch"}, {"agents", "--watch", "--watch", "--json"}, {"agents", "--watch", "--json", "--json"}, {"agents", "--json", "--unknown"}, {"agents", "--json", "--watch=true"}, {"agents", "--json", "--watch", "."}} {
		if _, err := Parse(args); err == nil {
			t.Fatalf("accepted %v", args)
		}
	}
	for _, text := range []string{"wisp agents --json [--watch]", "JSONL", "every second", "nonzero", "10-second"} {
		if !strings.Contains(commandHelp[CommandAgents], text) {
			t.Fatalf("help missing %q", text)
		}
	}
}
