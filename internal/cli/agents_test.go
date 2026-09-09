package cli

import "testing"

func TestAgents(t *testing.T) {
	for _, args := range [][]string{{"agents", "--json"}, {"agents", "--help"}, {"help", "agents"}} {
		r, err := Parse(args)
		if err != nil || r.Command != CommandAgents {
			t.Fatalf("%v: %+v %v", args, r, err)
		}
	}
	for _, args := range [][]string{{"agents"}, {"agents", "--json", "."}, {"agents", "--config", "x"}, {"agents", "--json", "--json"}} {
		if _, err := Parse(args); err == nil {
			t.Fatalf("accepted %v", args)
		}
	}
}
