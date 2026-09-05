package config

import (
	"os"
	"strconv"
	"strings"
	"testing"
)

const validAliasTOML = `
[aws.aliases.development]
profile = "company-development"
role_arn = "arn:aws:iam::123456789012:role/Wisp"
`

func TestDecodeStrict(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		input   string
		wantErr bool
	}{
		{name: "valid", input: "schema_version = 1\n" + validAliasTOML},
		{name: "unknown top level", input: "schema_version = 1\nunknown = true\n" + validAliasTOML, wantErr: true},
		{name: "unknown nested", input: "schema_version = 1\n[images]\nsandox = \"typo\"\n" + validAliasTOML, wantErr: true},
		{name: "duplicate", input: "schema_version = 1\nschema_version = 1\n" + validAliasTOML, wantErr: true},
	}
	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			_, err := Decode([]byte(test.input))
			if (err != nil) != test.wantErr {
				t.Fatalf("Decode() error = %v, wantErr %v", err, test.wantErr)
			}
		})
	}
}

func TestDecodeTestdataUnknown(t *testing.T) {
	t.Parallel()
	data, err := os.ReadFile("testdata/unknown.toml")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Decode(data); err == nil {
		t.Fatal("Decode() accepted unknown testdata key")
	}
}

func TestResolveDefaults(t *testing.T) {
	t.Parallel()
	version := 1
	profile := "profile"
	role := "arn:role"
	cfg, err := Resolve(RawConfig{
		SchemaVersion: &version,
		AWS: &RawAWSConfig{Aliases: map[string]RawAWSAliasConfig{
			"only": {Profile: &profile, RoleARN: &role},
		}},
		Mounts: []RawMountConfig{{Source: stringPointer("repo"), Target: stringPointer("/workspace/repos/repo")}},
	})
	if err != nil {
		t.Fatalf("Resolve() error = %v", err)
	}
	if cfg.Images.Sandbox != DefaultSandboxImage || cfg.Images.Credentials != DefaultCredentialsImage {
		t.Fatalf("image defaults = %#v", cfg.Images)
	}
	if cfg.Build.CPUs != DefaultBuildCPUs || cfg.Build.Versions.Boto3 != DefaultBoto3Version {
		t.Fatalf("build defaults = %#v", cfg.Build)
	}
	if cfg.AWS.Aliases["only"].DurationSeconds != DefaultAWSDuration {
		t.Fatalf("duration = %d", cfg.AWS.Aliases["only"].DurationSeconds)
	}
	if cfg.Mounts[0].Mode != "ro" {
		t.Fatalf("mount mode = %q", cfg.Mounts[0].Mode)
	}
}

func TestResolveRejectsInvalidValues(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		toml    string
		wantErr string
	}{
		{name: "missing schema", toml: validAliasTOML, wantErr: "schema_version is required"},
		{name: "future schema", toml: "schema_version = 2\n" + validAliasTOML, wantErr: "schema_version must be 1"},
		{name: "empty image", toml: "schema_version = 1\n[images]\nsandbox = \"\"\n" + validAliasTOML, wantErr: "images.sandbox"},
		{name: "zero cpus", toml: "schema_version = 1\n[build]\ncpus = 0\n" + validAliasTOML, wantErr: "build.cpus"},
		{name: "empty boto3", toml: "schema_version = 1\n[build.versions]\nboto3 = \"\"\n" + validAliasTOML, wantErr: "boto3"},
		{name: "no aliases", toml: "schema_version = 1\n[aws]\n", wantErr: "at least one alias"},
		{name: "bad alias name", toml: "schema_version = 1\n[aws.aliases.'bad name']\nprofile = \"p\"\nrole_arn = \"arn:r\"\n", wantErr: "alias name"},
		{name: "zero duration", toml: "schema_version = 1\n[aws.aliases.a]\nprofile = \"p\"\nrole_arn = \"arn:r\"\nduration_seconds = 0\n", wantErr: "between 900 and 43200"},
		{name: "eks without region", toml: "schema_version = 1\n[aws.aliases.a]\nprofile = \"p\"\nrole_arn = \"arn:r\"\neks_cluster = \"cluster\"\n", wantErr: "region is required"},
		{name: "unknown default", toml: "schema_version = 1\n[aws]\ndefault = \"missing\"\n" + validAliasTOML, wantErr: "does not name"},
	}
	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			raw, err := Decode([]byte(test.toml))
			if err != nil {
				t.Fatalf("Decode() error = %v", err)
			}
			_, err = Resolve(raw)
			if err == nil || !strings.Contains(err.Error(), test.wantErr) {
				t.Fatalf("Resolve() error = %v, want containing %q", err, test.wantErr)
			}
		})
	}
}

func TestResolveAllowsAWSOptOutAndOptionalProfile(t *testing.T) {
	t.Parallel()
	raw, err := Decode([]byte("schema_version = 1\n"))
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := Resolve(raw)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.AWS.Enabled {
		t.Fatal("AWS was enabled without an [aws] table")
	}
	if alias, err := SelectAWSAlias(cfg, ""); err != nil || alias != "" {
		t.Fatalf("disabled AWS selection = %q, %v", alias, err)
	}
	if _, err := SelectAWSAlias(cfg, "dev"); err == nil {
		t.Fatal("--aws was accepted while AWS is disabled")
	}

	raw, err = Decode([]byte("schema_version = 1\n[aws.aliases.dev]\nrole_arn = \"arn:role\"\n"))
	if err != nil {
		t.Fatal(err)
	}
	cfg, err = Resolve(raw)
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.AWS.Enabled || cfg.AWS.Aliases["dev"].Profile != "" {
		t.Fatalf("optional profile config = %#v", cfg.AWS)
	}
}

func TestDurationBoundaries(t *testing.T) {
	t.Parallel()
	for _, duration := range []int{900, 43200} {
		duration := duration
		t.Run(strconv.Itoa(duration), func(t *testing.T) {
			version := 1
			profile, role := "profile", "arn:role"
			_, err := Resolve(RawConfig{SchemaVersion: &version, AWS: &RawAWSConfig{Aliases: map[string]RawAWSAliasConfig{
				"alias": {Profile: &profile, RoleARN: &role, DurationSeconds: &duration},
			}}})
			if err != nil {
				t.Fatalf("Resolve() rejected duration %d: %v", duration, err)
			}
		})
	}
}

func TestSelectAWSAlias(t *testing.T) {
	t.Parallel()
	cfg := Config{AWS: AWSConfig{Enabled: true, Default: "default", Aliases: map[string]AWSAliasConfig{"default": {}, "other": {}}}}
	if got, err := SelectAWSAlias(cfg, "other"); err != nil || got != "other" {
		t.Fatalf("requested selection = %q, %v", got, err)
	}
	if got, err := SelectAWSAlias(cfg, ""); err != nil || got != "default" {
		t.Fatalf("default selection = %q, %v", got, err)
	}
	cfg.AWS.Default = ""
	if _, err := SelectAWSAlias(cfg, ""); err == nil {
		t.Fatal("SelectAWSAlias() selected nondeterministically from multiple aliases")
	}
	cfg.AWS.Aliases = map[string]AWSAliasConfig{"sole": {}}
	if got, err := SelectAWSAlias(cfg, ""); err != nil || got != "sole" {
		t.Fatalf("sole selection = %q, %v", got, err)
	}
	if _, err := SelectAWSAlias(cfg, "missing"); err == nil {
		t.Fatal("SelectAWSAlias() accepted missing requested alias")
	}
}

func TestMountTargetValidation(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		targets []string
		wantErr bool
	}{
		{name: "siblings", targets: []string{"/workspace/repos/a", "/workspace/repos/b"}},
		{name: "root", targets: []string{"/workspace/repos"}, wantErr: true},
		{name: "unclean", targets: []string{"/workspace/repos/a/../b"}, wantErr: true},
		{name: "outside", targets: []string{"/workspace/current/repo"}, wantErr: true},
		{name: "duplicate", targets: []string{"/workspace/repos/a", "/workspace/repos/a"}, wantErr: true},
		{name: "nested", targets: []string{"/workspace/repos/a", "/workspace/repos/a/b"}, wantErr: true},
	}
	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			mounts := make([]MountConfig, len(test.targets))
			for i, target := range test.targets {
				mounts[i] = MountConfig{Target: target}
			}
			err := validateMountTargets(mounts)
			if (err != nil) != test.wantErr {
				t.Fatalf("validateMountTargets() error = %v, wantErr %v", err, test.wantErr)
			}
		})
	}
}

func stringPointer(value string) *string { return &value }
