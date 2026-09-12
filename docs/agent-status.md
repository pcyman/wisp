# Agent status

`wisp agents --json` returns a one-shot snapshot of this user's verified running
Wisp sandboxes, sorted by repository path and then run ID:

```json
{
  "schema_version": 1,
  "agents": [
    {
      "id": "e71bb1de471cb700cff0d8aaf425cf82",
      "sandbox_id": "wisp-1000-project-0123456789ab",
      "repo": "/home/user/project",
      "agent": "opencode",
      "state": "waiting",
      "reason": "question",
      "reporter": "ready",
      "updated_at": "2026-09-09T12:00:00Z",
      "host_process": { "pid": 12345 }
    }
  ]
}
```

`id` identifies a unique launch; `sandbox_id` is the stable sandbox container
name for that project. An empty result is `{"schema_version":1,"agents":[]}`.

`host_process.pid` is the host PID of the Wisp process that launched the sandbox,
recorded at registration, not the PID of `wisp agents` or its watcher. Older
registrations without a PID remain valid and omit `host_process`. The PID is
advisory: processes can exit and PIDs can be reused. Consumers must validate that
the PID still identifies the expected launching Wisp process before using it for
process ancestry. It is not a liveness authority; running containers and verified
ownership, project, and unique run-ID labels remain authoritative.

The command requires `--json`, does not load project or user configuration, and
does not materialize runtime assets. It does not contact Docker when there are
no valid registry entries. Errors go to stderr with a nonzero exit status.

`wisp agents --json --watch` (also `wisp agents --watch --json`) emits full
snapshots with the identical schema as newline-delimited JSON (JSONL). It emits
the initial snapshot immediately, then polls every second and emits only when
the snapshot changes, including timestamps or removal of all agents. An empty
snapshot is emitted initially when no agents are running and whenever the last
agent disappears. There are no deltas, heartbeats, or extra watch-only fields.

Each collection, in either mode, has a 10-second context timeout bounding its
Docker calls. Collection failures, including Docker errors and timeouts, end the
command with a nonzero status and an error on stderr; they never produce a
fabricated empty snapshot. Stdout write failures also exit nonzero. Watch exits
cleanly on context cancellation (including an interrupt), without a final
snapshot. Watch has the same configuration and asset independence as one-shot.

States are advisory summaries of observed agent events, not process health:

- `idle`: no observed active work or pending input; also used after cancellation
  or when no terminal outcome can be established.
- `working`: observed work is active, including retries.
- `waiting`: an observed permission request or question needs a response.
- `done`: the latest observed root turn ended normally; this is not proof that
  the overall task succeeded.
- `failed`: the observed root-session work ended with an assistant failure.
- `unknown`: the reporter is missing, invalid, or uses an unsupported schema.

`reporter` is `ready` for a valid schema-1 report, `missing` when the report file
or status directory is absent, `invalid` for unsafe, unreadable, malformed, or
invalid report data, and `unsupported` for an unrecognized positive integer
schema version. Only schema incompatibility produces `unsupported`; an unknown
state is invalid. `reason`, when present, is limited to `permission`, `question`,
or `retry`. `updated_at` is the report's timestamp, not a heartbeat or liveness
deadline. Unusable reports omit both optional fields. No prompts, summaries,
error text, or internal ownership metadata are returned.

Each launch has one plugin writer and one atomically replaced current-status
file. The reporter observes live events only: it does not replay session history
or retain historical snapshots. Resumed sessions may remain ambiguous until
their identity and activity are observed. There is no desktop UI.

The host-only registry is under Wisp's private runtime root, normally
`$XDG_RUNTIME_DIR/wisp/agents/<run-id>` or the private `wisp-<uid>` directory in
the temporary directory. `metadata.json` stays on the host; only `status/` is
mounted read-write at `/run/wisp/agent-status`, where the plugin writes
`agent.json`. The trusted host registration records whether the harness is
`opencode` or `pi`; reports cannot choose or change that identity. Host OpenCode
configuration and authentication remain read-only. A selected Pi profile is
shared read-write, while its sessions and trust state are project-scoped.
Normal and interrupted cleanup removes the registration. Files left after an
abrupt exit do not establish liveness: listing requires a running Docker
container with matching ownership, project, and unique run-ID labels. Stale
registrations cannot match a later launch reusing the same sandbox name.

The static `container/opencode.json` is loaded via `OPENCODE_CONFIG` and references
the image's JavaScript plugin at `file:///usr/local/share/wisp/agent-status.js`.
Wisp does not use `OPENCODE_CONFIG_DIR`, avoiding an extra dependency install on
each start.

Pi loads `/usr/local/share/wisp/pi-agent-status.mjs` as an explicit CLI
extension. It reports `working` from `agent_start`, returns to `idle` only at
`agent_settled`, and reports extension UI prompts as `waiting`. Pi's public
extension API does not expose exact retry state or a conservative terminal
success/failure signal, so the Pi reporter does not manufacture those states.

Runtime assets, including the reporter plugin, are embedded in the Wisp binary.
After updating them, rebuild the binary using the
[install instructions](../README.md#install), then launch with `wisp --rebuild`
to refresh existing runtime image tags. Rebuilding only the images with an old
binary does not pick up new embedded assets.
