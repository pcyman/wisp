package app

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"
	"unicode/utf8"

	"wisp/internal/agentstatus"
	"wisp/internal/cli"
	"wisp/internal/config"
	"wisp/internal/process"
)

type centralProject struct {
	name          string
	plan          SandboxPlan
	state         string
	cancel        context.CancelFunc
	sandbox       centralSandbox
	log           *centralLog
	err           error
	activity      agentstatus.Report
	activityStale bool
}

type centralRandomReader struct {
	mu     sync.Mutex
	reader io.Reader
}

func (r *centralRandomReader) Read(p []byte) (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.reader.Read(p)
}

type centralEvent struct {
	index   int
	ready   bool
	sandbox centralSandbox
	err     error
}

func (a *App) centralPlans(ctx context.Context, override string) ([]centralProject, error) {
	env := config.Environment{Home: a.env.Home, XDGConfigHome: a.env.XDGConfigHome, XDGDataHome: a.env.XDGDataHome}
	path, err := config.ResolvePath(override, a.deps.InvocationDir, env)
	if err != nil {
		return nil, err
	}
	loaded, err := config.LoadForRun(path, env, "")
	if err != nil {
		return nil, err
	}
	catalog, err := config.CentralProjects(loaded.Config, loaded.Path, env)
	if err != nil {
		return nil, err
	}
	if len(catalog) == 0 {
		return nil, fmt.Errorf("no central projects configured; add [[central.projects]] entries with name and path to %s", loaded.Path)
	}
	projects := make([]centralProject, 0, len(catalog))
	seen := make(map[string]string)
	for _, entry := range catalog {
		plan, err := PlanRun(ctx, cli.RunRequest{Directory: entry.Path, ConfigPath: loaded.Path}, a.planOptions())
		if err != nil {
			return nil, fmt.Errorf("central project %q: %w", entry.Name, err)
		}
		if other := seen[plan.Project.Hash]; other != "" {
			return nil, fmt.Errorf("central projects %q and %q resolve to the same worktree", other, entry.Name)
		}
		seen[plan.Project.Hash] = entry.Name
		projects = append(projects, centralProject{name: entry.Name, plan: plan, state: "closed", log: &centralLog{}})
	}
	return projects, nil
}

func (a *App) central(parent context.Context, configPath string) (status int, resultErr error) {
	terminal, err := newCentralTerminal(a.deps.Stdin, a.deps.Stdout)
	if err != nil {
		return 1, err
	}
	return a.centralWithTerminal(parent, configPath, terminal)
}

func (a *App) centralWithTerminal(parent context.Context, configPath string, terminal centralUI) (status int, resultErr error) {
	ctx, cancel := context.WithCancel(parent)
	defer cancel()
	var handoff atomic.Bool
	var interrupted atomic.Int32
	signals := make(chan os.Signal, 4)
	signal.Notify(signals, os.Interrupt, syscall.SIGTERM, syscall.SIGHUP)
	defer signal.Stop(signals)
	go func() {
		for {
			select {
			case sig := <-signals:
				// Ctrl-C belongs to the foreground editor/agent during handoff.
				if sig != os.Interrupt || !handoff.Load() {
					if number, ok := sig.(syscall.Signal); ok {
						interrupted.Store(int32(128 + number))
					}
					cancel()
				}
			case <-ctx.Done():
				return
			}
		}
	}()
	// Validate the whole catalog before any launch or Docker side effects.
	projects, err := a.centralPlans(ctx, configPath)
	if err != nil {
		return 1, err
	}
	if err := terminal.resume(); err != nil {
		_ = terminal.suspend()
		return 1, err
	}
	defer func() {
		resultErr = errors.Join(resultErr, terminal.suspend())
		if resultErr != nil && status == 0 {
			status = 1
		}
	}()

	events := make(chan centralEvent, 2*len(projects))
	var workers sync.WaitGroup
	defer func() {
		cancel()
		_, _ = io.WriteString(a.deps.Stdout, "\x1b[2J\x1b[HStopping Central sandboxes...\r\n")
		workers.Wait() // Each run has its own bounded, ownership-checked cleanup.
		close(events)
		for _, project := range projects {
			resultErr = errors.Join(resultErr, project.err)
		}
		for event := range events {
			if !event.ready && event.err != nil {
				resultErr = errors.Join(resultErr, event.err)
			}
		}
		if resultErr != nil && status == 0 {
			status = 1
		}
	}()
	statusRequests := make(chan []string, 1)
	statusUpdates := make(chan centralStatusUpdate, 1)
	workers.Add(1)
	go func() {
		defer workers.Done()
		ticker := time.NewTicker(time.Second)
		defer ticker.Stop()
		pollCentralStatus(ctx, statusRequests, statusUpdates, ticker.C, a.collectAgentSnapshot)
	}()
	refreshStatus := func() { centralLatest(statusRequests, centralStatusRuns(projects)) }
	random := a.deps.Random
	if random != nil {
		random = &centralRandomReader{reader: random}
	}
	start := func(index int) {
		p := &projects[index]
		if p.cancel != nil {
			return
		}
		runCtx, stop := context.WithCancel(ctx)
		p.cancel, p.state, p.err = stop, "starting", nil
		p.sandbox, p.activity, p.activityStale = centralSandbox{}, agentstatus.Report{}, false
		p.log = &centralLog{}
		worker := *a
		worker.deps = a.deps
		worker.deps.Random = random
		worker.deps.Stdin = strings.NewReader("")
		worker.deps.Stdout, worker.deps.Stderr = p.log, p.log
		plan := p.plan
		workers.Add(1)
		go func() {
			defer workers.Done()
			defer stop()
			code, err := worker.runPlan(runCtx, plan, func(sandbox centralSandbox) {
				events <- centralEvent{index: index, ready: true, sandbox: sandbox}
			})
			if err == nil && code != 0 {
				err = fmt.Errorf("sandbox exited with status %d", code)
			}
			events <- centralEvent{index: index, err: err}
		}()
	}

	selected := 0
	query, message := "", "Open a project with Enter or o."
	search, confirm, showLog := false, false, false
	statusWarning := ""
	for ctx.Err() == nil {
		for draining := true; draining; {
			select {
			case event := <-events:
				p := &projects[event.index]
				if event.ready {
					p.sandbox = event.sandbox
					if p.state != "stopping" {
						p.state = "running"
					}
				} else {
					p.cancel, p.err = nil, event.err
					p.state = "closed"
					if event.err != nil {
						p.state = "failed"
						message = event.err.Error()
					}
				}
				refreshStatus()
			default:
				draining = false
			}
		}
		select {
		case update := <-statusUpdates:
			if applyCentralStatus(projects, update) {
				statusWarning = ""
				if update.err != nil {
					statusWarning = "Agent status refresh unavailable: " + update.err.Error()
				}
			}
		default:
		}
		if len(centralStatusRuns(projects)) == 0 {
			statusWarning = ""
		}
		visible := centralVisible(projects, query)
		if selected >= len(visible) {
			selected = max(0, len(visible)-1)
		}
		width, height := terminal.size()
		displayMessage := message
		if statusWarning != "" {
			displayMessage = statusWarning
		}
		if err := renderCentral(a.deps.Stdout, projects, visible, selected, query, displayMessage, search, confirm, showLog, width, height); err != nil {
			return 1, err
		}
		key, err := terminal.key()
		if err != nil {
			return 1, err
		}
		if key == "" {
			continue
		}
		if confirm {
			if key == "y" || key == "Y" {
				break
			}
			confirm = false
			continue
		}
		if search && key != "\x03" {
			switch key {
			case "\r", "\n":
				search = false
			case "\x1b":
				search, query = false, ""
			case "\x7f", "\b":
				if query != "" {
					_, size := utf8.DecodeLastRuneInString(query)
					query = query[:len(query)-size]
				}
			default:
				if len(key) <= utf8.UTFMax && key != "up" && key != "down" && centralText(key, 4) == key && len(query) < 256 {
					query += key
				}
			}
			selected = 0
			continue
		}
		switch key {
		case "q", "\x03":
			active := false
			for _, p := range projects {
				active = active || p.cancel != nil
			}
			if !active {
				return 0, nil
			}
			confirm = true
		case "j", "down":
			selected = min(selected+1, max(0, len(visible)-1))
		case "k", "up":
			selected = max(0, selected-1)
		case "/":
			search, query, selected = true, "", 0
		case "\x1b":
			query = ""
		case "l":
			showLog = !showLog
		case "o", "\r", "\n", "e", "g", "x":
			if len(visible) == 0 {
				continue
			}
			index := visible[selected]
			p := &projects[index]
			if key == "x" {
				if p.cancel != nil {
					p.state = "stopping"
					p.cancel()
					refreshStatus()
				}
				continue
			}
			if key == "o" && p.cancel != nil {
				message = p.name + " is already open."
				continue
			}
			if key == "o" || p.cancel == nil {
				start(index)
				message = "Starting " + p.name + " (l shows startup logs)."
				if key != "e" && key != "g" {
					continue
				}
			}
			if key != "e" && key != "g" && p.state != "running" {
				message = "Wait for this project to finish starting."
				continue
			}
			handoff.Store(true)
			if err := terminal.suspend(); err != nil {
				return 1, err
			}
			if key == "e" || key == "g" {
				path := "nvim"
				if key == "g" {
					path = "lazygit"
				}
				err = a.deps.Runner.Attached(ctx, process.Command{Path: path, Dir: p.plan.Project.RootDir, Env: a.childEnvironment(nil), Stdin: a.deps.Stdin, Stdout: a.deps.Stdout, Stderr: a.deps.Stderr})
			} else {
				err = a.attachCentral(ctx, p.sandbox)
			}
			handoff.Store(false)
			if resumeErr := terminal.resume(); resumeErr != nil {
				return 1, resumeErr
			}
			message = ""
			if err != nil {
				message = err.Error()
			}
		}
	}
	if code := interrupted.Load(); code != 0 {
		return int(code), nil
	}
	if ctx.Err() != nil {
		return signalStatus(ctx, 1), nil
	}
	return 0, nil
}

func centralVisible(projects []centralProject, query string) []int {
	query = strings.ToLower(query)
	var result []int
	for i, p := range projects {
		if strings.Contains(strings.ToLower(p.name+" "+p.plan.Project.RootDir), query) {
			result = append(result, i)
		}
	}
	return result
}

func renderCentral(out io.Writer, projects []centralProject, visible []int, selected int, query, message string, search, confirm, showLog bool, width, height int) error {
	lines := []string{"Wisp Central", "", "  PROJECT                  SANDBOX       AGENT      ACTIVITY"}
	rows := max(1, height-9)
	if showLog {
		rows = max(1, rows-5)
	}
	offset := max(0, selected-rows+1)
	for i := offset; i < len(visible) && i < offset+rows; i++ {
		p := projects[visible[i]]
		marker := " "
		if i == selected {
			marker = ">"
		}
		lines = append(lines, fmt.Sprintf("%s %-24s %-13s %-10s %s", marker, centralText(p.name, 24), p.state, p.plan.AgentName, centralActivity(p)))
	}
	if len(visible) == 0 {
		lines = append(lines, "  No matching projects.")
	}
	if len(visible) > 0 {
		lines = append(lines, "", projects[visible[selected]].plan.Project.RootDir)
	}
	if showLog && len(visible) > 0 {
		log := strings.Split(strings.TrimSpace(projects[visible[selected]].log.String()), "\n")
		lines = append(lines, "-- startup log --")
		lines = append(lines, log[max(0, len(log)-4):]...)
	}
	if query != "" || search {
		lines = append(lines, "/"+query)
	}
	if confirm {
		message = "Stop all and quit? y / any other key cancels"
	}
	lines = append(lines, "", message, "j/k move  / filter  enter agent  o open  e nvim  g lazygit  x stop  l logs  q quit", "Agent detach: Ctrl-b, d. Copy: Shift-drag, then your terminal's copy shortcut.")
	if len(lines) > height {
		footer := min(4, height)
		lines = append(lines[:max(0, height-footer)], lines[len(lines)-footer:]...)
	}
	var screen strings.Builder
	screen.WriteString("\x1b[H")
	for i := 0; i < height; i++ {
		if i < len(lines) {
			screen.WriteString(centralText(lines[i], max(0, width-1)))
		}
		screen.WriteString("\x1b[K")
		if i+1 < height {
			screen.WriteString("\r\n")
		}
	}
	_, err := io.WriteString(out, screen.String())
	return err
}
