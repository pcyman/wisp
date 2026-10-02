package cli

const rootHelp = `Usage:
  wisp [RUN_OPTIONS] [DIRECTORY]
  wisp COMMAND [ARGUMENTS]

Commands:
  central   Manage project sandboxes in a terminal control room
  agents    List or watch running Wisp agents as JSON
  run       Launch the selected agent in the sandbox
  exec      Execute a command in a running sandbox
  hunk      Run Hunk in a running sandbox
  config    Initialize, locate, or validate configuration
  aws       Check AWS broker credentials
  doctor    Diagnose the local Wisp environment
  version   Print the Wisp version
  help      Show help for a command

Use "wisp help COMMAND" for command help.
`

var commandHelp = map[Command]string{
	CommandCentral: `Usage:
  wisp central [--config FILE]

Configure [[central.projects]] entries with name and path in the global config.
Requires an interactive terminal. Projects launch on demand in background tmux
sessions. Central stops its own sandboxes on quit; images are retained.

Keys:
  j/k         Move (also arrows); / filter; Escape clear filter
  Enter       Open a stopped project or attach to its running agent
  o           Open project (launch its sandbox asynchronously)
  e/g         Open host nvim/lazygit; also launch the sandbox if stopped
  x           Stop selected sandbox
  l           Toggle selected project startup logs
  q/Ctrl-C    Quit (confirm with y when projects are active)

Inside host tmux (3.2+), all tools share one right-hand pane, with no splits.
Repeating an open focuses the visible tool. Switching keeps sandbox agents
running, but closes the previous host tool; save editor changes before switching.
Use host pane navigation to return to Central. Quit closes only Central's own
tool pane and sandboxes; save editor changes first.
Outside host tmux, tools use the full terminal; detach an agent with Ctrl-b, d.
Inside host tmux, Ctrl-b twice, d detaches the inner agent session.
See docs/central.md for configuration, lifecycle, and terminal limitations.
`,
	CommandAgents: `Usage:
  wisp agents --json [--watch]

Lists verified running sandboxes. Reporter timestamps are advisory, not liveness.
--json is required. --watch emits a full JSONL snapshot immediately, then only
when changed, polling every second. Cancellation exits cleanly; collection or
stdout errors exit nonzero. Each collection has a 10-second timeout.
`,
	CommandRun: `Usage:
  wisp [RUN_OPTIONS] [DIRECTORY]
  wisp run [RUN_OPTIONS] [DIRECTORY]

Options:
  --agent NAME     Select opencode or pi for this run
  --aws ALIAS       Select a configured AWS alias
  --rebuild         Rebuild both images
  --mount DIRECTORY Add a read-only repository mount (repeatable)
  --mount-rw DIR    Add a read-write repository mount (repeatable)
  --config FILE     Use a non-default config file
  -h, --help        Show run help
`,
	CommandExec: `Usage:
  wisp exec [DIRECTORY] -- COMMAND [ARG...]
`,
	CommandHunk: `Usage:
  wisp hunk [DIRECTORY] [-- HUNK_ARG...]

With no Hunk arguments, runs "hunk diff --watch".
`,
	CommandConfig: `Usage:
  wisp config init [--config FILE]
  wisp config path [--config FILE]
  wisp config validate [--config FILE]
`,
	CommandAWS: `Usage:
  wisp aws check [--config FILE] [--rebuild] [ALIAS]
`,
	CommandDoctor: `Usage:
  wisp doctor [--config FILE]
`,
	CommandVersion: `Usage:
  wisp version
  wisp --version
`,
	CommandHelp: `Usage:
  wisp help [COMMAND]
`,
}

// Help returns stable help text for a top-level command.
func Help(command Command) string {
	if command == CommandRoot {
		return rootHelp
	}
	if text, ok := commandHelp[command]; ok {
		return text
	}
	return rootHelp
}
