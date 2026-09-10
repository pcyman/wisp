package config

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

const InitialConfig = `schema_version = 1

[images]
sandbox = "wisp:local"
credentials = "wisp-credentials:local"

[build]
cpus = 4

[build.versions]
opencode = ""
hunk = ""
aws_cli = ""
kubectl = ""
helm = ""
terraform = ""
yq = ""
uv = ""
go = ""
boto3 = "1.35.99"

[opencode]
# config_path = "~/.config/opencode"

# AWS is opt-in. Uncomment this table and at least one alias to enable it.
# [aws]
# default = "development"
# host_config_path = "~/.aws/config"
# host_credentials_path = "~/.aws/credentials"
# sso_cache_path = "~/.aws/sso/cache"

# [aws.aliases.development]
# profile = "company-development" # optional; omit to use the default credential chain
# role_arn = "arn:aws:iam::123456789012:role/Wisp"
# region = "eu-west-1"
# duration_seconds = 3600
# eks_cluster = "development-cluster"

# [[mounts]]
# source = "../shared-library"
# target = "/workspace/repos/shared-library"
# mode = "ro"
`

// Init atomically creates a private config without replacing any existing
// filesystem entry. The hard-link publication gives create-if-absent semantics
// that rename alone cannot provide.
func Init(configPath string) error {
	if !filepath.IsAbs(configPath) {
		return fmt.Errorf("config path %q is not absolute", configPath)
	}
	configPath = filepath.Clean(configPath)
	if _, err := os.Lstat(configPath); err == nil {
		return fmt.Errorf("refusing to overwrite existing config path %q", configPath)
	} else if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("inspect config path %q: %w", configPath, err)
	}

	parent := filepath.Dir(configPath)
	_, parentErr := os.Stat(parent)
	parentMissing := errors.Is(parentErr, os.ErrNotExist)
	if parentErr != nil && !parentMissing {
		return fmt.Errorf("inspect config directory %q: %w", parent, parentErr)
	}
	if err := os.MkdirAll(parent, 0o700); err != nil {
		return fmt.Errorf("create config directory %q: %w", parent, err)
	}
	if parentMissing {
		if err := os.Chmod(parent, 0o700); err != nil {
			return fmt.Errorf("set config directory permissions %q: %w", parent, err)
		}
	}

	temporary, err := os.CreateTemp(parent, ".config.toml.tmp-")
	if err != nil {
		return fmt.Errorf("create temporary config: %w", err)
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)
	closeTemporary := func() {
		_ = temporary.Close()
	}
	defer closeTemporary()
	if err := temporary.Chmod(0o600); err != nil {
		return fmt.Errorf("set temporary config permissions: %w", err)
	}
	if _, err := temporary.WriteString(InitialConfig); err != nil {
		return fmt.Errorf("write temporary config: %w", err)
	}
	if err := temporary.Sync(); err != nil {
		return fmt.Errorf("sync temporary config: %w", err)
	}
	if err := temporary.Close(); err != nil {
		return fmt.Errorf("close temporary config: %w", err)
	}
	if err := os.Link(temporaryPath, configPath); err != nil {
		if errors.Is(err, os.ErrExist) {
			return fmt.Errorf("refusing to overwrite existing config path %q", configPath)
		}
		return fmt.Errorf("publish config %q atomically: %w", configPath, err)
	}
	if directory, err := os.Open(parent); err == nil {
		syncErr := directory.Sync()
		closeErr := directory.Close()
		if syncErr != nil {
			return fmt.Errorf("sync config directory: %w", syncErr)
		}
		if closeErr != nil {
			return fmt.Errorf("close config directory: %w", closeErr)
		}
	}
	return nil
}
