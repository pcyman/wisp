package app

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"wisp/internal/docker"
)

func TestAzurePlanAndForwarding(t *testing.T) {
	// Every subset: only none and all four are valid.
	for mask := 0; mask < 16; mask++ {
		root, configPath, projectDir := applicationFixture(t)
		inherited := []string{"PATH=/bin"}
		for i, key := range azureEnvironmentKeys {
			if mask&(1<<i) != 0 {
				inherited = append(inherited, key+"=test-value")
			}
		}
		git := &fakeGit{root: projectDir}
		plan, err := PlanRun(context.Background(), runRequest(configPath, projectDir, ""), PlanOptions{
			InvocationDir: root, UID: os.Getuid(), GID: os.Getgid(),
			Environment: HostEnvironment{Home: filepath.Join(root, "home"), TempDir: root},
			Runner:      git, ProcessEnv: inherited,
		})
		if mask != 0 && mask != 15 {
			if err == nil || strings.Contains(err.Error(), "test-value") || git.called != "" {
				t.Fatalf("partial configuration %d not rejected safely before subprocesses", mask)
			}
			continue
		}
		if err != nil {
			t.Fatal(err)
		}
		composeEnv := strings.Join(docker.SanitizeEnvironment(inherited, plan.Environment), "\n")
		for _, key := range azureEnvironmentKeys {
			if mask == 15 && (plan.Environment[key] != "test-value" || !strings.Contains(composeEnv, key+"=test-value")) {
				t.Fatalf("missing planned Azure value for %s", key)
			}
			if mask == 0 && strings.Contains(composeEnv, key+"=") {
				t.Fatalf("unexpected Azure value for %s", key)
			}
			if strings.Contains(strings.Join(git.env, "\n"), key+"=") {
				t.Fatalf("Azure value reached Git for %s", key)
			}
		}
	}
}

func TestAzureAppReadsHostValuesAndRedactsSecret(t *testing.T) {
	app, err := New(Dependencies{Environment: []string{"WISP_AZURE_CLIENT_SECRET=test-secret"}})
	if err != nil {
		t.Fatal(err)
	}
	values := app.planOptions().ProcessEnv
	if environmentValue(values, "WISP_AZURE_CLIENT_SECRET") != "test-secret" {
		t.Fatal("planning lost explicit host input")
	}
	if strings.Contains(strings.Join(app.childEnvironment(nil), "\n"), "test-secret") {
		t.Fatal("secret inherited by unplanned subprocess")
	}
	if got := app.redactText("error test-secret", map[string]string{"WISP_AZURE_CLIENT_SECRET": "test-secret"}); got != "error [REDACTED]" {
		t.Fatal("secret not redacted")
	}
}
