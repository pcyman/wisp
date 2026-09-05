package app

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"

	"wisp/internal/cli"
	"wisp/internal/process"
)

type fakeGit struct {
	root   string
	called string
	env    []string
}

func (f *fakeGit) Capture(_ context.Context, command process.Command) ([]byte, []byte, error) {
	f.called = command.Args[1]
	f.env = append([]string(nil), command.Env...)
	return []byte(f.root + "\n"), nil, nil
}

func TestPlanRunUsesProvidedProcessEnvironmentForGit(t *testing.T) {
	root, configPath, projectDir := applicationFixture(t)
	git := &fakeGit{root: projectDir}
	want := []string{"PATH=/bin", "DOCKER_HOST=unix:///socket", "DOCKER_CONTEXT=local", "DOCKER_TLS_VERIFY=1", "DOCKER_CERT_PATH=/certs"}
	processEnv := append(append([]string(nil), want...), privateProcessEnvironment("inherited")...)
	_, err := PlanRun(context.Background(), runRequest(configPath, projectDir, ""), PlanOptions{
		InvocationDir: root,
		UID:           os.Getuid(),
		GID:           os.Getgid(),
		Environment:   HostEnvironment{Home: filepath.Join(root, "home"), TempDir: root},
		Runner:        git,
		ProcessEnv:    processEnv,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(git.env, want) {
		t.Fatalf("Git environment = %#v, want %#v", git.env, want)
	}
}

func privateProcessEnvironment(value string) []string {
	return []string{
		"WISP_IMAGE=" + value,
		"WISP_CREDENTIALS_IMAGE=" + value,
		"WISP_UID=" + value,
		"WISP_GID=" + value,
		"WISP_AWS_ALIAS=" + value,
		"WISP_AWS_AUTHORIZATION_TOKEN=" + value,
		"WISP_PROJECT_HASH=" + value,
		"WISP_CLI_VERSION=" + value,
		"OPENCODE_VERSION=" + value,
		"HUNK_VERSION=" + value,
		"AWS_CLI_VERSION=" + value,
		"KUBECTL_VERSION=" + value,
		"HELM_VERSION=" + value,
		"TERRAFORM_VERSION=" + value,
		"GO_VERSION=" + value,
		"BOTO3_VERSION=" + value,
	}
}

func (*fakeGit) Attached(context.Context, process.Command) error { return nil }

func TestPlanRunBuildsCompletePlanWithoutDocker(t *testing.T) {
	root := t.TempDir()
	home := filepath.Join(root, "home")
	configDir := filepath.Join(root, "config")
	projectRoot := filepath.Join(root, "project")
	requested := filepath.Join(projectRoot, "service")
	configuredRepo := filepath.Join(root, "configured")
	cliRepo := filepath.Join(root, "cli")
	openCodeConfig := filepath.Join(home, ".config", "opencode")
	openCodeAuth := filepath.Join(home, ".local", "share", "opencode", "auth.json")
	awsConfig := filepath.Join(home, ".aws", "config")
	awsCache := filepath.Join(home, ".aws", "sso", "cache")
	for _, directory := range []string{
		configDir, requested, configuredRepo, cliRepo, openCodeConfig,
		filepath.Dir(openCodeAuth), filepath.Dir(awsConfig), awsCache,
	} {
		if err := os.MkdirAll(directory, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	for path, contents := range map[string]string{openCodeAuth: "{}", awsConfig: "[profile dev]\n"} {
		if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	configPath := filepath.Join(configDir, "wisp.toml")
	contents := `schema_version = 1
[images]
sandbox = "sandbox:test"
credentials = "credentials:test"
[build]
cpus = 3
[build.versions]
opencode = "1.2.3"
boto3 = "1.35.99"
[aws]
default = "dev"
host_config_path = "` + awsConfig + `"
sso_cache_path = "` + awsCache + `"
[aws.aliases.dev]
profile = "dev"
role_arn = "arn:aws:iam::123456789012:role/Wisp"
[[mounts]]
source = "` + configuredRepo + `"
target = "/workspace/repos/configured"
mode = "rw"
`
	if err := os.WriteFile(configPath, []byte(contents), 0o600); err != nil {
		t.Fatal(err)
	}

	git := &fakeGit{root: projectRoot}
	plan, err := PlanRun(context.Background(), runRequest(configPath, requested, cliRepo), PlanOptions{
		InvocationDir: root,
		UID:           os.Getuid(),
		GID:           os.Getgid(),
		CLIVersion:    "test-version",
		Environment: HostEnvironment{
			Home:      home,
			TempDir:   root,
			Term:      "xterm-256color",
			ColorTerm: "truecolor",
		},
		Runner: git,
	})
	if err != nil {
		t.Fatal(err)
	}
	if git.called != requested {
		t.Fatalf("GitRoot called with %q, want %q", git.called, requested)
	}
	if plan.ConfigPath != configPath || string(plan.ConfigSnapshot) != contents {
		t.Fatal("plan did not retain the effective path and exact config snapshot")
	}
	if plan.SelectedAWSAlias != "dev" || plan.AWSConfigPath != awsConfig || plan.AWSSSOCachePath != awsCache {
		t.Fatalf("unexpected AWS plan: %#v", plan)
	}
	if plan.Project.RootDir != projectRoot || plan.Project.Workdir() != "/workspace/current/service" {
		t.Fatalf("unexpected project: %#v", plan.Project)
	}
	if plan.AgentName != "OpenCode" || !reflect.DeepEqual(plan.Command, []string{"opencode"}) {
		t.Fatalf("unexpected agent: %q %#v", plan.AgentName, plan.Command)
	}
	wantTargets := []string{
		"/workspace/current",
		"/run/wisp/opencode/config",
		"/run/wisp/opencode/data/opencode/auth.json",
		"/workspace/repos/configured",
		"/workspace/repos/cli",
	}
	if len(plan.Mounts) != len(wantTargets) {
		t.Fatalf("mounts = %#v", plan.Mounts)
	}
	for i, target := range wantTargets {
		if plan.Mounts[i].Target != target {
			t.Fatalf("mount %d target = %q, want %q", i, plan.Mounts[i].Target, target)
		}
	}
	if plan.Mounts[0].ReadOnly || !plan.Mounts[1].ReadOnly || !plan.Mounts[2].ReadOnly || plan.Mounts[3].ReadOnly || !plan.Mounts[4].ReadOnly {
		t.Fatalf("unexpected mount modes: %#v", plan.Mounts)
	}
	if _, exists := plan.Environment["WISP_AWS_AUTHORIZATION_TOKEN"]; exists {
		t.Fatal("run planning generated an authorization token")
	}
	for key, want := range map[string]string{
		"WISP_IMAGE": "sandbox:test", "WISP_CREDENTIALS_IMAGE": "credentials:test",
		"WISP_AWS_ALIAS": "dev", "WISP_CLI_VERSION": "test-version",
		"OPENCODE_VERSION": "1.2.3", "BOTO3_VERSION": "1.35.99",
		"TERM": "xterm-256color", "COLORTERM": "truecolor",
	} {
		if plan.Environment[key] != want {
			t.Errorf("environment[%q] = %q, want %q", key, plan.Environment[key], want)
		}
	}
	if plan.RuntimeDirectories.Assets != filepath.Join(home, ".cache", "wisp", "runtime") ||
		plan.RuntimeDirectories.Private != filepath.Join(root, "wisp-"+plan.Environment["WISP_UID"]) {
		t.Fatalf("runtime directories = %#v", plan.RuntimeDirectories)
	}
	if plan.OpenCodeDataRoot != filepath.Join(home, ".local", "share", "wisp") {
		t.Fatalf("OpenCode data root = %q", plan.OpenCodeDataRoot)
	}
	if len(plan.Warnings) != 0 {
		t.Fatalf("warnings = %q", plan.Warnings)
	}
}

func TestPlanRunRetainsMissingOpenCodeWarnings(t *testing.T) {
	root := t.TempDir()
	home := filepath.Join(root, "home")
	projectDir := filepath.Join(root, "project")
	awsConfig := filepath.Join(home, ".aws", "config")
	awsCache := filepath.Join(home, ".aws", "sso", "cache")
	for _, directory := range []string{projectDir, filepath.Dir(awsConfig), awsCache} {
		if err := os.MkdirAll(directory, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(awsConfig, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	configPath := filepath.Join(root, "config.toml")
	contents := `schema_version = 1
[aws]
host_config_path = "` + awsConfig + `"
sso_cache_path = "` + awsCache + `"
[aws.aliases.only]
profile = "only"
role_arn = "arn:aws:iam::123456789012:role/Wisp"
`
	if err := os.WriteFile(configPath, []byte(contents), 0o600); err != nil {
		t.Fatal(err)
	}

	plan, err := PlanRun(context.Background(), runRequest(configPath, projectDir, ""), PlanOptions{
		InvocationDir: root,
		UID:           os.Getuid(),
		GID:           os.Getgid(),
		Environment:   HostEnvironment{Home: home, TempDir: root},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Warnings) != 2 {
		t.Fatalf("warnings = %q, want config and auth warnings", plan.Warnings)
	}
	if len(plan.Mounts) != 1 || plan.Mounts[0].Target != "/workspace/current" {
		t.Fatalf("mounts = %#v, want only project", plan.Mounts)
	}
}

func TestPlanRunWithoutAWSHasNoAWSInputs(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	projectDir := filepath.Join(root, "project")
	if err := os.Mkdir(projectDir, 0o700); err != nil {
		t.Fatal(err)
	}
	configPath := filepath.Join(root, "config.toml")
	if err := os.WriteFile(configPath, []byte("schema_version = 1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	plan, err := PlanRun(context.Background(), runRequest(configPath, projectDir, ""), PlanOptions{
		InvocationDir: root,
		UID:           os.Getuid(),
		GID:           os.Getgid(),
		Environment:   HostEnvironment{Home: root, TempDir: root},
	})
	if err != nil {
		t.Fatal(err)
	}
	if plan.AWSEnabled || plan.SelectedAWSAlias != "" || plan.AWSConfigPath != "" || plan.AWSCredentialsPath != "" || plan.AWSSSOCachePath != "" {
		t.Fatalf("AWS plan = %#v", plan)
	}
	if _, ok := plan.Environment["WISP_AWS_ALIAS"]; ok {
		t.Fatal("disabled plan contains WISP_AWS_ALIAS")
	}
}

func TestPlanRunReportsConfigResolutionError(t *testing.T) {
	root := t.TempDir()
	_, err := PlanRun(context.Background(), runRequest(filepath.Join(root, "missing.toml"), root, ""), PlanOptions{
		InvocationDir: root,
		UID:           os.Getuid(),
		GID:           os.Getgid(),
		Environment:   HostEnvironment{Home: root},
	})
	if err == nil || !strings.Contains(err.Error(), "resolve config") {
		t.Fatalf("error = %v, want config resolution error", err)
	}
}

func TestPlanRuntimeDirectoriesUsesOwnedXDGRuntimeDirectory(t *testing.T) {
	root := t.TempDir()
	directories, err := planRuntimeDirectories(PlanOptions{
		UID: os.Getuid(),
		Environment: HostEnvironment{
			Home:          root,
			XDGRuntimeDir: root,
			TempDir:       filepath.Join(root, "fallback"),
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if directories.Private != filepath.Join(root, "wisp") {
		t.Fatalf("private runtime root = %q", directories.Private)
	}
}

func TestPlanRuntimeDirectoriesFallsBackToPrivateTempRoot(t *testing.T) {
	root := t.TempDir()
	directories, err := planRuntimeDirectories(PlanOptions{
		UID: os.Getuid(),
		Environment: HostEnvironment{
			Home:          root,
			XDGRuntimeDir: filepath.Join(root, "missing"),
			TempDir:       root,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if directories.Private != filepath.Join(root, "wisp-"+strconv.Itoa(os.Getuid())) {
		t.Fatalf("private runtime root = %q", directories.Private)
	}
}

func TestPlanOpenCodeDataRootUsesXDGDataHome(t *testing.T) {
	root := t.TempDir()
	got, err := planOpenCodeDataRoot(HostEnvironment{Home: filepath.Join(root, "home"), XDGDataHome: filepath.Join(root, "data")})
	if err != nil {
		t.Fatal(err)
	}
	if want := filepath.Join(root, "data", "wisp"); got != want {
		t.Fatalf("OpenCode data root = %q, want %q", got, want)
	}
}

func TestPlanOpenCodeDataRootRejectsMissingOrRelativeBase(t *testing.T) {
	for _, test := range []struct {
		name string
		env  HostEnvironment
	}{
		{name: "missing"},
		{name: "relative XDG", env: HostEnvironment{Home: t.TempDir(), XDGDataHome: "data"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			if _, err := planOpenCodeDataRoot(test.env); err == nil {
				t.Fatal("invalid OpenCode data base was accepted")
			}
		})
	}
}

func runRequest(configPath, directory, readOnlyMount string) cli.RunRequest {
	request := cli.RunRequest{ConfigPath: configPath, Directory: directory}
	if readOnlyMount != "" {
		request.ReadOnlyMounts = []string{readOnlyMount}
	}
	return request
}
