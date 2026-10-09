package app

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"reflect"
	"strings"
	"testing"

	"wisp/internal/cli"
	"wisp/internal/docker"
	"wisp/internal/process"
)

func TestPlanEnvPassthrough(t *testing.T) {
	root, configPath, projectDir := applicationFixture(t)
	if err := os.WriteFile(configPath, []byte("schema_version = 1\nenv_passthrough = [\"TOKEN\", \"EMPTY\", \"UNSET\"]\n"), 0600); err != nil {
		t.Fatal(err)
	}
	git := &fakeGit{root: projectDir}
	plan, err := PlanRun(context.Background(), runRequest(configPath, projectDir, ""), PlanOptions{
		InvocationDir: root, UID: os.Getuid(), GID: os.Getgid(),
		Environment: HostEnvironment{Home: root, TempDir: root},
		Runner:      git,
		ProcessEnv:  []string{"TOKEN=literal=$value\nwith spaces", "EMPTY=", "UNSELECTED=secret"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(git.env, []string{"UNSELECTED=secret"}) {
		t.Fatalf("Git environment = %#v", git.env)
	}
	want := map[string]string{"TOKEN": "literal=$value\nwith spaces", "EMPTY": ""}
	if !reflect.DeepEqual(plan.SandboxEnvironment, want) {
		t.Fatalf("sandbox environment = %#v", plan.SandboxEnvironment)
	}
	for name := range want {
		if _, exists := plan.Environment[name]; exists {
			t.Fatal("passthrough was added to Compose interpolation environment")
		}
	}
}

func TestRunEnvPassthroughIsolationAndCleanup(t *testing.T) {
	for _, agent := range []string{"opencode", "pi"} {
		for _, aws := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/aws=%t", agent, aws), func(t *testing.T) {
				root, configPath, projectDir := applicationFixture(t)
				contents, err := os.ReadFile(configPath)
				if err != nil {
					t.Fatal(err)
				}
				if !aws {
					contents = []byte("schema_version = 1\n")
				}
				contents = bytes.Replace(contents, []byte("schema_version = 1\n"), []byte("schema_version = 1\nenv_passthrough = [\"TOKEN\", \"EMPTY\", \"UNSET\"]\n"), 1)
				if err := os.WriteFile(configPath, contents, 0600); err != nil {
					t.Fatal(err)
				}
				const secret = "secret=${HOST_REFERENCE} $$ \"quoted\"\nsecond line"
				var stderr bytes.Buffer
				fake := &lifecycleFake{}
				runner := fake.runner(t)
				application := newFixtureApp(t, root, runner, &bytes.Buffer{}, &stderr)
				application.deps.Environment = append(application.deps.Environment, "TOKEN="+secret, "EMPTY=")
				var overridePath string
				fake.attachedError = func(command process.Command) error {
					index := indexSlice(command.Args, []string{"run", "--rm"})
					if index < 2 {
						t.Fatal("missing Compose override")
					}
					overridePath = command.Args[index-1]
					data, err := os.ReadFile(overridePath)
					if err != nil {
						t.Fatal(err)
					}
					var override docker.Override
					if err := json.Unmarshal(data, &override); err != nil {
						t.Fatal(err)
					}
					env := override.Services["sandbox"].Environment
					if env["TOKEN"] != strings.ReplaceAll(secret, "$", "$$") {
						t.Fatal("literal passthrough not in sandbox override")
					}
					if value, exists := env["EMPTY"]; !exists || value != "" {
						t.Fatal("empty passthrough omitted")
					}
					if _, exists := env["UNSET"]; exists {
						t.Fatal("unset passthrough included")
					}
					if len(override.Services["credentials"].Environment) != 0 {
						t.Fatal("passthrough reached broker")
					}
					return errors.New("launch failed: " + secret)
				}
				fake.cleanupError = func(process.Command) error { return errors.New("cleanup failed: " + secret) }
				status := application.Execute(context.Background(), cli.Request{Command: cli.CommandRun, Run: cli.RunRequest{Directory: projectDir, ConfigPath: configPath, Agent: agent}})
				if status != 1 || strings.Contains(stderr.String(), secret) || !strings.Contains(stderr.String(), "[REDACTED]") {
					t.Fatalf("status=%d stderr=%q", status, stderr.String())
				}
				if overridePath == "" {
					t.Fatal("sandbox did not start")
				}
				if _, err := os.Stat(overridePath); !os.IsNotExist(err) {
					t.Fatalf("override not cleaned up: %v", err)
				}
				for _, command := range append(runner.commands, runner.attaches...) {
					for _, entry := range command.Env {
						if strings.HasPrefix(entry, "TOKEN=") || strings.HasPrefix(entry, "EMPTY=") {
							t.Fatalf("passthrough reached host subprocess %s %v", command.Path, command.Args)
						}
					}
				}
			})
		}
	}
}

func TestPassthroughRedactionEncodedValues(t *testing.T) {
	const value = "secret=$TOKEN \"quoted\"\nsecond line"
	app := &App{}
	for _, variant := range []string{value, strings.ReplaceAll(value, "$", "$$")} {
		quoted, _ := json.Marshal(variant)
		for _, message := range []string{variant, string(quoted)} {
			got := app.redactText(message, nil, map[string]string{"TOKEN": value})
			if strings.Contains(got, "secret=") || !strings.Contains(got, "[REDACTED]") {
				t.Fatalf("redaction = %q", got)
			}
		}
	}
}

func TestPassthroughRedactionOverlappingValues(t *testing.T) {
	app := &App{}
	got := app.redactText("abcd abc [REDACTED]", nil, map[string]string{"A": "abc", "B": "abcd", "C": "", "D": "REDACTED"})
	if got != "[REDACTED] [REDACTED] [[REDACTED]]" {
		t.Fatalf("redaction = %q", got)
	}
}
