package app

import (
	"context"
	"errors"
	"fmt"
	"strconv"

	"wisp/internal/cli"
	"wisp/internal/docker"
	"wisp/internal/lock"
	"wisp/internal/process"
	"wisp/internal/runtimeassets"
)

func (a *App) run(ctx context.Context, request cli.RunRequest) (status int, resultErr error) {
	plan, err := PlanRun(ctx, request, a.planOptions())
	if err != nil {
		return 1, err
	}
	for _, warning := range plan.Warnings {
		fmt.Fprintf(a.deps.Stderr, "warning: %s\n", warning)
	}

	runtimeRoot, err := lock.RuntimeRoot(a.env.XDGRuntimeDir, a.env.TempDir, plan.UID)
	if err != nil {
		return 1, err
	}
	projectLock, err := lock.Try(runtimeRoot, plan.Project.LockKey(plan.UID))
	if errors.Is(err, lock.ErrContended) {
		return 1, fmt.Errorf("another launch for this project is in progress or active")
	}
	if err != nil {
		return 1, fmt.Errorf("acquire project lock: %w", err)
	}
	defer func() {
		if closeErr := projectLock.Close(); closeErr != nil {
			status, resultErr = cleanupResult(status, resultErr, fmt.Errorf("release project lock: %w", closeErr), a.deps.Stderr)
		}
	}()
	openCodeDataMount, err := prepareOpenCodeDataMount(plan.OpenCodeDataRoot, plan.Project.Hash, plan.UID)
	if err != nil {
		return 1, err
	}
	// Put the data directory before the read-only auth-file overlay nested
	// beneath it. The project mount is always the first planned mount.
	sandboxMounts := make([]docker.Mount, 0, len(plan.Mounts)+1)
	sandboxMounts = append(sandboxMounts, plan.Mounts[0], openCodeDataMount)
	sandboxMounts = append(sandboxMounts, plan.Mounts[1:]...)

	assetRoot, err := runtimeassets.Materialize(a.deps.RuntimeAssets, runtimeassets.Environment{XDGCacheHome: a.env.XDGCacheHome, Home: a.env.Home})
	if err != nil {
		return 1, err
	}
	var brokerConfigSnapshot []byte
	if plan.AWSEnabled {
		brokerConfigSnapshot = plan.ConfigSnapshot
	}
	files, err := docker.CreateInvocationFiles(runtimeRoot, brokerConfigSnapshot, func(snapshot string) (docker.Override, error) {
		var credentials []docker.Mount
		if plan.AWSEnabled {
			credentials, err = docker.CredentialsMounts(snapshot, plan.AWSConfigPath, plan.AWSCredentialsPath, plan.AWSSSOCachePath)
			if err != nil {
				return docker.Override{}, err
			}
		}
		return docker.NewRunOverride(plan.Command, credentials, sandboxMounts)
	})
	if err != nil {
		return 1, err
	}
	defer func() {
		status, resultErr = cleanupResult(status, resultErr, files.Cleanup(), a.deps.Stderr)
	}()

	invocation := docker.ComposeInvocation{ProjectName: plan.Project.ComposeProject, RuntimeRoot: assetRoot, Override: files.OverridePath}
	if _, err := a.docker.CheckPrerequisites(ctx); err != nil {
		return 1, err
	}
	stale, err := a.inspectProject(ctx, plan.Project.ContainerName, plan.Project.ComposeProject, plan.Project.Hash, plan.UID)
	if err != nil {
		return 1, err
	}
	if stale {
		cleanupEnvironment := cloneEnvironment(plan.Environment)
		if plan.AWSEnabled {
			// The AWS-enabled override requires this interpolation even for `down`.
			// Use a non-secret placeholder before generating the per-run token.
			cleanupEnvironment["WISP_AWS_AUTHORIZATION_TOKEN"] = cleanupAuthorizationPlaceholder
		}
		invocation.Environment = a.childEnvironment(cleanupEnvironment)
		if _, stderr, err := a.docker.ComposeCapture(ctx, invocation, "down", "--remove-orphans"); err != nil {
			return 1, fmt.Errorf("clean stale Compose resources: %s: %w", string(stderr), err)
		}
	}
	if plan.AWSEnabled {
		if err := a.addAuthorizationToken(plan.Environment); err != nil {
			return 1, err
		}
	}
	invocation.Environment = a.childEnvironment(plan.Environment)
	images := []docker.Image{{Service: "sandbox", Name: plan.Images.Sandbox}}
	if plan.AWSEnabled {
		images = append(images, docker.Image{Service: "credentials", Name: plan.Images.Credentials})
	}
	if _, err := a.docker.EnsureImages(ctx, docker.BuildRequest{
		Invocation: invocation, LockRoot: runtimeRoot, UID: plan.UID, CPUs: plan.BuildCPUs, Rebuild: plan.Rebuild,
		Images: images,
	}); err != nil {
		return 1, a.redact(err, plan.Environment)
	}

	composeStarted := false
	defer func() {
		if !composeStarted {
			return
		}
		cleanupCtx, cancel := context.WithTimeout(context.Background(), a.deps.CleanupTimeout)
		defer cancel()
		cleanupErr := a.cleanupComposeProject(cleanupCtx, invocation, plan.Project.Hash, plan.UID, map[string]bool{"sandbox": true, "credentials": true}, plan.Project.ContainerName)
		status, resultErr = cleanupResult(status, resultErr, a.redact(cleanupErr, plan.Environment), a.deps.Stderr)
	}()

	if plan.AWSEnabled {
		fmt.Fprintln(a.deps.Stderr, "Starting AWS credential broker...")
		composeStarted = true
		if _, _, err := a.docker.ComposeCapture(ctx, invocation, "up", "--detach", "--wait", "credentials"); err != nil {
			logCtx, cancel := context.WithTimeout(context.Background(), a.deps.CleanupTimeout)
			logs, logErrors, _ := a.docker.ComposeCapture(logCtx, invocation, "logs", "--no-color", "--tail", "100", "credentials")
			cancel()
			if len(logs)+len(logErrors) > 0 {
				_, _ = fmt.Fprint(a.deps.Stderr, a.redactText(string(append(logs, logErrors...)), plan.Environment))
			}
			return 1, a.redact(fmt.Errorf("start AWS credential broker: %w; verify the configured or default AWS credential source", err), plan.Environment)
		}
	}

	fmt.Fprintln(a.deps.Stderr, "Starting OpenCode...")
	composeStarted = true
	args := []string{"run", "--rm", "--name", plan.Project.ContainerName, "--user", fmt.Sprintf("%d:%d", plan.UID, plan.GID), "--workdir", plan.Project.Workdir(), "--no-deps"}
	if !a.deps.IsTerminal(a.deps.Stdin, a.deps.Stdout) {
		args = append(args, "--no-TTY")
	}
	args = append(args, "sandbox")
	err = a.docker.ComposeAttachedIO(ctx, invocation, a.deps.Stdin, a.deps.Stdout, a.deps.Stderr, args...)
	if err != nil {
		return process.ExitCode(err, signalStatus(ctx, 1)), a.redact(err, plan.Environment)
	}
	return 0, nil
}

func (a *App) inspectProject(ctx context.Context, containerName, composeProject, hash string, uid int) (bool, error) {
	container, err := a.docker.InspectContainer(ctx, containerName)
	if err != nil {
		return false, err
	}
	if container != nil {
		if err := docker.VerifyLabels(container.Labels, composeProjectLabels(hash, uid, "sandbox", composeProject)); err != nil {
			return false, fmt.Errorf("refusing container name collision %q: %w", containerName, err)
		}
		if container.Running {
			return false, fmt.Errorf("sandbox %q is already running; use wisp hunk or wisp exec", containerName)
		}
		if err := a.docker.RemoveContainer(ctx, containerName); err != nil {
			return false, err
		}
	}
	containers, err := a.docker.ProjectContainers(ctx, composeProject)
	if err != nil {
		return false, err
	}
	for _, current := range containers {
		expected := composeProjectLabels(hash, uid, "", composeProject)
		if err := docker.VerifyLabels(current.Labels, expected); err != nil {
			return false, fmt.Errorf("refusing stale Compose cleanup because container %q is not owned by this project: %w", current.Name, err)
		}
		if kind := current.Labels["wisp.kind"]; kind != "sandbox" && kind != "credentials" {
			return false, fmt.Errorf("refusing stale Compose cleanup because container %q has invalid Wisp kind %q", current.Name, kind)
		}
	}
	return len(containers) > 0, nil
}

func (a *App) cleanupComposeProject(ctx context.Context, invocation docker.ComposeInvocation, hash string, uid int, allowedKinds map[string]bool, deterministicName string) error {
	validate := func(containers []docker.Container) error {
		for _, container := range containers {
			expected := composeProjectLabels(hash, uid, "", invocation.ProjectName)
			if err := docker.VerifyLabels(container.Labels, expected); err != nil {
				return fmt.Errorf("container %q has mismatched labels: %w", container.Name, err)
			}
			if kind := container.Labels["wisp.kind"]; !allowedKinds[kind] {
				return fmt.Errorf("container %q has invalid Wisp kind %q", container.Name, kind)
			}
		}
		return nil
	}

	containers, err := a.docker.ProjectContainers(ctx, invocation.ProjectName)
	if err != nil {
		return err
	}
	if err := validate(containers); err != nil {
		return fmt.Errorf("refusing Compose cleanup: %w", err)
	}
	_, stderr, cleanupErr := a.docker.ComposeCapture(ctx, invocation, "down", "--remove-orphans")
	if cleanupErr != nil {
		cleanupErr = fmt.Errorf("Compose cleanup: %s: %w", string(stderr), cleanupErr)
	}

	survivors, discoverErr := a.docker.ProjectContainers(ctx, invocation.ProjectName)
	if discoverErr != nil {
		return errors.Join(cleanupErr, discoverErr)
	}
	if err := validate(survivors); err != nil {
		return errors.Join(cleanupErr, fmt.Errorf("surviving resources were not removed because labels mismatch: %w", err))
	}
	seen := make(map[string]bool, len(survivors))
	for _, survivor := range survivors {
		seen[survivor.Name] = true
		cleanupErr = errors.Join(cleanupErr, a.docker.RemoveContainer(ctx, survivor.Name))
	}
	if deterministicName != "" && !seen[deterministicName] {
		survivor, inspectErr := a.docker.InspectContainer(ctx, deterministicName)
		if inspectErr != nil {
			return errors.Join(cleanupErr, inspectErr)
		}
		if survivor != nil {
			if labelErr := docker.VerifyLabels(survivor.Labels, composeProjectLabels(hash, uid, "sandbox", invocation.ProjectName)); labelErr != nil {
				return errors.Join(cleanupErr, fmt.Errorf("surviving container %q was not removed because its labels mismatch: %w", deterministicName, labelErr))
			}
			cleanupErr = errors.Join(cleanupErr, a.docker.RemoveContainer(ctx, deterministicName))
		}
	}
	return cleanupErr
}

func projectLabels(hash string, uid int, kind string) map[string]string {
	labels := map[string]string{"wisp.managed": "true", "wisp.owner-uid": strconv.Itoa(uid), "wisp.project-hash": hash}
	if kind != "" {
		labels["wisp.kind"] = kind
	}
	return labels
}

func composeProjectLabels(hash string, uid int, kind, composeProject string) map[string]string {
	labels := projectLabels(hash, uid, kind)
	labels["com.docker.compose.project"] = composeProject
	return labels
}

func cleanupResult(status int, primary, cleanup error, stderr interface{ Write([]byte) (int, error) }) (int, error) {
	if cleanup == nil {
		return status, primary
	}
	if primary != nil || status != 0 {
		_, _ = fmt.Fprintf(stderr, "warning: cleanup failed: %v\n", cleanup)
		return status, primary
	}
	return 1, cleanup
}

func signalStatus(ctx context.Context, fallback int) int {
	if errors.Is(ctx.Err(), context.Canceled) {
		return 130
	}
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return 143
	}
	return fallback
}
