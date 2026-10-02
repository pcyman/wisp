# Wisp Central

Central is a host-side terminal switchboard for on-demand project sandboxes.
It requires an interactive terminal, but does not require host tmux. When run
inside host tmux (3.2 or newer), it opens tools in panes of the same window.

## Configuration

Add bookmarks to your global Wisp config (locate it with `wisp config path`):

```toml
[[central.projects]]
name = "wisp"
path = "~/src/wisp"

[[central.projects]]
name = "website"
path = "~/src/website"
```

Names must be unique. Paths support `~/` and config-directory-relative paths.
Central physically resolves directories and identifies Git worktrees before
starting Docker. Two entries resolving to the same worktree are rejected.
Unavailable bookmarks block Central, but not ordinary runs in other projects.

All projects use the normal global agent, AWS alias, and mount configuration.
The first version does not offer per-project overrides.

## First launch after upgrading

The runtime assets are embedded in the binary. Rebuild/install the binary:

```sh
go build -trimpath -o ~/.local/bin/wisp ./cmd/wisp
```

Existing image tags need rebuilding to include tmux and the session wrapper.
Run `wisp --rebuild /path/to/a/project` once, then exit that ordinary agent
session before launching Central. Without existing tags, Wisp builds its images
automatically. Images are retained when Central quits.

Run `wisp central`, or `wisp central --config /path/to/config.toml`.
See `wisp help central` for keys.

## Interaction

The dashboard shows configured projects, sandbox lifecycle, selected harness,
and live agent activity, with colored status icons and animated indicators for
starting, stopping, and working projects. The header summarizes running and
busy projects; the selected project's path and activity appear below the list.
On narrow terminals, secondary columns collapse into this detail area.
Set `NO_COLOR=1` to disable colors while retaining icons and status text.

Nothing starts until you open a project. Enter or `o` starts its sandbox asynchronously;
press Enter again once running to attach to the agent.

`e` and `g` launch host Neovim and lazygit in the physical project root.
They also start the sandbox if the project is closed.

Inside host tmux, Central stays visible on the left with one shared tool pane
on the right. Enter, `e`, and `g` replace the entire right-hand pane with the
selected project's agent, Neovim, or lazygit; Central never splits that area.
Repeating an open of the already-visible project/tool just focuses its pane.
Switching away from an agent leaves its sandbox running in the background.
Switching away from Neovim or lazygit closes that host tool: **save editor changes
before switching tools or projects**. Editor sessions are not preserved.
Use host tmux's pane navigation (by default **Ctrl-b, then an arrow**) to return
to Central. Ordinary tool exit closes its pane; failed commands retain
their pane so errors remain visible. Reopening replaces a failed pane. Tools use
the host tmux session environment and the physical project directory.

Outside host tmux, Central suspends its screen and terminal input while another
interactive program has control. Exiting the tool returns to Central. Missing
tools are reported in the UI. Central does not start a host tmux server for you.

Filter by name or path with `/`; Enter finishes editing the filter and Escape
clears it. `l` toggles a bounded tail of the selected project's startup logs.
`x` stops a project; it can be reopened after cleanup completes.

## Agent terminals

Each sandbox runs one agent inside one bundled tmux session. Attachment uses
`docker exec -it … tmux -u attach-session`, not `docker attach`. Tmux owns the
persistent agent terminal and redraws it when clients reconnect or resize.

Outside host tmux, detach with **Ctrl-b, then d** to return to Central. Inside
host tmux with the default prefix, use **Ctrl-b twice, then d** to detach the
inner tmux and close the attachment pane, without stopping the agent. To keep
the attachment visible, just switch back to Central using host pane navigation.
Hold Shift while dragging to use your host terminal's text selection, then use
its normal copy shortcut.

The wrapper uses a UTF-8 locale and clients attach with `-u`. Extended keys
are enabled in the bundled config before the agent starts, but modified Enter
support still depends on the host terminal and tmux capability detection.

## Lifetime and limitations

Central retains each project's lock, run registration, broker, and invocation
files until that sandbox is stopped. Normal agent exit also triggers cleanup.
The registry remains compatible with `wisp agents --json`.

Quit with `q` or Ctrl-C. When projects are active, confirm with `y`.
Central cancels in-progress launches and cleans its own sandboxes and credential
brokers, in parallel, using bounded cleanup contexts and ownership checks.
It also closes only its own host tool/attachment panes, including failed panes;
pre-existing panes and the host tmux server are left alone. Save editor changes
before quitting Central. Stopping a sandbox with `x` does not close host editors.
SIGTERM and SIGHUP also trigger cleanup. Ctrl-C inside a tool pane or handed-off
program belongs to that program, rather than terminating every sandbox.

Central does not adopt sandboxes launched independently: their project locks
or running-container checks reject a conflicting launch. It verifies the
specific run ID before entering or cleaning its sandbox. A force-kill or host
crash cannot guarantee cleanup; this basic version does not recover/adopt live
orphaned sessions. Stop those owned resources manually before restarting.

## Live agent activity

The ACTIVITY column refreshes approximately once per second while projects are
running, including while agents work detached. It shares the verified status
collector used by `wisp agents --json`; Docker checks run in the background and
do not block keyboard input. Only the current project's unique run ID can
update its row. Normal report states are `working`, `waiting`, `idle`, `done`,
`failed`, and `unknown`, with permission/question/retry reasons when available.
Pi reports work as `working` and settled turns as `idle`; it does not invent
terminal success/failure outcomes.

Missing, invalid, or unsupported reports display `unknown` with reporter detail.
Collection errors are nonfatal: existing activity is marked `(stale)`, or unknown
activity as `(unavailable)`, until collection recovers. Reporter timestamps are
not used as liveness deadlines. `done` describes an observed turn, not proof
that the overall task succeeded. See [agent-status.md](agent-status.md).

Central delegates optional pane layout to host tmux; it is not a terminal
emulator, persistent daemon, or multi-agent-per-project manager. Container tmux
still preserves the agent terminal independently of host attachment panes.
