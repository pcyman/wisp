package app

import (
	"context"
	"fmt"

	"wisp/internal/docker"
	"wisp/internal/process"
	"wisp/internal/project"
)

func (a *App) exec(ctx context.Context, directory string, payload []string) (int, error) {
	if len(payload) == 0 {
		return 2, fmt.Errorf("command payload is empty")
	}
	resolved, err := project.Resolve(ctx, directory, a.deps.InvocationDir, a.deps.UID, a.gitRoot)
	if err != nil {
		return 1, err
	}
	if _, err := a.docker.CheckDocker(ctx); err != nil {
		return 1, err
	}
	container, err := a.docker.InspectContainer(ctx, resolved.ContainerName)
	if err != nil {
		return 1, err
	}
	if container == nil || !container.Running {
		return 1, fmt.Errorf("no running Wisp sandbox for %q", resolved.RootDir)
	}
	expected := composeProjectLabels(resolved.Hash, a.deps.UID, "sandbox", resolved.ComposeProject)
	if err := docker.VerifyLabels(container.Labels, expected); err != nil {
		return 1, fmt.Errorf("refusing container name collision %q: %w", resolved.ContainerName, err)
	}
	args := []string{"exec", "--interactive", "--user", fmt.Sprintf("%d:%d", a.deps.UID, a.deps.GID), "--workdir", resolved.Workdir()}
	if a.deps.IsTerminal(a.deps.Stdin, a.deps.Stdout) {
		args = append(args, "--tty")
	}
	args = append(args, resolved.ContainerName)
	args = append(args, payload...)
	err = a.docker.Attached(ctx, args, a.childEnvironment(nil), a.deps.Stdin, a.deps.Stdout, a.deps.Stderr)
	if err != nil {
		return process.ExitCode(err, 1), err
	}
	return 0, nil
}

func (a *App) gitRoot(ctx context.Context, dir string) (string, error) {
	stdout, _, err := a.deps.Runner.Capture(ctx, process.Command{Path: "git", Args: []string{"-C", dir, "rev-parse", "--show-toplevel"}, Env: a.childEnvironment(nil)})
	return string(stdout), err
}
