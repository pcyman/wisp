package config

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

var (
	aliasNamePattern = regexp.MustCompile(`^[A-Za-z0-9._-]+$`)
	eksNamePattern   = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]{0,99}$`)
)

// Resolve applies defaults and performs all path-independent validation.
// Relative path strings remain unresolved until ValidateHostPaths.
func Resolve(raw RawConfig) (Config, error) {
	if raw.SchemaVersion == nil {
		return Config{}, errors.New("schema_version is required")
	}
	if *raw.SchemaVersion != SchemaVersion {
		return Config{}, fmt.Errorf("schema_version must be %d, got %d", SchemaVersion, *raw.SchemaVersion)
	}

	cfg := Config{
		SchemaVersion: SchemaVersion,
		Images: ImageConfig{
			Sandbox:     DefaultSandboxImage,
			Credentials: DefaultCredentialsImage,
		},
		Build: BuildConfig{
			CPUs:     DefaultBuildCPUs,
			Versions: VersionConfig{Boto3: DefaultBoto3Version},
		},
		AWS: AWSConfig{Aliases: make(map[string]AWSAliasConfig)},
	}
	if raw.Images != nil {
		setString(&cfg.Images.Sandbox, raw.Images.Sandbox)
		setString(&cfg.Images.Credentials, raw.Images.Credentials)
	}
	if strings.TrimSpace(cfg.Images.Sandbox) == "" {
		return Config{}, errors.New("images.sandbox must not be empty")
	}
	if strings.TrimSpace(cfg.Images.Credentials) == "" {
		return Config{}, errors.New("images.credentials must not be empty")
	}

	if raw.Build != nil {
		if raw.Build.CPUs != nil {
			cfg.Build.CPUs = *raw.Build.CPUs
		}
		if raw.Build.Versions != nil {
			versions := raw.Build.Versions
			setString(&cfg.Build.Versions.OpenCode, versions.OpenCode)
			setString(&cfg.Build.Versions.Hunk, versions.Hunk)
			setString(&cfg.Build.Versions.AWSCLI, versions.AWSCLI)
			setString(&cfg.Build.Versions.Kubectl, versions.Kubectl)
			setString(&cfg.Build.Versions.Helm, versions.Helm)
			setString(&cfg.Build.Versions.Terraform, versions.Terraform)
			setString(&cfg.Build.Versions.Go, versions.Go)
			setString(&cfg.Build.Versions.Boto3, versions.Boto3)
		}
	}
	if cfg.Build.CPUs <= 0 {
		return Config{}, errors.New("build.cpus must be positive")
	}
	if strings.TrimSpace(cfg.Build.Versions.Boto3) == "" {
		return Config{}, errors.New("build.versions.boto3 must not be empty")
	}

	if raw.OpenCode != nil && raw.OpenCode.ConfigPath != nil {
		if *raw.OpenCode.ConfigPath == "" {
			return Config{}, errors.New("opencode.config_path must not be empty when set")
		}
		cfg.OpenCode.ConfigPath = *raw.OpenCode.ConfigPath
		cfg.OpenCode.ConfigPathExplicit = true
	}

	if raw.AWS != nil {
		if raw.AWS.Default != nil {
			cfg.AWS.Default = *raw.AWS.Default
			if cfg.AWS.Default == "" {
				return Config{}, errors.New("aws.default must not be empty when set")
			}
		}
		if raw.AWS.HostConfigPath != nil {
			cfg.AWS.HostConfigPath = *raw.AWS.HostConfigPath
			cfg.AWS.HostConfigPathExplicit = true
			if cfg.AWS.HostConfigPath == "" {
				return Config{}, errors.New("aws.host_config_path must not be empty when set")
			}
		}
		if raw.AWS.SSOCachePath != nil {
			cfg.AWS.SSOCachePath = *raw.AWS.SSOCachePath
			cfg.AWS.SSOCachePathExplicit = true
			if cfg.AWS.SSOCachePath == "" {
				return Config{}, errors.New("aws.sso_cache_path must not be empty when set")
			}
		}
		aliasNames := sortedRawAliasNames(raw.AWS.Aliases)
		for _, name := range aliasNames {
			alias, err := resolveAlias(name, raw.AWS.Aliases[name])
			if err != nil {
				return Config{}, err
			}
			cfg.AWS.Aliases[name] = alias
		}
	}
	if len(cfg.AWS.Aliases) == 0 {
		return Config{}, errors.New("aws.aliases must contain at least one alias")
	}
	if cfg.AWS.Default != "" {
		if _, ok := cfg.AWS.Aliases[cfg.AWS.Default]; !ok {
			return Config{}, fmt.Errorf("aws.default %q does not name a configured alias", cfg.AWS.Default)
		}
	}

	cfg.Mounts = make([]MountConfig, 0, len(raw.Mounts))
	for i, mount := range raw.Mounts {
		resolved, err := resolveMount(i, mount)
		if err != nil {
			return Config{}, err
		}
		cfg.Mounts = append(cfg.Mounts, resolved)
	}
	if err := validateMountTargets(cfg.Mounts); err != nil {
		return Config{}, err
	}
	return cfg, nil
}

func resolveAlias(name string, raw RawAWSAliasConfig) (AWSAliasConfig, error) {
	if !aliasNamePattern.MatchString(name) {
		return AWSAliasConfig{}, fmt.Errorf("aws alias name %q must match %s", name, aliasNamePattern)
	}
	if raw.Profile == nil || strings.TrimSpace(*raw.Profile) == "" {
		return AWSAliasConfig{}, fmt.Errorf("aws.aliases.%s.profile is required and must not be empty", name)
	}
	if raw.RoleARN == nil || !strings.HasPrefix(*raw.RoleARN, "arn:") {
		return AWSAliasConfig{}, fmt.Errorf("aws.aliases.%s.role_arn is required and must begin with %q", name, "arn:")
	}
	alias := AWSAliasConfig{
		Profile:         *raw.Profile,
		RoleARN:         *raw.RoleARN,
		DurationSeconds: DefaultAWSDuration,
	}
	if raw.DurationSeconds != nil {
		alias.DurationSeconds = *raw.DurationSeconds
	}
	if alias.DurationSeconds < 900 || alias.DurationSeconds > 43200 {
		return AWSAliasConfig{}, fmt.Errorf("aws.aliases.%s.duration_seconds must be between 900 and 43200", name)
	}
	if raw.Region != nil {
		alias.Region = *raw.Region
		if strings.TrimSpace(alias.Region) == "" {
			return AWSAliasConfig{}, fmt.Errorf("aws.aliases.%s.region must not be empty when set", name)
		}
	}
	if raw.EKSCluster != nil {
		alias.EKSCluster = *raw.EKSCluster
		if !eksNamePattern.MatchString(alias.EKSCluster) {
			return AWSAliasConfig{}, fmt.Errorf("aws.aliases.%s.eks_cluster is invalid", name)
		}
		if alias.Region == "" {
			return AWSAliasConfig{}, fmt.Errorf("aws.aliases.%s.region is required when eks_cluster is set", name)
		}
	}
	return alias, nil
}

func resolveMount(index int, raw RawMountConfig) (MountConfig, error) {
	prefix := fmt.Sprintf("mounts[%d]", index)
	if raw.Source == nil || *raw.Source == "" {
		return MountConfig{}, fmt.Errorf("%s.source is required and must not be empty", prefix)
	}
	if raw.Target == nil || *raw.Target == "" {
		return MountConfig{}, fmt.Errorf("%s.target is required and must not be empty", prefix)
	}
	mode := "ro"
	if raw.Mode != nil {
		mode = *raw.Mode
	}
	if mode != "ro" && mode != "rw" {
		return MountConfig{}, fmt.Errorf("%s.mode must be %q or %q", prefix, "ro", "rw")
	}
	return MountConfig{Source: *raw.Source, Target: *raw.Target, Mode: mode}, nil
}

func validateMountTargets(mounts []MountConfig) error {
	for i, mount := range mounts {
		if strings.IndexByte(mount.Target, 0) >= 0 {
			return fmt.Errorf("mounts[%d].target contains a NUL byte", i)
		}
		if !path.IsAbs(mount.Target) || path.Clean(mount.Target) != mount.Target || mount.Target == "/workspace/repos" || !strings.HasPrefix(mount.Target, "/workspace/repos/") {
			return fmt.Errorf("mounts[%d].target %q must be a clean absolute descendant of /workspace/repos", i, mount.Target)
		}
		for j := 0; j < i; j++ {
			other := mounts[j].Target
			if mount.Target == other || strings.HasPrefix(mount.Target, other+"/") || strings.HasPrefix(other, mount.Target+"/") {
				return fmt.Errorf("mount targets %q and %q overlap", other, mount.Target)
			}
		}
	}
	return nil
}

// SelectAWSAlias applies requested, configured-default, then sole-alias
// precedence. It never relies on map iteration order.
func SelectAWSAlias(cfg Config, requested string) (string, error) {
	if requested != "" {
		if _, ok := cfg.AWS.Aliases[requested]; !ok {
			return "", fmt.Errorf("AWS alias %q is not configured", requested)
		}
		return requested, nil
	}
	if cfg.AWS.Default != "" {
		return cfg.AWS.Default, nil
	}
	if len(cfg.AWS.Aliases) == 1 {
		for name := range cfg.AWS.Aliases {
			return name, nil
		}
	}
	return "", errors.New("multiple AWS aliases are configured; configure aws.default or pass --aws ALIAS")
}

// ValidateHostPaths resolves all consumed paths to existing physical paths.
// It mutates cfg only after each individual path has passed its checks.
func ValidateHostPaths(cfg *Config, configPath string, env Environment) ([]Warning, error) {
	configDir := filepath.Dir(configPath)
	for i := range cfg.Mounts {
		candidate, err := resolveTOMLPath(cfg.Mounts[i].Source, configDir, env.Home)
		if err != nil {
			return nil, fmt.Errorf("mounts[%d].source: %w", i, err)
		}
		physical, err := requirePath(candidate, pathDirectory)
		if err != nil {
			return nil, fmt.Errorf("mounts[%d].source: %w", i, err)
		}
		cfg.Mounts[i].Source = physical
	}

	if cfg.AWS.HostConfigPath == "" {
		if err := requireAbsoluteHome(env.Home); err != nil {
			return nil, fmt.Errorf("resolve default AWS config path: %w", err)
		}
		cfg.AWS.HostConfigPath = filepath.Join(env.Home, ".aws", "config")
	} else {
		resolved, err := resolveTOMLPath(cfg.AWS.HostConfigPath, configDir, env.Home)
		if err != nil {
			return nil, fmt.Errorf("aws.host_config_path: %w", err)
		}
		cfg.AWS.HostConfigPath = resolved
	}
	physical, err := requirePath(cfg.AWS.HostConfigPath, pathRegularFile)
	if err != nil {
		return nil, fmt.Errorf("AWS config path %q: %w; configure aws.host_config_path and run aws sso login --profile PROFILE", cfg.AWS.HostConfigPath, err)
	}
	cfg.AWS.HostConfigPath = physical

	if cfg.AWS.SSOCachePath == "" {
		if err := requireAbsoluteHome(env.Home); err != nil {
			return nil, fmt.Errorf("resolve default AWS SSO cache path: %w", err)
		}
		cfg.AWS.SSOCachePath = filepath.Join(env.Home, ".aws", "sso", "cache")
	} else {
		resolved, err := resolveTOMLPath(cfg.AWS.SSOCachePath, configDir, env.Home)
		if err != nil {
			return nil, fmt.Errorf("aws.sso_cache_path: %w", err)
		}
		cfg.AWS.SSOCachePath = resolved
	}
	physical, err = requirePath(cfg.AWS.SSOCachePath, pathDirectory)
	if err != nil {
		return nil, fmt.Errorf("AWS SSO cache path %q: %w; configure aws.sso_cache_path and run aws sso login --profile PROFILE", cfg.AWS.SSOCachePath, err)
	}
	cfg.AWS.SSOCachePath = physical

	warnings := make([]Warning, 0, 2)
	if cfg.OpenCode.ConfigPathExplicit {
		resolved, err := resolveTOMLPath(cfg.OpenCode.ConfigPath, configDir, env.Home)
		if err != nil {
			return nil, fmt.Errorf("opencode.config_path: %w", err)
		}
		physical, err := requirePath(resolved, pathDirectory)
		if err != nil {
			return nil, fmt.Errorf("opencode.config_path %q: %w", resolved, err)
		}
		cfg.OpenCode.ConfigPath = physical
	} else {
		base, err := xdgBase(env.XDGConfigHome, env.Home, ".config", "XDG_CONFIG_HOME")
		if err != nil {
			if env.XDGConfigHome != "" || env.Home != "" {
				return nil, fmt.Errorf("resolve default OpenCode config path: %w", err)
			}
			warnings = append(warnings, Warning("OpenCode config path is unavailable: "+err.Error()))
		} else {
			candidate := filepath.Join(base, "opencode")
			physical, pathErr := requirePath(candidate, pathDirectory)
			if pathErr != nil {
				if errors.Is(pathErr, os.ErrNotExist) {
					warnings = append(warnings, Warning(fmt.Sprintf("OpenCode config directory %q does not exist; continuing with container defaults", candidate)))
				} else {
					return nil, fmt.Errorf("OpenCode config path %q: %w", candidate, pathErr)
				}
			} else {
				cfg.OpenCode.ConfigPath = physical
			}
		}
	}

	dataBase, err := xdgBase(env.XDGDataHome, env.Home, filepath.Join(".local", "share"), "XDG_DATA_HOME")
	if err != nil {
		if env.XDGDataHome != "" || env.Home != "" {
			return nil, fmt.Errorf("resolve OpenCode auth path: %w", err)
		}
		warnings = append(warnings, Warning("OpenCode auth path is unavailable: "+err.Error()))
		return warnings, nil
	}
	authPath := filepath.Join(dataBase, "opencode", "auth.json")
	physical, err = requirePath(authPath, pathRegularFile)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			warnings = append(warnings, Warning(fmt.Sprintf("OpenCode auth file %q does not exist; authenticate on the host to persist login", authPath)))
			return warnings, nil
		}
		return nil, fmt.Errorf("OpenCode auth path %q: %w", authPath, err)
	}
	cfg.OpenCode.AuthPath = physical
	return warnings, nil
}

// HostPathDiagnostic describes one independently checked host path. A warning
// is used only for optional default OpenCode paths; required paths use Err.
type HostPathDiagnostic struct {
	Path    string
	Warning Warning
	Err     error
}

// HostPathDiagnostics contains independent path results for diagnostic
// commands that must continue after one host path fails.
type HostPathDiagnostics struct {
	Mounts         []HostPathDiagnostic
	AWSConfig      HostPathDiagnostic
	AWSSSOCache    HostPathDiagnostic
	OpenCodeConfig HostPathDiagnostic
	OpenCodeAuth   HostPathDiagnostic
}

// DiagnoseHostPaths checks all configured host paths without mutating cfg or
// the filesystem. Resolve must have validated cfg before this is called.
func DiagnoseHostPaths(cfg Config, configPath string, env Environment) HostPathDiagnostics {
	configDir := filepath.Dir(configPath)
	result := HostPathDiagnostics{Mounts: make([]HostPathDiagnostic, len(cfg.Mounts))}
	for i, configured := range cfg.Mounts {
		candidate, err := resolveTOMLPath(configured.Source, configDir, env.Home)
		if err == nil {
			result.Mounts[i].Path = candidate
			result.Mounts[i].Path, err = requirePath(candidate, pathDirectory)
		}
		result.Mounts[i].Err = err
	}

	awsConfig := cfg.AWS.HostConfigPath
	if awsConfig == "" {
		if err := requireAbsoluteHome(env.Home); err != nil {
			result.AWSConfig.Err = fmt.Errorf("resolve default AWS config path: %w", err)
		} else {
			awsConfig = filepath.Join(env.Home, ".aws", "config")
		}
	} else {
		awsConfig, result.AWSConfig.Err = resolveTOMLPath(awsConfig, configDir, env.Home)
	}
	if result.AWSConfig.Err == nil {
		result.AWSConfig.Path = awsConfig
		result.AWSConfig.Path, result.AWSConfig.Err = requirePath(awsConfig, pathRegularFile)
	}

	awsCache := cfg.AWS.SSOCachePath
	if awsCache == "" {
		if err := requireAbsoluteHome(env.Home); err != nil {
			result.AWSSSOCache.Err = fmt.Errorf("resolve default AWS SSO cache path: %w", err)
		} else {
			awsCache = filepath.Join(env.Home, ".aws", "sso", "cache")
		}
	} else {
		awsCache, result.AWSSSOCache.Err = resolveTOMLPath(awsCache, configDir, env.Home)
	}
	if result.AWSSSOCache.Err == nil {
		result.AWSSSOCache.Path = awsCache
		result.AWSSSOCache.Path, result.AWSSSOCache.Err = requirePath(awsCache, pathDirectory)
	}

	if cfg.OpenCode.ConfigPathExplicit {
		candidate, err := resolveTOMLPath(cfg.OpenCode.ConfigPath, configDir, env.Home)
		result.OpenCodeConfig.Path = candidate
		if err == nil {
			result.OpenCodeConfig.Path, err = requirePath(candidate, pathDirectory)
		}
		result.OpenCodeConfig.Err = err
	} else if base, err := xdgBase(env.XDGConfigHome, env.Home, ".config", "XDG_CONFIG_HOME"); err != nil {
		result.OpenCodeConfig.Warning = Warning("OpenCode config path is unavailable: " + err.Error())
	} else {
		candidate := filepath.Join(base, "opencode")
		result.OpenCodeConfig.Path = candidate
		physical, err := requirePath(candidate, pathDirectory)
		if errors.Is(err, os.ErrNotExist) {
			result.OpenCodeConfig.Warning = Warning(fmt.Sprintf("OpenCode config directory %q does not exist; continuing with container defaults", candidate))
		} else if err != nil {
			result.OpenCodeConfig.Err = err
		} else {
			result.OpenCodeConfig.Path = physical
		}
	}

	if base, err := xdgBase(env.XDGDataHome, env.Home, filepath.Join(".local", "share"), "XDG_DATA_HOME"); err != nil {
		result.OpenCodeAuth.Warning = Warning("OpenCode auth path is unavailable: " + err.Error())
	} else {
		candidate := filepath.Join(base, "opencode", "auth.json")
		result.OpenCodeAuth.Path = candidate
		physical, err := requirePath(candidate, pathRegularFile)
		if errors.Is(err, os.ErrNotExist) {
			result.OpenCodeAuth.Warning = Warning(fmt.Sprintf("OpenCode auth file %q does not exist; authenticate on the host to persist login", candidate))
		} else if err != nil {
			result.OpenCodeAuth.Err = err
		} else {
			result.OpenCodeAuth.Path = physical
		}
	}
	return result
}

type requiredPathType int

const (
	pathDirectory requiredPathType = iota
	pathRegularFile
)

func requirePath(candidate string, kind requiredPathType) (string, error) {
	physical, err := filepath.EvalSymlinks(candidate)
	if err != nil {
		return "", err
	}
	physical, err = filepath.Abs(physical)
	if err != nil {
		return "", err
	}
	physical = filepath.Clean(physical)
	info, err := os.Stat(physical)
	if err != nil {
		return "", err
	}
	if kind == pathDirectory {
		if !info.IsDir() {
			return "", errors.New("is not a directory")
		}
		file, err := os.Open(physical)
		if err != nil {
			return "", fmt.Errorf("is not readable: %w", err)
		}
		_, readErr := file.Readdirnames(1)
		closeErr := file.Close()
		if readErr != nil && !errors.Is(readErr, io.EOF) {
			return "", fmt.Errorf("is not readable: %w", readErr)
		}
		if closeErr != nil {
			return "", closeErr
		}
		return physical, nil
	}
	if !info.Mode().IsRegular() {
		return "", errors.New("is not a regular file")
	}
	file, err := os.Open(physical)
	if err != nil {
		return "", fmt.Errorf("is not readable: %w", err)
	}
	if err := file.Close(); err != nil {
		return "", err
	}
	return physical, nil
}

func requireAbsoluteHome(home string) error {
	if home == "" {
		return errors.New("HOME is not set")
	}
	if !filepath.IsAbs(home) {
		return fmt.Errorf("HOME must be an absolute path, got %q", home)
	}
	return nil
}

func setString(destination *string, source *string) {
	if source != nil {
		*destination = *source
	}
}

func sortedRawAliasNames(aliases map[string]RawAWSAliasConfig) []string {
	names := make([]string, 0, len(aliases))
	for name := range aliases {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}
