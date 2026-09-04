# Wisp

Wisp runs [OpenCode](https://opencode.ai/) in a small Docker sandbox for the
current project. The project is mounted read-write, while the container home is
ephemeral. AWS access is supplied through a separate credential broker using a
configured AWS SSO profile and role.

## Requirements

- Linux or macOS
- A local Docker daemon or Docker Desktop
- Docker 20.10+, Compose 2.24+, and Buildx 0.11+ for image builds
- AWS CLI with an SSO profile
- Go 1.23+ when building from source

Remote Docker daemons and Windows are not supported.

## Install

Build from source and place the resulting binary on your `PATH`:

```sh
go build -trimpath -o wisp ./cmd/wisp
```

## Get started

Create the config, uncomment and edit the generated AWS alias, then validate it:

```sh
wisp config init
${EDITOR:-vi} "$(wisp config path)"
wisp config validate
```

Authenticate the configured profile and verify broker access:

```sh
aws sso login --profile company-development
wisp aws check development
```

Launch OpenCode for the current directory:

```sh
wisp
```

Wisp uses `$XDG_CONFIG_HOME/wisp/config.toml`, falling back to
`$HOME/.config/wisp/config.toml`. Host OpenCode config and authentication are
mounted read-only when present.

## Usage

```text
wisp [OPTIONS] [DIRECTORY]       Launch OpenCode
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
