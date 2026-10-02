package app

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"

	"wisp/internal/process"
)

const centralPaneOwnerOption = "@wisp-central-owner"
const centralToolPaneSlot = "tool"

// Host tmux owns layout only. The container's tmux still owns the agent PTY.
// Pin the socket and pane IDs: never act on whichever window happens to be active.
type centralPanes struct {
	app    *App
	socket string
	server string
	root   string
	owner  string
	panes  map[string]string
	// All tools share one visible slot; container tmux keeps agents alive.
	activeTool string
}

type centralPane struct {
	id, window, owner string
	start             string
	dead              bool
}

func (a *App) newCentralPanes(ctx context.Context) (*centralPanes, error) {
	tmux := environmentValue(a.deps.Environment, "TMUX")
	root := environmentValue(a.deps.Environment, "TMUX_PANE")
	if tmux == "" || root == "" {
		return nil, nil // Preserve terminal handoff outside host tmux.
	}
	// Socket paths may contain commas; the final two fields are PID and index.
	end := strings.LastIndex(tmux, ",")
	if end < 0 {
		return nil, fmt.Errorf("invalid host TMUX environment")
	}
	pidEnd := end
	end = strings.LastIndex(tmux[:end], ",")
	if end <= 0 || !centralPaneID(root) {
		return nil, fmt.Errorf("invalid host TMUX environment")
	}
	token := make([]byte, 16)
	if _, err := rand.Read(token); err != nil {
		return nil, fmt.Errorf("generate Central pane owner: %w", err)
	}
	p := &centralPanes{app: a, socket: tmux[:end], server: tmux[end+1 : pidEnd], root: root, owner: hex.EncodeToString(token), panes: make(map[string]string)}
	panes, err := p.list(ctx)
	if err != nil {
		return nil, err
	}
	if _, ok := panes[root]; !ok {
		return nil, fmt.Errorf("Central host tmux pane is unavailable")
	}
	return p, nil
}

func centralPaneID(id string) bool {
	if len(id) < 2 || id[0] != '%' {
		return false
	}
	for _, c := range id[1:] {
		if c < '0' || c > '9' {
			return false
		}
	}
	return true
}

func (p *centralPanes) command(ctx context.Context, args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	out, _, err := p.app.deps.Runner.Capture(ctx, process.Command{
		Path: "tmux", Args: append([]string{"-S", p.socket}, args...), Env: p.app.childEnvironment(nil),
	})
	if err != nil {
		// Do not echo command arguments or server environment into the dashboard.
		return nil, fmt.Errorf("host tmux %s: %w", args[0], err)
	}
	return out, nil
}

func (p *centralPanes) list(ctx context.Context) (map[string]centralPane, error) {
	out, err := p.command(ctx, "list-panes", "-a", "-F", "#{pane_id}|#{window_id}|#{"+centralPaneOwnerOption+"}|#{pane_dead}|#{pid}|#{pane_start_command}")
	if err != nil {
		return nil, err
	}
	panes := make(map[string]centralPane)
	for _, line := range strings.Split(strings.TrimSuffix(string(out), "\n"), "\n") {
		fields := strings.SplitN(line, "|", 6)
		if len(fields) == 6 && centralPaneID(fields[0]) {
			if fields[4] != p.server {
				return nil, fmt.Errorf("Central host tmux server was replaced")
			}
			owner := fields[2]
			// split-window may succeed even when its client is interrupted before
			// returning the ID. A nonce in the temporary command keeps that pane
			// identifiable for cleanup, including before its option is set.
			if owner == "" && fields[5] == "env WISP_CENTRAL_PANE_OWNER="+p.owner+" sleep 30" {
				owner = p.owner
			}
			panes[fields[0]] = centralPane{id: fields[0], window: fields[1], owner: owner, dead: fields[3] == "1", start: fields[5]}
		}
	}
	return panes, nil
}

func (p *centralPanes) open(ctx context.Context, key, dir string, argv []string) error {
	return p.openReplacing(ctx, key, dir, argv, false)
}

func (p *centralPanes) openReplacing(ctx context.Context, key, dir string, argv []string, replace bool) error {
	panes, err := p.list(ctx)
	if err != nil {
		return err
	}
	root, ok := panes[p.root]
	if !ok {
		return fmt.Errorf("Central host tmux pane is unavailable")
	}
	// Recover the shared pane if a previous creation lost its response before
	// recording the ID. Opening a tool must not create a second right-side pane.
	if key == centralToolPaneSlot {
		if pane, ok := panes[p.panes[key]]; !ok || pane.owner != p.owner {
			for _, candidate := range panes {
				if candidate.id != p.root && candidate.owner == p.owner {
					p.panes[key] = candidate.id
					break
				}
			}
		}
	}
	if pane, ok := panes[p.panes[key]]; ok && pane.owner == p.owner {
		if !pane.dead {
			if pane.window != root.window {
				// Restore a manually moved pane without discarding editor state.
				args := []string{"move-pane", "-d", "-h", "-s", pane.id, "-t", p.root, "-l", "65%"}
				if _, err = p.command(ctx, args...); err != nil {
					return err
				}
			}
			if _, err = p.command(ctx, "select-pane", "-t", pane.id); err != nil {
				return err
			}
			placeholder := pane.start == "env WISP_CENTRAL_PANE_OWNER="+p.owner+" sleep 30"
			if placeholder {
				// Install durable ownership before replacing the temporary command
				// that identified a partially-created pane.
				if _, err = p.command(ctx, "set-option", "-p", "-t", pane.id, centralPaneOwnerOption, p.owner); err != nil {
					return err
				}
				if _, err = p.command(ctx, "set-option", "-p", "-t", pane.id, "remain-on-exit", "failed"); err != nil {
					return err
				}
			}
			if replace || placeholder {
				return p.respawn(ctx, pane.id, dir, argv)
			}
			return nil
		}
		// A failed tool is replaced on reopening.
		if _, err = p.command(ctx, "kill-pane", "-t", pane.id); err != nil {
			return err
		}
		delete(panes, pane.id)
	}
	// Keep Central on the left and one shared tool pane on the right. Never
	// subdivide the working area or rearrange unowned panes.
	args := []string{"split-window", "-d", "-h", "-t", p.root, "-l", "65%", "-c", dir, "-P", "-F", "#{pane_id}"}
	// First create a quiet placeholder, tag it, then start the tool. This avoids
	// losing ownership when a command exits before set-option can run. Multiple
	// command arguments make tmux exec directly, without a shell command string.
	args = append(args, "--", "env", "WISP_CENTRAL_PANE_OWNER="+p.owner, "sleep", "30")
	out, err := p.command(ctx, args...)
	if err != nil {
		return err
	}
	id := strings.TrimSpace(string(out))
	if !centralPaneID(id) {
		return fmt.Errorf("host tmux returned an invalid pane ID")
	}
	if _, err = p.command(ctx, "set-option", "-p", "-t", id, centralPaneOwnerOption, p.owner); err != nil {
		return err // The nonce-marked placeholder remains identifiable for cleanup.
	}
	p.panes[key] = id
	if _, err = p.command(ctx, "set-option", "-p", "-t", id, "remain-on-exit", "failed"); err != nil {
		return err
	}
	// Focus the placeholder before starting the tool: a command that exits
	// immediately must not turn a successful launch into a select-pane error.
	if _, err = p.command(ctx, "select-pane", "-t", id); err != nil {
		return err
	}
	return p.respawn(ctx, id, dir, argv)
}

func (p *centralPanes) respawn(ctx context.Context, id, dir string, argv []string) error {
	// Replacing a host attachment kills only the Docker/tmux client, not the
	// agent running in the container's persistent tmux session. Switching away
	// from a host editor or lazygit terminates that tool.
	// env -- also ensures single-word tools (nvim/lazygit) are not interpreted
	// as shell commands. Host tools inherit the host tmux session environment;
	// no sandbox/broker environment is copied into the tmux server.
	args := append([]string{"respawn-pane", "-k", "-t", id, "-c", dir, "--", "env", "--"}, argv...)
	_, err := p.command(ctx, args...)
	return err
}

func (p *centralPanes) close(ctx context.Context) error {
	panes, err := p.list(ctx)
	if err != nil {
		return err
	}
	var result error
	for _, pane := range panes {
		if pane.id != p.root && pane.owner == p.owner {
			_, err := p.command(ctx, "kill-pane", "-t", pane.id)
			result = errors.Join(result, err)
		}
	}
	return result
}

func (a *App) openCentralPane(ctx context.Context, panes *centralPanes, p *centralProject, key string) error {
	tool := "agent"
	var argv []string
	switch key {
	case "e":
		tool, argv = "nvim", []string{"nvim"}
	case "g":
		tool, argv = "lazygit", []string{"lazygit"}
	default:
		args, err := a.centralAttachmentArgs(ctx, p.sandbox)
		if err != nil {
			return err
		}
		argv = append([]string{"docker"}, args...)
	}
	identity := tool + ":" + p.plan.Project.Hash
	if tool == "agent" {
		identity = tool + ":" + p.sandbox.ID
	}
	err := panes.openReplacing(ctx, centralToolPaneSlot, p.plan.Project.RootDir, argv, panes.activeTool != identity)
	if err == nil {
		panes.activeTool = identity
	}
	return err
}
