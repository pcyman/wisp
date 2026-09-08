# Wisp

Wisp runs [OpenCode](https://opencode.ai/) in a small Docker sandbox for the
current project. The project is mounted read-write, while the container home is
ephemeral except for project-scoped OpenCode session data. AWS access is opt-in
and, when configured, is supplied through a separate credential broker that
assumes a configured role.

## Requirements

- Linux or macOS
- A local Docker daemon or Docker Desktop
- Docker 20.10+, Compose 2.24+, and Buildx 0.11+ for image builds
- AWS CLI when the optional AWS source-credential workflow requires it (for example, SSO login)
- Go 1.23+ when building from source

Remote Docker daemons and Windows are not supported.

## Install

Build from source and place the resulting binary on your `PATH`:

```sh
go build -trimpath -o ~/.local/bin/wisp ./cmd/wisp
```

## Get started

Create and validate the config:

```sh
wisp config init
${EDITOR:-vi} "$(wisp config path)"
wisp config validate
```

AWS and the EKS kubeconfig integration are disabled unless the config contains
an `[aws]` table with at least one alias. To enable them, configure a role:

```toml
[aws]
default = "development"

[aws.aliases.development]
role_arn = "arn:aws:iam::123456789012:role/Wisp"
# profile = "company-development" # optional
region = "eu-west-1"
# eks_cluster = "development-cluster"
```

When `profile` is set, Wisp uses that profile as the source identity for
`AssumeRole`. When it is omitted, boto3 uses its default credential chain.
Wisp makes available host environment credentials and any existing default
`~/.aws/config`, `~/.aws/credentials`, and `~/.aws/sso/cache` inputs to the
broker only. Their paths can be overridden with `host_config_path`,
`host_credentials_path`, and `sso_cache_path`; default paths that do not exist
are simply ignored.

For an SSO-backed profile, authenticate first, then verify broker access:

```sh
aws sso login --profile company-development
wisp aws check development
```

Launch OpenCode for the current directory:

```sh
wisp
```

Launch Wisp inside [Herdr](https://herdr.dev/):

```sh
wisp herdr
```

This launches the normal OpenCode sandbox while identifying the host-side Wisp
process to Herdr as an OpenCode agent. Herdr itself is not exposed inside the
sandbox. The `herdr` command name is reserved; use `wisp ./herdr` or
`wisp run herdr` to launch a directory named `herdr`.

Wisp uses `$XDG_CONFIG_HOME/wisp/config.toml`, falling back to
`$HOME/.config/wisp/config.toml`. Host OpenCode config and authentication are
mounted read-only when present. Wisp also mounts only the host Hunk
`config.toml` read-only from `$XDG_CONFIG_HOME/hunk` (or
`$HOME/.config/hunk`), leaving Hunk state files unmounted. OpenCode session
data is persisted separately for each project under
`$XDG_DATA_HOME/wisp/projects`, falling back to `$HOME/.local/share/wisp/projects`.

## Usage

```text
wisp [RUN_OPTIONS] [DIRECTORY]   Launch OpenCode
wisp herdr [RUN_OPTIONS] [DIRECTORY]
wisp exec [DIRECTORY] -- COMMAND
wisp hunk [DIRECTORY] [-- ARGS]
wisp aws check [ALIAS]
wisp doctor
```

Common run options:

```text
--aws ALIAS       Select an AWS alias
--rebuild         Rebuild runtime images
--mount DIR       Add a read-only repository mount
--mount-rw DIR    Add a read-write repository mount
--config FILE     Use another config file
```

Run `wisp help` or `wisp help COMMAND` for the full command syntax.
