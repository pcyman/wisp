package app

import (
	"bytes"
	"fmt"
	"regexp"
	"strings"
	"testing"

	"wisp/internal/agentstatus"
	"wisp/internal/project"
)

var centralTestSGR = regexp.MustCompile(`\x1b\[[0-9;]*m`)

func TestCentralStyledDashboard(t *testing.T) {
	projects := []centralProject{
		{name: "Alpha", state: "running", plan: SandboxPlan{Project: project.Project{RootDir: "/work/alpha"}, AgentName: "Pi"}, activity: agentstatus.Report{State: "working"}},
		{name: "Beta", state: "starting", plan: SandboxPlan{AgentName: "OpenCode"}},
		{name: "Gamma", state: "failed"},
	}
	var out bytes.Buffer
	if err := renderCentralFrame(&out, projects, []int{0, 1, 2}, 0, "", "Ready", false, false, false, 100, 24, 0, true); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"✦ Wisp Central", "3 projects", "1 running", "1 busy", "\x1b[1;97;44m> Alpha", "● running", "◐ starting", "× failed", "◐ working", "/work/alpha", "q quit"} {
		if !strings.Contains(out.String(), want) {
			t.Fatalf("missing %q in %q", want, out.String())
		}
	}
}

func TestCentralAnimationAndStaleActivity(t *testing.T) {
	p := centralProject{state: "starting"}
	a, _ := centralState(p, 0)
	b, _ := centralState(p, 1)
	if a == b {
		t.Fatal("startup spinner does not animate")
	}
	p.state, p.activity.State = "running", "working"
	a, _ = centralActivityStyle(p, 0)
	b, _ = centralActivityStyle(p, 1)
	if a == b {
		t.Fatal("busy activity does not animate")
	}
	p.activityStale = true
	a, style := centralActivityStyle(p, 0)
	b, _ = centralActivityStyle(p, 1)
	if a != b || style != centralWarm || !strings.Contains(a, "stale") {
		t.Fatalf("stale report must not look live: %q / %q / %q", a, b, style)
	}
}

func TestCentralFrameBoundsAndUntrustedText(t *testing.T) {
	log := &centralLog{}
	_, _ = log.Write([]byte("log\x1b[2J\x1b]0;bad\a\n界界界\nlast line"))
	projects := []centralProject{{
		name: "界project\x1b[31m", state: "running", log: log,
		plan:     SandboxPlan{AgentName: "Pi\x1b[31m", Project: project.Project{RootDir: "/work\x1b[31m"}},
		activity: agentstatus.Report{State: "working", Reason: "tool\x1b[31m"},
	}}
	for _, width := range []int{1, 2, 20, 50, 80, 120} {
		for _, height := range []int{1, 3, 6, 24} {
			t.Run(fmt.Sprintf("%dx%d", width, height), func(t *testing.T) {
				var out bytes.Buffer
				if err := renderCentralFrame(&out, projects, []int{0}, 0, "界\x1b[31m", "msg\x1b[31m", true, true, true, width, height, 0, true); err != nil {
					t.Fatal(err)
				}
				raw := out.String()
				if strings.Contains(raw, "\x1b[31m") || strings.Contains(raw, "\x1b[2J") || strings.Contains(raw, "\x1b]") || strings.Contains(raw, "\a") {
					t.Fatalf("untrusted terminal controls: %q", raw)
				}
				plain := centralTestSGR.ReplaceAllString(raw, "")
				plain = strings.TrimPrefix(plain, "\x1b[H")
				plain = strings.ReplaceAll(plain, "\x1b[K", "")
				lines := strings.Split(plain, "\r\n")
				if len(lines) != height {
					t.Fatalf("got %d lines, want %d", len(lines), height)
				}
				for _, line := range lines {
					if centralCells(line) > width-1 || strings.ContainsRune(line, '\x1b') {
						t.Fatalf("unsafe or overflowing line: %q", line)
					}
				}
				if width >= 50 && !strings.Contains(plain, "Stop all and quit?") {
					t.Fatal("confirmation hidden")
				}
			})
		}
	}
}

func TestCentralNoColor(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	var out bytes.Buffer
	if err := renderCentral(&out, nil, nil, 0, "", "", false, false, false, 80, 24); err != nil {
		t.Fatal(err)
	}
	if centralTestSGR.MatchString(out.String()) || !strings.Contains(out.String(), "No matching projects") {
		t.Fatalf("monochrome empty state: %q", out.String())
	}
}
