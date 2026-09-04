package docker

import (
	"encoding/json"
	"fmt"
	"os"
	"path"
	"path/filepath"
)

// Mount is encoded as a long-form Compose bind mount. This safely supports
// host paths containing spaces and colons.
type Mount struct {
	Type     string    `json:"type"`
	Source   string    `json:"source"`
	Target   string    `json:"target"`
	ReadOnly bool      `json:"read_only"`
	Bind     BindMount `json:"bind"`
}

type BindMount struct {
	CreateHostPath bool `json:"create_host_path"`
}

type Override struct {
	Services map[string]OverrideService `json:"services"`
}

type OverrideService struct {
	Command []string `json:"command,omitempty"`
	Volumes []Mount  `json:"volumes,omitempty"`
}

// Bind creates the only mount representation emitted by this package.
func Bind(source, target string, readOnly bool) (Mount, error) {
	if !filepath.IsAbs(source) {
		return Mount{}, fmt.Errorf("bind source %q is not absolute", source)
	}
	if !path.IsAbs(target) || path.Clean(target) != target {
		return Mount{}, fmt.Errorf("bind target %q is not absolute", target)
	}
	return Mount{Type: "bind", Source: filepath.Clean(source), Target: target, ReadOnly: readOnly, Bind: BindMount{CreateHostPath: false}}, nil
}

// NewRunOverride creates the credentials and sandbox service overrides.
func NewRunOverride(command []string, credentialMounts, sandboxMounts []Mount) (Override, error) {
	if len(command) == 0 {
		return Override{}, fmt.Errorf("sandbox command is empty")
	}
	if err := validateMounts(credentialMounts); err != nil {
		return Override{}, fmt.Errorf("credentials mounts: %w", err)
	}
	if err := validateMounts(sandboxMounts); err != nil {
		return Override{}, fmt.Errorf("sandbox mounts: %w", err)
	}
	return Override{Services: map[string]OverrideService{
		"credentials": {Volumes: append([]Mount(nil), credentialMounts...)},
		"sandbox":     {Command: append([]string(nil), command...), Volumes: append([]Mount(nil), sandboxMounts...)},
	}}, nil
}

// NewCredentialsOverride creates an aws-check override without sandbox data.
func NewCredentialsOverride(mounts []Mount) (Override, error) {
	if err := validateMounts(mounts); err != nil {
		return Override{}, err
	}
	return Override{Services: map[string]OverrideService{"credentials": {Volumes: append([]Mount(nil), mounts...)}}}, nil
}

// InvocationFiles are private, per-invocation inputs to Compose.
type InvocationFiles struct {
	Dir          string
	OverridePath string
	ConfigPath   string
}

// CredentialsMounts constructs the fixed broker trust-boundary mounts.
func CredentialsMounts(configSnapshot, awsConfig, ssoCache string) ([]Mount, error) {
	specs := []struct {
		source string
		target string
	}{
		{configSnapshot, "/run/wisp/config.toml"},
		{awsConfig, "/home/broker/.aws/config"},
		{ssoCache, "/run/aws/sso-cache"},
	}
	mounts := make([]Mount, 0, len(specs))
	for _, spec := range specs {
		mount, err := Bind(spec.source, spec.target, true)
		if err != nil {
			return nil, err
		}
		mounts = append(mounts, mount)
	}
	return mounts, nil
}

// CreateInvocationFiles creates the private directory and snapshot before
// asking buildOverride to construct an override using the final snapshot path.
func CreateInvocationFiles(runtimeRoot string, configSnapshot []byte, buildOverride func(configPath string) (Override, error)) (InvocationFiles, error) {
	if !filepath.IsAbs(runtimeRoot) {
		return InvocationFiles{}, fmt.Errorf("runtime root %q is not absolute", runtimeRoot)
	}
	if buildOverride == nil {
		return InvocationFiles{}, fmt.Errorf("Compose override builder is nil")
	}
	dir, err := os.MkdirTemp(runtimeRoot, "invocation-")
	if err != nil {
		return InvocationFiles{}, fmt.Errorf("create invocation directory: %w", err)
	}
	cleanup := func(cause error) (InvocationFiles, error) {
		_ = os.RemoveAll(dir)
		return InvocationFiles{}, cause
	}
	if err := os.Chmod(dir, 0700); err != nil {
		return cleanup(fmt.Errorf("secure invocation directory: %w", err))
	}
	files := InvocationFiles{Dir: dir, OverridePath: filepath.Join(dir, "override.yaml")}
	if configSnapshot != nil {
		files.ConfigPath = filepath.Join(dir, "config.toml")
		if err := writeExclusive(files.ConfigPath, configSnapshot); err != nil {
			return cleanup(err)
		}
	}
	override, err := buildOverride(files.ConfigPath)
	if err != nil {
		return cleanup(fmt.Errorf("build Compose override: %w", err))
	}
	data, err := json.MarshalIndent(override, "", "  ")
	if err != nil {
		return cleanup(fmt.Errorf("encode Compose override: %w", err))
	}
	data = append(data, '\n')
	if err := writeExclusive(files.OverridePath, data); err != nil {
		return cleanup(err)
	}
	return files, nil
}

// WriteInvocationFiles securely writes structured JSON (valid Compose YAML)
// and, when non-nil, the exact validated config snapshot.
func WriteInvocationFiles(runtimeRoot string, override Override, configSnapshot []byte) (InvocationFiles, error) {
	return CreateInvocationFiles(runtimeRoot, configSnapshot, func(string) (Override, error) {
		return override, nil
	})
}

func (f InvocationFiles) Cleanup() error {
	if f.Dir == "" {
		return nil
	}
	return os.RemoveAll(f.Dir)
}

func writeExclusive(name string, data []byte) error {
	file, err := os.OpenFile(name, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return fmt.Errorf("create secure invocation file %q: %w", name, err)
	}
	if _, err = file.Write(data); err == nil {
		err = file.Sync()
	}
	closeErr := file.Close()
	if err != nil {
		return fmt.Errorf("write secure invocation file %q: %w", name, err)
	}
	if closeErr != nil {
		return fmt.Errorf("close secure invocation file %q: %w", name, closeErr)
	}
	return nil
}

func validateMounts(mounts []Mount) error {
	for i, mount := range mounts {
		if mount.Type != "bind" || !filepath.IsAbs(mount.Source) || !path.IsAbs(mount.Target) || path.Clean(mount.Target) != mount.Target || mount.Bind.CreateHostPath {
			return fmt.Errorf("mount %d is not a safe long-form bind mount", i+1)
		}
	}
	return nil
}
