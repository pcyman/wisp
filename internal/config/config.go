package config

const (
	SchemaVersion           = 1
	DefaultSandboxImage     = "wisp:local"
	DefaultCredentialsImage = "wisp-credentials:local"
	DefaultBuildCPUs        = 4
	DefaultBoto3Version     = "1.35.99"
	DefaultAWSDuration      = 3600
	InitReminder            = "AWS access is disabled until an [aws] table and alias are configured"
)

// RawConfig preserves whether scalar TOML keys were present.
type RawConfig struct {
	SchemaVersion *int               `toml:"schema_version"`
	Images        *RawImageConfig    `toml:"images"`
	Build         *RawBuildConfig    `toml:"build"`
	OpenCode      *RawOpenCodeConfig `toml:"opencode"`
	AWS           *RawAWSConfig      `toml:"aws"`
	Mounts        []RawMountConfig   `toml:"mounts"`
}

type RawImageConfig struct {
	Sandbox     *string `toml:"sandbox"`
	Credentials *string `toml:"credentials"`
}

type RawBuildConfig struct {
	CPUs     *int              `toml:"cpus"`
	Versions *RawVersionConfig `toml:"versions"`
}

type RawVersionConfig struct {
	OpenCode  *string `toml:"opencode"`
	Hunk      *string `toml:"hunk"`
	AWSCLI    *string `toml:"aws_cli"`
	Kubectl   *string `toml:"kubectl"`
	Helm      *string `toml:"helm"`
	Terraform *string `toml:"terraform"`
	YQ        *string `toml:"yq"`
	UV        *string `toml:"uv"`
	Go        *string `toml:"go"`
	Boto3     *string `toml:"boto3"`
}

type RawOpenCodeConfig struct {
	ConfigPath *string `toml:"config_path"`
}

type RawAWSConfig struct {
	Default             *string                      `toml:"default"`
	HostConfigPath      *string                      `toml:"host_config_path"`
	HostCredentialsPath *string                      `toml:"host_credentials_path"`
	SSOCachePath        *string                      `toml:"sso_cache_path"`
	Aliases             map[string]RawAWSAliasConfig `toml:"aliases"`
}

type RawAWSAliasConfig struct {
	Profile         *string `toml:"profile"`
	RoleARN         *string `toml:"role_arn"`
	Region          *string `toml:"region"`
	DurationSeconds *int    `toml:"duration_seconds"`
	EKSCluster      *string `toml:"eks_cluster"`
}

type RawMountConfig struct {
	Source *string `toml:"source"`
	Target *string `toml:"target"`
	Mode   *string `toml:"mode"`
}

// Config is the defaulted, validated configuration used by the application.
// Host paths become physical paths after ValidateHostPaths is called.
type Config struct {
	SchemaVersion int
	Images        ImageConfig
	Build         BuildConfig
	OpenCode      OpenCodeConfig
	Hunk          HunkConfig
	AWS           AWSConfig
	Mounts        []MountConfig
}

type ImageConfig struct {
	Sandbox     string
	Credentials string
}

type BuildConfig struct {
	CPUs     int
	Versions VersionConfig
}

type VersionConfig struct {
	OpenCode  string
	Hunk      string
	AWSCLI    string
	Kubectl   string
	Helm      string
	Terraform string
	YQ        string
	UV        string
	Go        string
	Boto3     string
}

type OpenCodeConfig struct {
	ConfigPath         string
	ConfigPathExplicit bool
	AuthPath           string
}

type HunkConfig struct {
	ConfigPath string
}

type AWSConfig struct {
	Enabled                     bool
	Default                     string
	HostConfigPath              string
	HostConfigPathExplicit      bool
	HostCredentialsPath         string
	HostCredentialsPathExplicit bool
	SSOCachePath                string
	SSOCachePathExplicit        bool
	Aliases                     map[string]AWSAliasConfig
}

type AWSAliasConfig struct {
	Profile         string
	RoleARN         string
	Region          string
	DurationSeconds int
	EKSCluster      string
}

type MountConfig struct {
	Source string
	Target string
	Mode   string
}

// Environment contains only environment values that affect config paths.
// Callers should populate it explicitly rather than allowing ambient state to
// enter otherwise deterministic config planning.
type Environment struct {
	Home          string
	XDGConfigHome string
	XDGDataHome   string
}

type Warning string

func (w Warning) String() string { return string(w) }

// Result retains the exact validated bytes for immutable run planning.
type Result struct {
	Path     string
	Snapshot []byte
	Config   Config
	Warnings []Warning
}
