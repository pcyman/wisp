package config

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"wisp/internal/hostpath"
	"wisp/internal/mountpolicy"
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
		cfg.AWS.Enabled = true
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
		if raw.AWS.HostCredentialsPath != nil {
			cfg.AWS.HostCredentialsPath = *raw.AWS.HostCredentialsPath
			cfg.AWS.HostCredentialsPathExplicit = true
			if cfg.AWS.HostCredentialsPath == "" {
				return Config{}, errors.New("aws.host_credentials_path must not be empty when set")
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
	if cfg.AWS.Enabled && len(cfg.AWS.Aliases) == 0 {
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
	if raw.RoleARN == nil || !strings.HasPrefix(*raw.RoleARN, "arn:") {
		return AWSAliasConfig{}, fmt.Errorf("aws.aliases.%s.role_arn is required and must begin with %q", name, "arn:")
	}
	alias := AWSAliasConfig{
		RoleARN:         *raw.RoleARN,
		DurationSeconds: DefaultAWSDuration,
	}
	if raw.Profile != nil {
		alias.Profile = *raw.Profile
		if strings.TrimSpace(alias.Profile) == "" {
			return AWSAliasConfig{}, fmt.Errorf("aws.aliases.%s.profile must not be empty when set", name)
		}
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
		if err := mountpolicy.ValidateTarget(mount.Target); err != nil {
			return fmt.Errorf("mounts[%d].target: %w", i, err)
		}
		for j := 0; j < i; j++ {
			other := mounts[j].Target
			if mountpolicy.Overlaps(mount.Target, other) {
				return fmt.Errorf("mount targets %q and %q overlap", other, mount.Target)
			}
		}
	}
	return nil
}

// SelectAWSAlias applies requested, configured-default, then sole-alias
// precedence. It never relies on map iteration order.
func SelectAWSAlias(cfg Config, requested string) (string, error) {
	if !cfg.AWS.Enabled {
		if requested != "" {
			return "", errors.New("AWS support is not enabled in the configuration")
		}
		return "", nil
	}
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

	if cfg.AWS.Enabled {
		var err error
		cfg.AWS.HostConfigPath, err = resolveOptionalAWSPath(cfg.AWS.HostConfigPath, cfg.AWS.HostConfigPathExplicit, filepath.Join(".aws", "config"), pathRegularFile, configDir, env.Home, "aws.host_config_path")
		if err != nil {
			return nil, err
		}
		cfg.AWS.HostCredentialsPath, err = resolveOptionalAWSPath(cfg.AWS.HostCredentialsPath, cfg.AWS.HostCredentialsPathExplicit, filepath.Join(".aws", "credentials"), pathRegularFile, configDir, env.Home, "aws.host_credentials_path")
		if err != nil {
			return nil, err
		}
		cfg.AWS.SSOCachePath, err = resolveOptionalAWSPath(cfg.AWS.SSOCachePath, cfg.AWS.SSOCachePathExplicit, filepath.Join(".aws", "sso", "cache"), pathDirectory, configDir, env.Home, "aws.sso_cache_path")
		if err != nil {
			return nil, err
		}
	}

	warnings := make([]Warning, 0, 3)
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

	configBase, err := xdgBase(env.XDGConfigHome, env.Home, ".config", "XDG_CONFIG_HOME")
	if err != nil {
		if env.XDGConfigHome != "" || env.Home != "" {
			return nil, fmt.Errorf("resolve Hunk config path: %w", err)
		}
		warnings = append(warnings, Warning("Hunk config path is unavailable: "+err.Error()))
	} else {
		candidate := filepath.Join(configBase, "hunk", "config.toml")
		physical, pathErr := requirePath(candidate, pathRegularFile)
		if pathErr != nil {
			if errors.Is(pathErr, os.ErrNotExist) {
				warnings = append(warnings, Warning(fmt.Sprintf("Hunk config file %q does not exist; continuing with container defaults", candidate)))
			} else {
				return nil, fmt.Errorf("Hunk config path %q: %w", candidate, pathErr)
			}
		} else {
			cfg.Hunk.ConfigPath = physical
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
	physical, err := requirePath(authPath, pathRegularFile)
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

// HostPathDiagnostic describes one independently checked host path. Optional
// default paths use Warning; explicitly configured or required paths use Err.
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
	AWSCredentials HostPathDiagnostic
	AWSSSOCache    HostPathDiagnostic
	OpenCodeConfig HostPathDiagnostic
	OpenCodeAuth   HostPathDiagnostic
	HunkConfig     HostPathDiagnostic
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

	if cfg.AWS.Enabled {
		result.AWSConfig = diagnoseOptionalAWSPath(cfg.AWS.HostConfigPath, cfg.AWS.HostConfigPathExplicit, filepath.Join(".aws", "config"), pathRegularFile, configDir, env.Home, "AWS config")
		result.AWSCredentials = diagnoseOptionalAWSPath(cfg.AWS.HostCredentialsPath, cfg.AWS.HostCredentialsPathExplicit, filepath.Join(".aws", "credentials"), pathRegularFile, configDir, env.Home, "AWS credentials")
		result.AWSSSOCache = diagnoseOptionalAWSPath(cfg.AWS.SSOCachePath, cfg.AWS.SSOCachePathExplicit, filepath.Join(".aws", "sso", "cache"), pathDirectory, configDir, env.Home, "AWS SSO cache")
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

	if base, err := xdgBase(env.XDGConfigHome, env.Home, ".config", "XDG_CONFIG_HOME"); err != nil {
		result.HunkConfig.Warning = Warning("Hunk config path is unavailable: " + err.Error())
	} else {
		candidate := filepath.Join(base, "hunk", "config.toml")
		result.HunkConfig.Path = candidate
		physical, err := requirePath(candidate, pathRegularFile)
		if errors.Is(err, os.ErrNotExist) {
			result.HunkConfig.Warning = Warning(fmt.Sprintf("Hunk config file %q does not exist; continuing with container defaults", candidate))
		} else if err != nil {
			result.HunkConfig.Err = err
		} else {
			result.HunkConfig.Path = physical
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

func resolveOptionalAWSPath(configured string, explicit bool, defaultRelative string, kind requiredPathType, configDir, home, field string) (string, error) {
	candidate := configured
	if candidate == "" {
		if err := requireAbsoluteHome(home); err != nil {
			if explicit {
				return "", fmt.Errorf("%s: %w", field, err)
			}
			return "", nil
		}
		candidate = filepath.Join(home, defaultRelative)
	} else {
		resolved, err := resolveTOMLPath(candidate, configDir, home)
		if err != nil {
			return "", fmt.Errorf("%s: %w", field, err)
		}
		candidate = resolved
	}
	physical, err := requirePath(candidate, kind)
	if err != nil {
		if !explicit && errors.Is(err, os.ErrNotExist) {
			return "", nil
		}
		return "", fmt.Errorf("%s %q: %w", field, candidate, err)
	}
	return physical, nil
}

func diagnoseOptionalAWSPath(configured string, explicit bool, defaultRelative string, kind requiredPathType, configDir, home, name string) HostPathDiagnostic {
	var result HostPathDiagnostic
	candidate := configured
	var err error
	if candidate == "" {
		if err = requireAbsoluteHome(home); err == nil {
			candidate = filepath.Join(home, defaultRelative)
		}
	} else {
		candidate, err = resolveTOMLPath(candidate, configDir, home)
	}
	result.Path = candidate
	if err == nil {
		result.Path, err = requirePath(candidate, kind)
	}
	if err == nil {
		return result
	}
	if explicit {
		result.Err = err
	} else {
		result.Warning = Warning(fmt.Sprintf("default %s path %q is unavailable (%v); another credential source may be used", name, candidate, err))
	}
	return result
}

type requiredPathType int

const (
	pathDirectory requiredPathType = iota
	pathRegularFile
)

func requirePath(candidate string, kind requiredPathType) (string, error) {
	physical, info, err := hostpath.Resolve(candidate)
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
