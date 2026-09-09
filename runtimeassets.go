package wisp

import "embed"

// RuntimeAssets contains the immutable Docker runtime distributed with the CLI.
//
//go:embed compose.yaml Dockerfile .dockerignore container/entrypoint.sh container/agent-status.js container/opencode.json credentials/Dockerfile credentials/broker.py credentials/.dockerignore
var RuntimeAssets embed.FS
