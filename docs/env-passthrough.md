# Host environment passthrough

Add a top-level allowlist to the Wisp config, before any TOML table headers:

```toml
schema_version = 1
env_passthrough = ["GITHUB_TOKEN", "MY_API_KEY"]
```

For each new sandbox, Wisp copies the listed variables from the environment of
its host process into the selected agent container (OpenCode or Pi). Export them
before launching Wisp. Unset variables are omitted; explicitly empty values stay
empty. Values are copied literally, including dollar signs, spaces, quotes, and
newlines. Changing the host environment does not update an already-running
sandbox. The allowlist defaults to empty.

Names must match `[A-Za-z_][A-Za-z0-9_]*`, without duplicates. Wisp rejects names
that could override its managed settings:

- Prefixes `WISP_`, `AWS_`, `DOCKER_`, `COMPOSE_`, `GIT_CONFIG_`, and
  `PI_CODING_AGENT_`.
- `HOME`, `PATH`, `TMPDIR`, `XDG_DATA_HOME`, `KUBECONFIG`, `AZURE_CONFIG_DIR`,
  `OPENCODE_CONFIG`, `OPENCODE_CONFIG_DIR`, `OPENCODE_CONFIG_CONTENT`, `PI_TIMING`,
  `TERM`, and `COLORTERM`.
- Git metadata/discovery overrides: `GIT_DIR`, `GIT_WORK_TREE`, `GIT_COMMON_DIR`,
  `GIT_INDEX_FILE`, `GIT_OBJECT_DIRECTORY`, `GIT_ALTERNATE_OBJECT_DIRECTORIES`,
  `GIT_CEILING_DIRECTORIES`, and `GIT_DISCOVERY_ACROSS_FILESYSTEM`.
- Build-version variables: `OPENCODE_VERSION`, `PI_VERSION`, `HUNK_VERSION`,
  `KUBECTL_VERSION`, `HELM_VERSION`, `TERRAFORM_VERSION`, `YQ_VERSION`,
  `UV_VERSION`, `GO_VERSION`, and `BOTO3_VERSION`.

AWS access remains broker-only. Use Wisp's AWS aliases rather than forwarding
host AWS credentials. Existing terminal and Azure-login forwarding is unchanged.

## Security

Forwarded values are accessible to the agent and anything it runs. Only allow
variables you trust the sandbox to receive. They are not forwarded to the
credential broker and are removed from run-scoped host Git and Docker/Compose
subprocess environments.

Wisp writes the values to its existing private per-invocation Compose override
(directory mode `0700`, file mode `0600`) and removes it during normal or
interrupted cleanup. Abrupt termination such as SIGKILL can leave this private
file behind. Values also become part of Docker's container configuration, so
users with Docker access can inspect them while the container exists.

Wisp redacts non-empty passthrough values in captured launch/cleanup errors and
broker diagnostics. It does not filter the interactive agent's output; the
agent or its tools may print values. Do not store secret values in the TOML
config itself—only variable names.
