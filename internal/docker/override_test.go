package docker

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestRunOverrideAndInvocationFiles(t *testing.T) {
	credential, err := Bind("/home/user/.aws/config", "/home/broker/.aws/config", true)
	if err != nil {
		t.Fatal(err)
	}
	project, err := Bind("/src/a:b with space", "/workspace/current", false)
	if err != nil {
		t.Fatal(err)
	}
	override, err := NewRunOverride([]string{"opencode"}, []Mount{credential}, []Mount{project})
	if err != nil {
		t.Fatal(err)
	}
	files, err := WriteInvocationFiles(t.TempDir(), override, []byte("schema_version = 1\n"))
	if err != nil {
		t.Fatal(err)
	}
	defer files.Cleanup()
	for _, name := range []string{files.OverridePath, files.ConfigPath} {
		info, statErr := os.Stat(name)
		if statErr != nil {
			t.Fatal(statErr)
		}
		if info.Mode().Perm() != 0600 {
			t.Fatalf("%s mode = %o", name, info.Mode().Perm())
		}
	}
	dirInfo, _ := os.Stat(files.Dir)
	if dirInfo.Mode().Perm() != 0700 {
		t.Fatalf("directory mode = %o", dirInfo.Mode().Perm())
	}
	data, err := os.ReadFile(files.OverridePath)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "type: bind") {
		t.Fatal("override was rendered by YAML concatenation")
	}
	var decoded Override
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(decoded, override) {
		t.Fatalf("decoded override = %#v, want %#v", decoded, override)
	}
	if decoded.Services["sandbox"].Volumes[0].Bind.CreateHostPath {
		t.Fatal("create_host_path enabled")
	}
	snapshot, _ := os.ReadFile(files.ConfigPath)
	if string(snapshot) != "schema_version = 1\n" {
		t.Fatalf("snapshot = %q", snapshot)
	}
}

func TestCredentialsOverrideOmitsSandbox(t *testing.T) {
	mount, _ := Bind(filepath.Join(string(filepath.Separator), "config.toml"), "/run/wisp/config.toml", true)
	override, err := NewCredentialsOverride([]Mount{mount})
	if err != nil {
		t.Fatal(err)
	}
	if _, exists := override.Services["sandbox"]; exists {
		t.Fatal("credentials override includes sandbox")
	}
}

func TestRunOverrideEnablesAWSOnlyWithCredentialMounts(t *testing.T) {
	project, _ := Bind("/project", "/workspace/current", false)
	disabled, err := NewRunOverride([]string{"opencode"}, nil, []Mount{project})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := disabled.Services["credentials"]; ok || disabled.Services["sandbox"].NetworkMode != "" || len(disabled.Services["sandbox"].Environment) != 0 {
		t.Fatalf("disabled override contains AWS plumbing: %#v", disabled)
	}

	config, _ := Bind("/config", "/run/wisp/config.toml", true)
	enabled, err := NewRunOverride([]string{"opencode"}, []Mount{config}, []Mount{project})
	if err != nil {
		t.Fatal(err)
	}
	sandbox := enabled.Services["sandbox"]
	if _, ok := enabled.Services["credentials"]; !ok || sandbox.NetworkMode != "service:credentials" || sandbox.Environment["AWS_CONTAINER_CREDENTIALS_FULL_URI"] == "" || sandbox.DependsOn["credentials"].Condition != "service_healthy" {
		t.Fatalf("enabled override lacks AWS plumbing: %#v", enabled)
	}
}

func TestCreateInvocationUsesFinalSnapshotPath(t *testing.T) {
	root := t.TempDir()
	files, err := CreateInvocationFiles(root, []byte("validated"), func(configPath string) (Override, error) {
		mounts, mountErr := CredentialsMounts(configPath, "/host/aws/config", "", "/host/aws/sso/cache")
		if mountErr != nil {
			return Override{}, mountErr
		}
		return NewCredentialsOverride(mounts)
	})
	if err != nil {
		t.Fatal(err)
	}
	defer files.Cleanup()
	data, err := os.ReadFile(files.OverridePath)
	if err != nil {
		t.Fatal(err)
	}
	var override Override
	if err := json.Unmarshal(data, &override); err != nil {
		t.Fatal(err)
	}
	mounts := override.Services["credentials"].Volumes
	if len(mounts) != 3 || mounts[0].Source != files.ConfigPath || mounts[0].Target != "/run/wisp/config.toml" {
		t.Fatalf("credentials mounts = %#v", mounts)
	}
	for _, mount := range mounts {
		if !mount.ReadOnly || mount.Bind.CreateHostPath {
			t.Fatalf("unsafe credentials mount = %#v", mount)
		}
	}
}

func TestBindRejectsUnsafePaths(t *testing.T) {
	if _, err := Bind("relative", "/workspace/current", false); err == nil {
		t.Fatal("relative source accepted")
	}
	if _, err := Bind("/source", "/workspace/../etc", false); err == nil {
		t.Fatal("unclean target accepted")
	}
}
