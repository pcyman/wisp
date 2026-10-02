package app

import (
	"fmt"
	"io"
	"os"
	"strings"
	"time"
)

// Keep decoration separate from untrusted project names, status and log output.
// Only these fixed SGR styles may reach the terminal as escape sequences.
const (
	centralMuted    = "2;37"
	centralAccent   = "1;36"
	centralGood     = "32"
	centralWarm     = "33"
	centralBad      = "1;91"
	centralSelected = "1;97;44"
)

type centralSpan struct{ text, style string }
type centralLine []centralSpan

func centralPlain(text, style string) centralLine {
	return centralLine{{text: text, style: style}}
}

func centralCells(text string) int {
	cells := 0
	for _, r := range text {
		cells += centralRuneCells(r)
	}
	return cells
}

func centralColumn(text string, width int) string {
	text = centralText(text, max(0, width))
	return text + strings.Repeat(" ", max(0, width-centralCells(text)))
}

func centralSpinner(frame int) string {
	frames := []string{"◐", "◓", "◑", "◒"}
	return frames[frame%len(frames)]
}

func centralState(p centralProject, frame int) (string, string) {
	switch p.state {
	case "running":
		return "● running", centralGood
	case "starting", "stopping":
		return centralSpinner(frame) + " " + p.state, centralWarm
	case "failed":
		return "× failed", centralBad
	default:
		return "○ " + p.state, centralMuted
	}
}

func centralActivityStyle(p centralProject, frame int) (string, string) {
	text := centralActivity(p)
	if p.state != "running" {
		return text, centralMuted
	}
	if p.activityStale {
		return "! " + text, centralWarm
	}
	switch p.activity.State {
	case "working":
		return centralSpinner(frame) + " " + text, centralAccent
	case "idle", "done":
		return "◇ " + text, centralGood
	case "waiting":
		return "! " + text, centralWarm
	case "failed":
		return "× " + text, centralBad
	default:
		return "? " + text, centralMuted
	}
}

func renderCentral(out io.Writer, projects []centralProject, visible []int, selected int, query, message string, search, confirm, showLog bool, width, height int) error {
	// Input already wakes every 100 ms, including while no key is pressed.
	// Use wall time rather than keystrokes so activity has a steady cadence.
	return renderCentralFrame(out, projects, visible, selected, query, message, search, confirm, showLog, width, height, int(time.Now().UnixMilli()/160), os.Getenv("NO_COLOR") == "")
}

func renderCentralFrame(out io.Writer, projects []centralProject, visible []int, selected int, query, message string, search, confirm, showLog bool, width, height, frame int, color bool) error {
	width, height = max(0, width-1), max(0, height)
	selected = min(max(0, selected), max(0, len(visible)-1))
	running, busy := 0, 0
	for _, p := range projects {
		if p.state == "running" {
			running++
			if p.activity.State == "working" && !p.activityStale {
				busy++
			}
		}
	}
	lines := []centralLine{
		{{"  ✦ Wisp Central", centralAccent}, {"  /  your workspace, in orbit", centralMuted}},
		{{fmt.Sprintf("  %d projects", len(projects)), centralMuted}, {fmt.Sprintf("   ● %d running", running), centralGood}, {fmt.Sprintf("   %d busy", busy), centralAccent}},
		centralPlain(strings.Repeat("─", width), centralMuted),
	}
	compact := height < 12
	if compact {
		lines = lines[:1]
	}
	filter := query != "" || search
	if filter {
		cursor := ""
		if search && frame%4 < 2 {
			cursor = "▏"
		}
		lines = append(lines, centralPlain("  / "+query+cursor+fmt.Sprintf("   (%d matches)", len(visible)), centralAccent))
	}

	// Compact terminals retain the project and lifecycle; wider ones also get
	// agent and activity columns. The selected project's detail stays below.
	nameWidth := max(8, min(28, width-20))
	full := width >= 76
	agentColumn := width >= 54
	if full {
		nameWidth = min(28, max(16, width/4))
	} else if agentColumn {
		nameWidth = min(24, width-32)
	}
	header := "  " + centralColumn("PROJECT", nameWidth) + "  " + centralColumn("SANDBOX", 13)
	if agentColumn {
		header += "  " + centralColumn("AGENT", 10)
	}
	if full {
		header += "  ACTIVITY"
	}
	if !compact {
		lines = append(lines, centralPlain(header, centralMuted))
	}

	footer := []centralLine{
		centralPlain(message, centralMuted),
		centralPlain("j/k move  / filter  enter agent  o open  q quit", centralAccent),
		centralPlain("e nvim  g lazygit  x stop  l logs", centralAccent),
		centralPlain("Detach: Ctrl-b, d · Copy: Shift-drag + terminal copy", centralMuted),
	}
	if confirm {
		footer[0] = centralPlain("Stop all and quit? y / any other key cancels", centralBad)
	}
	detailRows := 0
	if len(visible) > 0 && !compact {
		detailRows = 3
		if showLog {
			detailRows += 5
		}
	}
	rows := max(1, height-len(lines)-len(footer)-detailRows-1)
	offset := max(0, selected-rows+1)
	for i := offset; i < len(visible) && i < offset+rows; i++ {
		p := projects[visible[i]]
		state, style := centralState(p, frame)
		marker, nameStyle := " ", ""
		if i == selected {
			marker, nameStyle = ">", centralSelected
		}
		line := centralLine{{marker + " " + centralColumn(p.name, nameWidth) + "  ", nameStyle}, {centralColumn(state, 13), style}}
		if agentColumn {
			line = append(line, centralSpan{"  " + centralColumn(p.plan.AgentName, 10), centralMuted})
		}
		if full {
			activity, activityStyle := centralActivityStyle(p, frame)
			line = append(line, centralSpan{"  " + activity, activityStyle})
		}
		lines = append(lines, line)
	}
	if len(visible) == 0 {
		lines = append(lines, centralPlain("  No matching projects. Esc clears the filter.", centralWarm))
	} else if !compact {
		p := projects[visible[selected]]
		activity, style := centralActivityStyle(p, frame)
		lines = append(lines,
			centralPlain(fmt.Sprintf("  ── %d–%d of %d ──", offset+1, min(offset+rows, len(visible)), len(visible)), centralMuted),
			centralPlain("  ↳ "+p.plan.Project.RootDir, centralMuted),
			centralLine{{"  " + p.plan.AgentName + "  ·  ", centralMuted}, {activity, style}},
		)
		if showLog {
			lines = append(lines, centralPlain("  ── startup log ──", centralAccent))
			var log []string
			if p.log != nil {
				log = strings.Split(strings.TrimSpace(p.log.String()), "\n")
			}
			log = log[max(0, len(log)-4):]
			for _, entry := range log {
				lines = append(lines, centralPlain("  "+entry, centralMuted))
			}
			for i := len(log); i < 4; i++ {
				lines = append(lines, nil)
			}
		}
	}
	// Pin controls to the bottom, prioritizing the confirmation on tiny screens.
	if height < len(footer) {
		lines = footer[:height]
	} else {
		bodyHeight := height - len(footer)
		if len(lines) > bodyHeight {
			lines = lines[:bodyHeight]
		}
		for len(lines) < bodyHeight {
			lines = append(lines, nil)
		}
		lines = append(lines, footer...)
	}
	var screen strings.Builder
	screen.WriteString("\x1b[H")
	for i, line := range lines {
		remaining := width
		for _, span := range line {
			text := centralText(span.text, remaining)
			if color && span.style != "" {
				screen.WriteString("\x1b[" + span.style + "m")
			}
			screen.WriteString(text)
			if color && span.style != "" {
				screen.WriteString("\x1b[0m")
			}
			remaining -= centralCells(text)
		}
		screen.WriteString("\x1b[K")
		if i+1 < len(lines) {
			screen.WriteString("\r\n")
		}
	}
	_, err := io.WriteString(out, screen.String())
	return err
}
