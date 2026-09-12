package config

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

func TestLoadResolvesPhysicalPathsAndWarnings(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	configDir := filepath.Join(root, "configuration")
	realMount := filepath.Join(root, "real mount:repo")
	linkMount := filepath.Join(configDir, "linked-repo")
	awsConfig := filepath.Join(root, "aws", "config")
	awsCache := filepath.Join(root, "aws", "cache")
	hunkConfig := filepath.Join(root, ".config", "hunk", "config.toml")
	for _, directory := range []string{configDir, realMount, filepath.Dir(awsConfig), awsCache} {
		if err := os.MkdirAll(directory, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(awsConfig, []byte("[profile development]\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(hunkConfig), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(hunkConfig, []byte("theme = \"dark\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(realMount, linkMount); err != nil {
		t.Fatal(err)
	}
	configPath := filepath.Join(configDir, "config.toml")
	contents := `schema_version = 1

[aws]
host_config_path = "../aws/config"
sso_cache_path = "../aws/cache"

[aws.aliases.development]
profile = "development"
role_arn = "arn:aws:iam::123456789012:role/Wisp"

[[mounts]]
source = "linked-repo"
target = "/workspace/repos/repo"
`
	if err := os.WriteFile(configPath, []byte(contents), 0o600); err != nil {
		t.Fatal(err)
	}
	result, err := Load(configPath, Environment{Home: root})
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if string(result.Snapshot) != contents {
		t.Fatal("Load() did not retain exact config bytes")
	}
	physicalMount, err := filepath.EvalSymlinks(realMount)
	if err != nil {
		t.Fatal(err)
	}
	if result.Config.Mounts[0].Source != physicalMount {
		t.Fatalf("mount source = %q, want %q", result.Config.Mounts[0].Source, physicalMount)
	}
	if result.Config.Hunk.ConfigPath != hunkConfig {
		t.Fatalf("Hunk config path = %q, want %q", result.Config.Hunk.ConfigPath, hunkConfig)
	}
	if len(result.Warnings) != 2 {
		t.Fatalf("warnings = %q, want OpenCode config and auth warnings", result.Warnings)
	}
}

func TestLoadExplicitOpenCodePathMustExist(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	awsConfig, awsCache := makeAWSPaths(t, root)
	configPath := filepath.Join(root, "config.toml")
	contents := "schema_version = 1\n[opencode]\nconfig_path = \"missing\"\n[aws]\nhost_config_path = " + tomlQuote(awsConfig) + "\nsso_cache_path = " + tomlQuote(awsCache) + validAliasTOML
	if err := os.WriteFile(configPath, []byte(contents), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := Load(configPath, Environment{Home: root})
	if err == nil || !strings.Contains(err.Error(), "opencode.config_path") {
		t.Fatalf("Load() error = %v", err)
	}
}

func TestLoadForRunValidatesOnlySelectedAgentPaths(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name       string
		selected   string
		irrelevant string
	}{
		{name: "Pi ignores OpenCode path", selected: "pi", irrelevant: "opencode"},
		{name: "OpenCode ignores Pi path", selected: "opencode", irrelevant: "pi"},
	} {
		t.Run(test.name, func(t *testing.T) {
			root := t.TempDir()
			configPath := filepath.Join(root, "config.toml")
			contents := "schema_version = 1\n[" + test.irrelevant + "]\nconfig_path = \"missing\"\n"
			if err := os.WriteFile(configPath, []byte(contents), 0o600); err != nil {
				t.Fatal(err)
			}
			result, err := LoadForRun(configPath, Environment{Home: root}, test.selected)
			if err != nil {
				t.Fatalf("LoadForRun() error = %v", err)
			}
			if result.SelectedAgent != test.selected {
				t.Fatalf("selected agent = %q", result.SelectedAgent)
			}
			if _, err := Load(configPath, Environment{Home: root}); err == nil {
				t.Fatal("full config validation accepted missing path")
			}
		})
	}
}

func TestLoadWithoutAWSDoesNotRequireAWSHostPaths(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	configPath := filepath.Join(root, "config.toml")
	if err := os.WriteFile(configPath, []byte("schema_version = 1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	result, err := Load(configPath, Environment{Home: root})
	if err != nil {
		t.Fatal(err)
	}
	if result.Config.AWS.Enabled || result.Config.AWS.HostConfigPath != "" || result.Config.AWS.SSOCachePath != "" {
		t.Fatalf("AWS config = %#v", result.Config.AWS)
	}
}

func TestLoadAWSUsesOnlyAvailableDefaultInputs(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	awsDir := filepath.Join(root, ".aws")
	if err := os.MkdirAll(awsDir, 0o700); err != nil {
		t.Fatal(err)
	}
	credentials := filepath.Join(awsDir, "credentials")
	if err := os.WriteFile(credentials, []byte("[default]\naws_access_key_id = test\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	configPath := filepath.Join(root, "config.toml")
	contents := "schema_version = 1\n[aws.aliases.dev]\nrole_arn = \"arn:role\"\n"
	if err := os.WriteFile(configPath, []byte(contents), 0o600); err != nil {
		t.Fatal(err)
	}
	result, err := Load(configPath, Environment{Home: root})
	if err != nil {
		t.Fatal(err)
	}
	if result.Config.AWS.HostConfigPath != "" || result.Config.AWS.SSOCachePath != "" || result.Config.AWS.HostCredentialsPath != credentials {
		t.Fatalf("AWS paths = %#v", result.Config.AWS)
	}
}

func TestLoadReturnsCanonicalConfigPath(t *testing.T) {
	root := t.TempDir()
	realDir := filepath.Join(root, "real")
	if err := os.Mkdir(realDir, 0700); err != nil {
		t.Fatal(err)
	}
	awsConfig, awsCache := makeAWSPaths(t, root)
	realPath := filepath.Join(realDir, "config.toml")
	contents := "schema_version = 1\n[aws]\nhost_config_path = " + tomlQuote(awsConfig) + "sso_cache_path = " + tomlQuote(awsCache) + validAliasTOML
	if err := os.WriteFile(realPath, []byte(contents), 0600); err != nil {
		t.Fatal(err)
	}
	linkPath := filepath.Join(root, "linked.toml")
	if err := os.Symlink(realPath, linkPath); err != nil {
		t.Fatal(err)
	}
	result, err := Load(linkPath, Environment{Home: root})
	if err != nil {
		t.Fatal(err)
	}
	if result.Path != realPath {
		t.Fatalf("config path = %q, want %q", result.Path, realPath)
	}
}

func TestLoadRejectsRelativeXDGDataHome(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	awsConfig, awsCache := makeAWSPaths(t, root)
	configPath := filepath.Join(root, "config.toml")
	contents := "schema_version = 1\n[aws]\nhost_config_path = " + tomlQuote(awsConfig) + "\nsso_cache_path = " + tomlQuote(awsCache) + validAliasTOML
	if err := os.WriteFile(configPath, []byte(contents), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := Load(configPath, Environment{Home: root, XDGDataHome: "relative"})
	if err == nil || !strings.Contains(err.Error(), "XDG_DATA_HOME must be an absolute path") {
		t.Fatalf("Load() error = %v", err)
	}
}

func makeAWSPaths(t *testing.T, root string) (string, string) {
	t.Helper()
	config := filepath.Join(root, "aws", "config")
	cache := filepath.Join(root, "aws", "cache")
	if err := os.MkdirAll(filepath.Dir(config), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(cache, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(config, []byte(""), 0o600); err != nil {
		t.Fatal(err)
	}
	return config, cache
}

func tomlQuote(value string) string {
	return strconv.Quote(value) + "\n"
}
