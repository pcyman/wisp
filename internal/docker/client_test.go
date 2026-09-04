package docker

import (
	"context"
	"reflect"
	"strings"
	"testing"
)

func TestDirectDockerCommandsUseSanitizedEnvironment(t *testing.T) {
	preserved := []string{
		"PATH=/bin",
		"DOCKER_HOST=unix:///socket",
		"DOCKER_CONTEXT=local",
		"DOCKER_TLS_VERIFY=1",
		"DOCKER_CERT_PATH=/certs",
	}
	inherited := append(append([]string(nil), preserved...), privateEnvironmentForTest("inherited")...)
	runner := &fakeRunner{captureResults: []fakeResult{
		{stdout: "Docker version 27.0.0"},
		{stdout: "27.0.0"},
		{stdout: "2.29.0"},
		{stdout: "github.com/docker/buildx v0.16.0"},
		{stdout: "[]"},
		{stdout: `[{"Id":"id","Name":"/name","Config":{"Labels":{}},"State":{"Running":false}}]`},
		{stdout: ""},
		{stdout: ""},
		{stdout: ""},
	}}
	client := NewClientWithEnvironment(runner, inherited)
	ctx := context.Background()
	if _, err := client.CheckCLI(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := client.CheckDocker(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := client.CheckCompose(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := client.CheckBuildx(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := client.ImageExists(ctx, "image"); err != nil {
		t.Fatal(err)
	}
	if _, err := client.InspectContainer(ctx, "name"); err != nil {
		t.Fatal(err)
	}
	if _, err := client.ProjectContainers(ctx, "project"); err != nil {
		t.Fatal(err)
	}
	if _, err := client.ManagedProjectContainers(ctx, "hash", 42); err != nil {
		t.Fatal(err)
	}
	if err := client.RemoveContainer(ctx, "name"); err != nil {
		t.Fatal(err)
	}
	for _, command := range runner.captured {
		if !reflect.DeepEqual(command.Env, preserved) {
			t.Errorf("docker %v environment = %#v, want %#v", command.Args, command.Env, preserved)
		}
	}
}

func TestDirectAttachedDockerCommandSanitizesCallerEnvironment(t *testing.T) {
	preserved := []string{"PATH=/bin", "DOCKER_HOST=unix:///socket", "DOCKER_CONTEXT=local"}
	environment := append(append([]string(nil), preserved...), privateEnvironmentForTest("inherited")...)
	runner := &fakeRunner{attachedResults: []error{nil}}
	if err := NewClientWithEnvironment(runner, nil).Attached(context.Background(), []string{"exec", "name", "true"}, environment, nil, nil, nil); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(runner.attached[0].Env, preserved) {
		t.Fatalf("attached environment = %#v, want %#v", runner.attached[0].Env, preserved)
	}
}

func privateEnvironmentForTest(value string) []string {
	result := make([]string, 0, len(privateEnvironment))
	for name := range privateEnvironment {
		result = append(result, name+"="+value)
	}
	// Stable input makes equality failures easier to read.
	for i := 0; i < len(result); i++ {
		for j := i + 1; j < len(result); j++ {
			if strings.Compare(result[i], result[j]) > 0 {
				result[i], result[j] = result[j], result[i]
			}
		}
	}
	return result
}
