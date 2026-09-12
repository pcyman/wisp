# Wisp contributor notes

Wisp is a Go CLI that runs OpenCode in a Docker Compose sandbox. AWS access is
always provided by the Python credential broker; Docker is invoked through the
CLI rather than an SDK.

## Code map

- `cmd/wisp`: executable wiring
- `internal/cli`: argument parsing and help
- `internal/app`: planning and command lifecycles
- `internal/config`, `project`, `mount`: host-input validation
- `internal/docker`, `process`, `lock`: Docker commands, subprocesses, locks
- `internal/runtimeassets`: embedded asset materialization
- `internal/agent`: the OpenCode boundary
- `internal/agentstatus`, `internal/app/agents.go`: run registry and status snapshots
- `container/agent-status.js`, `container/opencode.json`: OpenCode status reporter
- `credentials/broker.py`: AWS credential broker
- `compose.yaml`, both Dockerfiles, and `container/entrypoint.sh`: embedded
  runtime assets

## Invariants

- Validate and resolve all host input before Docker side effects.
- Pass subprocess arguments directly; never construct shell command strings.
- OpenCode and Pi are the supported agents. Run arguments must never become
  agent arguments or user-selected container commands.
- Never mount the Docker socket, the whole host home, or host AWS files into
  the sandbox. OpenCode config and auth mounts stay read-only.
- AWS config and SSO data are broker-only. Never log credentials, broker tokens,
  or environment dumps that may contain them.
- Resolve bind sources physically, disable implicit host-path creation, and
  reject mount collisions.
- Verify Wisp ownership labels before entering or deleting containers.
- Preserve child exit status when cleanup also fails, and clean detached
  Compose resources on normal and interrupted exits.
- Keep help, version, config commands, `exec`, and `hunk` independent of runtime
  asset materialization where applicable.
- Do not add legacy environment configuration, project-local Wisp configuration,
  direct/no-AWS modes, Codex support, or remote-Docker assumptions.

Runtime assets are embedded into the Go binary. Rebuild the binary after asset
changes, and use `wisp --rebuild` when existing image tags must be refreshed.

## Agent status

See `docs/agent-status.md` for the JSON contract and lifecycle details. Keep the
README brief; put feature references in `docs/` and command syntax in CLI help.

- One reporter writes one atomic `agent.json` per sandbox. Keep it independent
  of Docker and desktop concepts; Wisp owns host metadata and aggregation.
- Mount only the status directory writable, never host registration metadata.
  Treat reports as untrusted input with bounded, no-follow reads.
- Determine sandbox liveness from running containers and verified ownership,
  project, and unique run-ID labels, not file presence or timestamps.
- Do not infer task success from idle events or terminal failure from tool
  errors. Preserve conservative root/child session aggregation.
- Keep `agents --json` independent of config loading and asset materialization.
- Load the dependency-free reporter through the bundled explicit config file.
  Avoid adding a fresh `OPENCODE_CONFIG_DIR`: OpenCode installs directory
  dependencies at startup, even when the reporter needs none.

## Validation

```sh
gofmt -w cmd internal runtimeassets.go
go test -race ./...
go vet ./...
go build ./cmd/wisp
python -m unittest discover -s credentials -p 'test_*.py'
node --test container/agent-status.test.mjs
node --test container/pi-agent-status.test.mjs
bash -n container/entrypoint.sh
git diff --check
```

When Docker is available, validate Compose and build both images using
non-secret placeholder values for its required interpolation variables.
