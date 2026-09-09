package cli

const rootHelp = `Usage:
  wisp [RUN_OPTIONS] [DIRECTORY]
  wisp COMMAND [ARGUMENTS]

Commands:
  agents    List or watch running Wisp agents as JSON
  run       Launch OpenCode in the sandbox
  herdr     Launch OpenCode with Herdr integration
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
  --aws ALIAS       Select a configured AWS alias
  --rebuild         Rebuild both images
  --mount DIRECTORY Add a read-only repository mount (repeatable)
  --mount-rw DIR    Add a read-write repository mount (repeatable)
  --config FILE     Use a non-default config file
  -h, --help        Show run help
`,
	CommandHerdr: `Usage:
  wisp herdr [RUN_OPTIONS] [DIRECTORY]

Launch the normal Wisp OpenCode sandbox while identifying the host-side Wisp
process to Herdr as an OpenCode agent. Herdr is not exposed inside the sandbox.
Run options are identical to "wisp run"; see "wisp help run".
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
