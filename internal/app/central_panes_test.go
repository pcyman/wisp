package app

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"wisp/internal/process"
	"wisp/internal/project"
)

func TestCentralPanesOutsideTmux(t *testing.T) {
	runner := &recordingRunner{capture: func(process.Command) ([]byte, []byte, error) {
		t.Fatal("unexpected host command")
		return nil, nil, nil
	}}
	a, err := New(Dependencies{Runner: runner, Environment: []string{}, InvocationDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	panes, err := a.newCentralPanes(context.Background())
	if err != nil || panes != nil {
		t.Fatalf("panes=%v err=%v", panes, err)
	}
}

// A small tmux control-plane fake also exercises Central's real event loop.
type centralTmuxFake struct {
	panes map[string]centralPane
	next  int
	calls []process.Command
}

func (f *centralTmuxFake) capture(c process.Command) ([]byte, []byte, error) {
	f.calls = append(f.calls, c)
	if len(c.Args) < 3 || c.Args[0] != "-S" || c.Args[1] != "/tmp/host,tmux" {
		return nil, nil, fmt.Errorf("unexpected tmux command %v", c.Args)
	}
	args := c.Args[2:]
	switch args[0] {
	case "list-panes":
		var out strings.Builder
		for _, pane := range f.panes {
			dead := 0
			if pane.dead {
				dead = 1
			}
			fmt.Fprintf(&out, "%s|%s|%s|%d|100|%s\n", pane.id, pane.window, pane.owner, dead, pane.start)
		}
		return []byte(out.String()), nil, nil
	case "split-window":
		f.next++
		id := fmt.Sprintf("%%%d", f.next)
		start := strings.Join(args[indexSlice(args, []string{"--"})+1:], " ")
		f.panes[id] = centralPane{id: id, window: "@0", start: start}
		return []byte(id + "\n"), nil, nil
	case "set-option":
		id := args[3]
		pane := f.panes[id]
		if args[4] == centralPaneOwnerOption {
			pane.owner = args[5]
		}
		f.panes[id] = pane
	case "kill-pane":
		delete(f.panes, args[2])
	case "move-pane":
		i := indexSlice(args, []string{"-s"})
		pane := f.panes[args[i+1]]
		pane.window = "@0"
		f.panes[pane.id] = pane
	case "respawn-pane":
		id := args[indexSlice(args, []string{"-t"})+1]
		pane := f.panes[id]
		pane.start = strings.Join(args[indexSlice(args, []string{"--"})+1:], " ")
		f.panes[id] = pane
	case "select-pane":
	default:
		return nil, nil, fmt.Errorf("unexpected tmux command %v", args)
	}
	return nil, nil, nil
}

func newCentralTmuxFake() *centralTmuxFake {
	return &centralTmuxFake{panes: map[string]centralPane{"%0": {id: "%0", window: "@0"}}}
}

func TestCentralHostPaneToolsKeepDashboardLive(t *testing.T) {
	root, path, dir := applicationFixture(t)
	if err := os.WriteFile(path, []byte(fmt.Sprintf("schema_version=1\n[[central.projects]]\nname='test'\npath=%q\n", dir)), 0600); err != nil {
		t.Fatal(err)
	}
	base := (&lifecycleFake{}).runner(t)
	fake := newCentralTmuxFake()
	ui := &scriptedCentralUI{}
	runner := contextCentralRunner{
		capture: func(ctx context.Context, c process.Command) ([]byte, []byte, error) {
			if c.Path == "tmux" {
				if ui.suspended && (c.Args[2] == "split-window" || c.Args[2] == "respawn-pane" || c.Args[2] == "select-pane") {
					t.Errorf("dashboard suspended during host pane command")
				}
				return fake.capture(c)
			}
			if indexSlice(c.Args, []string{"run", "--detach"}) >= 0 {
				<-ctx.Done()
				return nil, nil, ctx.Err()
			}
			return base.Capture(ctx, c)
		},
		attached: func(context.Context, process.Command) error {
			t.Fatal("host pane launch performed terminal handoff")
			return nil
		},
	}
	a := newFixtureApp(t, root, runner, &bytes.Buffer{}, &bytes.Buffer{})
	a.deps.Environment = append(a.deps.Environment, "TMUX=/tmp/host,tmux,100,0", "TMUX_PANE=%0")
	keys := []string{"e", "e", "g", "q", "y"}
	ui.read = func() (string, error) {
		key := keys[0]
		keys = keys[1:]
		return key, nil
	}
	code, err := a.centralWithTerminal(context.Background(), path, ui)
	if code != 0 || err != nil || ui.resumes != 1 || !ui.suspended {
		t.Fatalf("code=%d err=%v resumes=%d restored=%t", code, err, ui.resumes, ui.suspended)
	}
	launches := 0
	for _, c := range fake.calls {
		if c.Args[2] == "respawn-pane" {
			launches++
			if indexSlice(c.Args, []string{"-c", dir, "--", "env", "--"}) < 0 {
				t.Fatalf("not a shell-free project-root launch: %v", c.Args)
			}
		}
	}
	if launches != 2 || fake.next != 1 || len(fake.panes) != 1 {
		t.Fatalf("launches=%d surviving panes=%v", launches, fake.panes)
	}
}

func TestCentralPaneReuseAndOwnership(t *testing.T) {
	fake := newCentralTmuxFake()
	runner := &recordingRunner{capture: fake.capture}
	a, err := New(Dependencies{Runner: runner, Environment: []string{"TMUX=/tmp/host,tmux,100,0", "TMUX_PANE=%0"}, InvocationDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	p, err := a.newCentralPanes(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		if err := p.open(ctx, "one:nvim", "/project with spaces", []string{"nvim"}); err != nil {
			t.Fatal(err)
		}
	}
	if fake.next != 1 {
		t.Fatal("did not reuse pane")
	}
	pane := fake.panes["%1"]
	pane.window = "@moved"
	fake.panes["%1"] = pane
	if err := p.open(ctx, "one:nvim", "/project", []string{"nvim"}); err != nil || fake.next != 1 || fake.panes["%1"].window != "@0" {
		t.Fatalf("did not restore live moved pane: %v %v", fake.panes, err)
	}
	pane = fake.panes["%1"]
	pane.owner = "another-central"
	fake.panes["%1"] = pane
	if err := p.open(ctx, "one:nvim", "/project", []string{"nvim"}); err != nil {
		t.Fatal(err)
	}
	if fake.next != 2 || fake.panes["%1"].owner != "another-central" {
		t.Fatal("reused or mutated another owner's pane")
	}
	pane = fake.panes["%2"]
	pane.dead = true
	fake.panes["%2"] = pane
	if err := p.open(ctx, "one:nvim", "/project", []string{"nvim"}); err != nil {
		t.Fatal(err)
	}
	if fake.next != 3 {
		t.Fatal("did not replace dead pane")
	}
	if err := p.close(ctx); err != nil {
		t.Fatal(err)
	}
	if len(fake.panes) != 2 || fake.panes["%0"].id == "" || fake.panes["%1"].owner != "another-central" {
		t.Fatalf("deleted unowned panes: %v", fake.panes)
	}
}

func TestCentralPaneInterruptedCreationStillCleans(t *testing.T) {
	for _, failedCommand := range []string{"split-window", "set-option"} {
		t.Run(failedCommand, func(t *testing.T) {
			fake := newCentralTmuxFake()
			runner := &recordingRunner{capture: func(c process.Command) ([]byte, []byte, error) {
				if c.Args[2] == failedCommand {
					if failedCommand == "split-window" {
						_, _, _ = fake.capture(c) // Server acted, but the client lost its response.
					}
					return nil, nil, context.Canceled
				}
				return fake.capture(c)
			}}
			a, err := New(Dependencies{Runner: runner, Environment: []string{"TMUX=/tmp/host,tmux,100,0", "TMUX_PANE=%0"}, InvocationDir: t.TempDir()})
			if err != nil {
				t.Fatal(err)
			}
			p, err := a.newCentralPanes(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			if err := p.open(context.Background(), "tool", "/project", []string{"nvim"}); err == nil {
				t.Fatal("lost interrupted creation error")
			}
			if len(fake.panes) != 2 {
				t.Fatal("test did not create a placeholder")
			}
			if err := p.close(context.Background()); err != nil || len(fake.panes) != 1 || fake.panes["%0"].id == "" {
				t.Fatalf("leaked temporary pane or deleted Central: %v %v", fake.panes, err)
			}
		})
	}
}

func TestCentralSharedPaneRecoversInterruptedCreation(t *testing.T) {
	fake := newCentralTmuxFake()
	interrupted := true
	runner := &recordingRunner{capture: func(c process.Command) ([]byte, []byte, error) {
		out, stderr, err := fake.capture(c)
		if c.Args[2] == "split-window" && interrupted {
			interrupted = false
			return nil, nil, context.Canceled
		}
		return out, stderr, err
	}}
	a, err := New(Dependencies{Runner: runner, Environment: []string{"TMUX=/tmp/host,tmux,100,0", "TMUX_PANE=%0"}, InvocationDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	p, err := a.newCentralPanes(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if err := p.open(ctx, centralToolPaneSlot, "/project", []string{"nvim"}); err == nil {
		t.Fatal("lost interrupted creation error")
	}
	for i := 0; i < 2; i++ {
		if err := p.open(ctx, centralToolPaneSlot, "/project", []string{"nvim"}); err != nil {
			t.Fatal(err)
		}
		if fake.next != 1 || len(fake.panes) != 2 || fake.panes["%1"].owner != p.owner || fake.panes["%1"].start != "env -- nvim" {
			t.Fatalf("did not recover a single owned pane: %v", fake.panes)
		}
	}
	if err := p.close(ctx); err != nil || len(fake.panes) != 1 {
		t.Fatalf("cleanup failed: %v %v", fake.panes, err)
	}
}

func TestCentralPaneRejectsReplacementServer(t *testing.T) {
	runner := &recordingRunner{capture: func(c process.Command) ([]byte, []byte, error) {
		if c.Args[2] != "list-panes" {
			t.Fatalf("mutated replacement server: %v", c.Args)
		}
		return []byte("%0|@0||0|200|sleep 300\n"), nil, nil
	}}
	a, err := New(Dependencies{Runner: runner, Environment: []string{"TMUX=/tmp/host,tmux,100,0", "TMUX_PANE=%0"}, InvocationDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := a.newCentralPanes(context.Background()); err == nil || !strings.Contains(err.Error(), "replaced") {
		t.Fatalf("err=%v", err)
	}
}

func TestCentralHostPaneAgentChecksSpecificRun(t *testing.T) {
	for _, mismatch := range []bool{false, true} {
		t.Run(fmt.Sprint(mismatch), func(t *testing.T) {
			fake := newCentralTmuxFake()
			labels := composeProjectLabels("hash", 123, "sandbox", "compose")
			labels["wisp.run-id"] = "expected"
			actual := cloneEnvironment(labels)
			if mismatch {
				actual["wisp.run-id"] = "replacement"
			}
			runner := &recordingRunner{capture: func(c process.Command) ([]byte, []byte, error) {
				if c.Path == "tmux" {
					return fake.capture(c)
				}
				if c.Path != "docker" || !reflect.DeepEqual(c.Args, []string{"container", "inspect", "immutable-id"}) {
					t.Fatalf("unexpected command: %#v", c)
				}
				out, err := json.Marshal([]any{map[string]any{"Id": "immutable-id", "Config": map[string]any{"Labels": actual}, "State": map[string]any{"Running": true}}})
				return out, nil, err
			}}
			a, err := New(Dependencies{Runner: runner, InvocationDir: t.TempDir(), Environment: []string{}, UID: 123, GID: 456})
			if err != nil {
				t.Fatal(err)
			}
			p := &centralPanes{app: a, socket: "/tmp/host,tmux", server: "100", root: "%0", owner: "owner", panes: map[string]string{}}
			project := &centralProject{plan: SandboxPlan{Project: project.Project{Hash: "hash", RootDir: "/root"}}, sandbox: centralSandbox{ID: "immutable-id", Labels: labels}}
			err = a.openCentralPane(context.Background(), p, project, "\r")
			if (err != nil) != mismatch {
				t.Fatalf("err=%v", err)
			}
			if mismatch {
				if len(fake.calls) != 0 {
					t.Fatal("opened host pane for replacement sandbox")
				}
				return
			}
			found := false
			for _, c := range fake.calls {
				if c.Args[2] == "respawn-pane" {
					found = indexSlice(c.Args, []string{"env", "--", "docker", "exec", "--interactive", "--tty", "--user", "123:456", "immutable-id", "tmux", "-u", "attach-session", "-t", "wisp"}) >= 0
				}
			}
			if !found {
				t.Fatal("did not attach by verified immutable ID")
			}
		})
	}
}

func TestCentralAgentPaneSwitchesBetweenSandboxes(t *testing.T) {
	fake := newCentralTmuxFake()
	projects := map[string]*centralProject{}
	for _, id := range []string{"first", "second"} {
		labels := composeProjectLabels(id, 123, "sandbox", id)
		labels["wisp.run-id"] = id + "-run"
		projects[id] = &centralProject{
			plan:    SandboxPlan{Project: project.Project{Hash: id, RootDir: "/" + id}},
			sandbox: centralSandbox{ID: id, Labels: labels},
		}
	}
	runner := &recordingRunner{capture: func(c process.Command) ([]byte, []byte, error) {
		if c.Path == "tmux" {
			return fake.capture(c)
		}
		if c.Path != "docker" || len(c.Args) != 3 || !reflect.DeepEqual(c.Args[:2], []string{"container", "inspect"}) {
			t.Fatalf("unexpected sandbox mutation: %#v", c)
		}
		p := projects[c.Args[2]]
		out, err := json.Marshal([]any{map[string]any{"Id": p.sandbox.ID, "Config": map[string]any{"Labels": p.sandbox.Labels}, "State": map[string]any{"Running": true}}})
		return out, nil, err
	}}
	a, err := New(Dependencies{Runner: runner, InvocationDir: t.TempDir(), Environment: []string{}, UID: 123, GID: 456})
	if err != nil {
		t.Fatal(err)
	}
	panes := &centralPanes{app: a, socket: "/tmp/host,tmux", server: "100", root: "%0", owner: "owner", panes: map[string]string{}}
	for _, id := range []string{"first", "first", "second", "second", "first"} {
		if err := a.openCentralPane(context.Background(), panes, projects[id], "\r"); err != nil {
			t.Fatal(err)
		}
		if fake.next != 1 || len(fake.panes) != 2 || panes.panes[centralToolPaneSlot] != "%1" || panes.activeTool != "agent:"+id {
			t.Fatalf("did not switch one pane: panes=%v active=%s", fake.panes, panes.activeTool)
		}
		if !strings.Contains(fake.panes["%1"].start, id+" tmux -u attach-session") {
			t.Fatalf("wrong visible sandbox: %s", fake.panes["%1"].start)
		}
	}
	var attachedIDs []string
	for _, c := range fake.calls {
		if c.Args[2] == "respawn-pane" {
			i := indexSlice(c.Args, []string{"--user", "123:456"})
			attachedIDs = append(attachedIDs, c.Args[i+2])
		}
	}
	if !reflect.DeepEqual(attachedIDs, []string{"first", "second", "first"}) {
		t.Fatalf("unnecessary or missing switches: %v", attachedIDs)
	}
	// All projects and tools replace this same right-hand pane. Repeating an
	// open of the visible tool focuses it without restarting it.
	for _, step := range []struct{ project, key, identity, command string }{
		{"first", "e", "nvim:first", "env -- nvim"},
		{"first", "e", "nvim:first", "env -- nvim"},
		{"second", "e", "nvim:second", "env -- nvim"},
		{"second", "g", "lazygit:second", "env -- lazygit"},
		{"second", "g", "lazygit:second", "env -- lazygit"},
		{"first", "\r", "agent:first", "first tmux -u attach-session"},
	} {
		if err := a.openCentralPane(context.Background(), panes, projects[step.project], step.key); err != nil {
			t.Fatal(err)
		}
		if fake.next != 1 || len(fake.panes) != 2 || len(panes.panes) != 1 || panes.activeTool != step.identity || !strings.Contains(fake.panes["%1"].start, step.command) {
			t.Fatalf("created extra pane or failed to switch: panes=%v active=%s", fake.panes, panes.activeTool)
		}
	}
	launches := 0
	for _, c := range fake.calls {
		if c.Args[2] == "respawn-pane" {
			launches++
		}
		if c.Args[2] == "split-window" && (indexSlice(c.Args, []string{"-h", "-t", "%0"}) < 0 || indexSlice(c.Args, []string{"-v"}) >= 0) {
			t.Fatalf("subdivided the right side: %v", c.Args)
		}
	}
	if launches != 7 {
		t.Fatalf("unexpected tool restarts: %d", launches)
	}
}

func TestCentralPanesWithRealTmux(t *testing.T) {
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("tmux unavailable")
	}
	// Keep the Unix socket path short even on systems with long test temp paths.
	dir, err := os.MkdirTemp("", "wisp-tmux-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	socket := filepath.Join(dir, "socket")
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	runner := process.OSRunner{}
	control := func(args ...string) ([]byte, error) {
		out, stderr, err := runner.Capture(ctx, process.Command{Path: "tmux", Args: append([]string{"-S", socket}, args...), Env: os.Environ()})
		if err != nil {
			return out, fmt.Errorf("tmux: %w: %s", err, stderr)
		}
		return out, nil
	}
	if _, err := control("-f", "/dev/null", "new-session", "-d", "-s", "test", "-x", "160", "-y", "50", "--", "sleep", "300"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		cleanupCtx, stop := context.WithTimeout(context.Background(), 3*time.Second)
		defer stop()
		_, _, _ = runner.Capture(cleanupCtx, process.Command{Path: "tmux", Args: []string{"-S", socket, "kill-server"}, Env: os.Environ()})
	})
	out, err := control("display-message", "-p", "-t", "test", "#{pane_id}|#{pid}")
	if err != nil {
		t.Fatal(err)
	}
	fields := strings.Split(strings.TrimSpace(string(out)), "|")
	if len(fields) != 2 {
		t.Fatalf("unexpected identity: %q", out)
	}
	root := fields[0]
	a, err := New(Dependencies{Runner: runner, InvocationDir: dir, Environment: append(os.Environ(), "TMUX="+socket+","+fields[1]+",0", "TMUX_PANE="+root)})
	if err != nil {
		t.Fatal(err)
	}
	p, err := a.newCentralPanes(ctx)
	if err != nil {
		t.Fatal(err)
	}
	projectDir := filepath.Join(dir, "project with spaces; and 'quotes'")
	if err := os.Mkdir(projectDir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := p.open(ctx, centralToolPaneSlot, projectDir, []string{"sleep", "300"}); err != nil {
		t.Fatal(err)
	}
	one := p.panes[centralToolPaneSlot]
	if err := p.open(ctx, centralToolPaneSlot, projectDir, []string{"sleep", "300"}); err != nil || p.panes[centralToolPaneSlot] != one {
		t.Fatalf("reuse failed: %v", err)
	}
	before, err := control("display-message", "-p", "-t", one, "#{pane_pid}")
	if err != nil {
		t.Fatal(err)
	}
	if err := p.openReplacing(ctx, centralToolPaneSlot, projectDir, []string{"sleep", "301"}, true); err != nil || p.panes[centralToolPaneSlot] != one {
		t.Fatalf("replacement did not reuse pane: %v", err)
	}
	after, err := control("display-message", "-p", "-t", one, "#{pane_pid}")
	if err != nil || bytes.Equal(before, after) {
		t.Fatalf("replacement did not change pane process: before=%s after=%s err=%v", before, after, err)
	}
	out, err = control("display-message", "-p", "-t", one, "#{pane_current_path}|#{window_id}")
	if err != nil || !strings.HasPrefix(string(out), projectDir+"|") {
		t.Fatalf("wrong working directory: %q %v", out, err)
	}
	if _, err := control("break-pane", "-d", "-s", one, "-n", "moved"); err != nil {
		t.Fatal(err)
	}
	if err := p.open(ctx, centralToolPaneSlot, projectDir, []string{"sleep", "300"}); err != nil || p.panes[centralToolPaneSlot] != one {
		t.Fatalf("did not reuse moved live pane: %v", err)
	}
	panesAfterMove, err := p.list(ctx)
	if err != nil || len(panesAfterMove) != 2 || panesAfterMove[one].window != panesAfterMove[root].window {
		t.Fatalf("moved pane was not restored to Central's window: %v %v", panesAfterMove, err)
	}
	rootHeight, err := control("display-message", "-p", "-t", root, "#{pane_height}")
	if err != nil {
		t.Fatal(err)
	}
	toolHeight, err := control("display-message", "-p", "-t", one, "#{pane_height}")
	if err != nil || !bytes.Equal(rootHeight, toolHeight) {
		t.Fatalf("right side is not full-height: central=%s tool=%s err=%v", rootHeight, toolHeight, err)
	}
	out, err = control("split-window", "-d", "-v", "-t", root, "-P", "-F", "#{pane_id}", "--", "sleep", "300")
	if err != nil {
		t.Fatal(err)
	}
	unowned := strings.TrimSpace(string(out))
	// Record exact argv, not just exit status: shell interpretation would create
	// marker files or change quotes, whitespace, and command substitutions.
	script := filepath.Join(dir, "record-argv")
	if err := os.WriteFile(script, []byte("#!/bin/sh\nout=$1\nshift\nprintf '%s\\0' \"$@\" > \"$out\"\nsleep 1\n"), 0700); err != nil {
		t.Fatal(err)
	}
	output := filepath.Join(dir, "argv")
	marker := filepath.Join(dir, "shell-injection")
	literal := []string{"one two", "quotes ' and \"", "; touch " + marker, "$(touch " + marker + ")", "`touch " + marker + "`", ""}
	argv := append([]string{script, output}, literal...)
	if err := p.openReplacing(ctx, centralToolPaneSlot, projectDir, argv, true); err != nil {
		t.Fatal(err)
	}
	for {
		data, err := os.ReadFile(output)
		if err == nil {
			if !bytes.Equal(data, []byte(strings.Join(literal, "\x00")+"\x00")) {
				t.Fatalf("argv changed: %q", data)
			}
			break
		}
		if ctx.Err() != nil {
			t.Fatalf("argv recording failed: %v", err)
		}
		time.Sleep(10 * time.Millisecond)
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatalf("shell expansion executed: %v", err)
	}
	if err := p.openReplacing(ctx, centralToolPaneSlot, projectDir, []string{"sh", "-c", "exit 7"}, true); err != nil {
		t.Fatal(err)
	}
	for {
		panes, err := p.list(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if panes[p.panes[centralToolPaneSlot]].dead {
			break
		}
		if ctx.Err() != nil {
			t.Fatal("failed pane did not retain exit status")
		}
		time.Sleep(10 * time.Millisecond)
	}
	// Simulate a split whose response was lost before the ownership option was
	// installed. Its temporary argv nonce must still make cleanup safe.
	out, err = control("split-window", "-d", "-v", "-t", root, "-P", "-F", "#{pane_id}", "--", "env", "WISP_CENTRAL_PANE_OWNER="+p.owner, "sleep", "30")
	if err != nil {
		t.Fatal(err)
	}
	pending := strings.TrimSpace(string(out))
	panes, err := p.list(ctx)
	if err != nil || panes[pending].owner != p.owner {
		t.Fatalf("lost untagged placeholder ownership: %v %v", panes, err)
	}
	if err := p.close(ctx); err != nil {
		t.Fatal(err)
	}
	panes, err = p.list(ctx)
	if err != nil || len(panes) != 2 || panes[root].id == "" || panes[unowned].id == "" {
		t.Fatalf("cleanup changed unowned panes: %v %v", panes, err)
	}
}

func TestCentralPaneControlFailureIsReported(t *testing.T) {
	runner := &recordingRunner{capture: func(process.Command) ([]byte, []byte, error) {
		return nil, []byte("do not echo server environment"), errors.New("control failed")
	}}
	a, err := New(Dependencies{Runner: runner, Environment: []string{"TMUX=/tmp/host,tmux,100,0", "TMUX_PANE=%0"}, InvocationDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	_, err = a.newCentralPanes(context.Background())
	if err == nil || !strings.Contains(err.Error(), "host tmux list-panes") || strings.Contains(err.Error(), "server environment") {
		t.Fatalf("err=%v", err)
	}
}
