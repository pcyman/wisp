# Wisp Central

Central is a host-side terminal switchboard for on-demand project sandboxes.
It requires an interactive terminal, but does not require host tmux.

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
They also start the sandbox if the project is closed. Central suspends its
screen and terminal input while another interactive program has control.
Exiting the tool returns to Central. Missing tools are reported in the UI.

Filter by name or path with `/`; Enter finishes editing the filter and Escape
clears it. `l` toggles a bounded tail of the selected project's startup logs.
`x` stops a project; it can be reopened after cleanup completes.

## Agent terminals

Each sandbox runs one agent inside one bundled tmux session. Attachment uses
`docker exec -it … tmux -u attach-session`, not `docker attach`. Tmux owns the
persistent agent terminal and redraws it when clients reconnect or resize.

Detach with **Ctrl-b, then d**. Inside host tmux with the default prefix, use
**Ctrl-b twice, then d** to reach the inner tmux. Hold Shift while dragging to
use your host terminal's text selection, then use its normal copy shortcut.

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
SIGTERM and SIGHUP also trigger cleanup. Ctrl-C inside a handed-off program
belongs to that program, rather than terminating every sandbox.

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

This version is deliberately not a terminal-pane compositor, persistent daemon,
or multi-agent-per-project manager.
