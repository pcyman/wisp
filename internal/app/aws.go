package app

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strconv"

	"wisp/internal/cli"
	"wisp/internal/config"
	"wisp/internal/docker"
	"wisp/internal/lock"
	"wisp/internal/runtimeassets"
)

func (a *App) awsCheck(ctx context.Context, request cli.AWSCheckRequest) (status int, resultErr error) {
	configEnv := config.Environment{Home: a.env.Home, XDGConfigHome: a.env.XDGConfigHome, XDGDataHome: a.env.XDGDataHome}
	configPath, err := config.ResolvePath(request.ConfigPath, a.deps.InvocationDir, configEnv)
	if err != nil {
		return 1, err
	}
	loaded, err := config.Load(configPath, configEnv)
	if err != nil {
		return 1, err
	}
	alias, err := config.SelectAWSAlias(loaded.Config, request.Alias)
	if err != nil {
		return 1, err
	}
	digest := sha256.Sum256([]byte("wisp-aws-check\x00" + loaded.Path + "\x00" + alias))
	hash := hex.EncodeToString(digest[:])
	composeProject := "wisp-check-" + strconv.Itoa(a.deps.UID) + "-" + hash[:16]
	runtimeRoot, err := lock.RuntimeRoot(a.env.XDGRuntimeDir, a.env.TempDir, a.deps.UID)
	if err != nil {
		return 1, err
	}
	checkLock, err := lock.Try(runtimeRoot, fmt.Sprintf("wisp-%d-aws-check-%s", a.deps.UID, hash))
	if errors.Is(err, lock.ErrContended) {
		return 1, fmt.Errorf("another AWS check for alias %q is in progress", alias)
	}
	if err != nil {
		return 1, err
	}
	defer func() {
		status, resultErr = cleanupResult(status, resultErr, checkLock.Close(), a.deps.Stderr)
	}()

	assetRoot, err := runtimeassets.Materialize(a.deps.RuntimeAssets, runtimeassets.Environment{XDGCacheHome: a.env.XDGCacheHome, Home: a.env.Home})
	if err != nil {
		return 1, err
	}
	planned := plannedEnvironment(a.planOptions(), loaded.Config, alias, hash)
	files, err := docker.CreateInvocationFiles(runtimeRoot, loaded.Snapshot, func(snapshot string) (docker.Override, error) {
		mounts, err := docker.CredentialsMounts(snapshot, loaded.Config.AWS.HostConfigPath, loaded.Config.AWS.SSOCachePath)
		if err != nil {
			return docker.Override{}, err
		}
		return docker.NewCredentialsOverride(mounts)
	})
	if err != nil {
		return 1, err
	}
	defer func() { status, resultErr = cleanupResult(status, resultErr, files.Cleanup(), a.deps.Stderr) }()
	invocation := docker.ComposeInvocation{ProjectName: composeProject, RuntimeRoot: assetRoot, Override: files.OverridePath}

	if _, err := a.docker.CheckPrerequisites(ctx); err != nil {
		return 1, err
	}
	containers, err := a.docker.ProjectContainers(ctx, composeProject)
	if err != nil {
		return 1, err
	}
	for _, container := range containers {
		expected := composeProjectLabels(hash, a.deps.UID, "credentials", composeProject)
		if err := docker.VerifyLabels(container.Labels, expected); err != nil {
			return 1, fmt.Errorf("refusing stale AWS check cleanup because container %q has mismatched labels: %w", container.Name, err)
		}
	}
	if len(containers) > 0 {
		cleanupEnvironment := cloneEnvironment(planned)
		cleanupEnvironment["WISP_AWS_AUTHORIZATION_TOKEN"] = cleanupAuthorizationPlaceholder
		invocation.Environment = a.childEnvironment(cleanupEnvironment)
		if _, stderr, err := a.docker.ComposeCapture(ctx, invocation, "down", "--remove-orphans"); err != nil {
			return 1, fmt.Errorf("clean stale AWS check resources: %s: %w", string(stderr), err)
		}
	}
	if err := a.addAuthorizationToken(planned); err != nil {
		return 1, err
	}
	invocation.Environment = a.childEnvironment(planned)
	if _, err := a.docker.EnsureImages(ctx, docker.BuildRequest{Invocation: invocation, LockRoot: runtimeRoot, UID: a.deps.UID, CPUs: loaded.Config.Build.CPUs, Rebuild: request.Rebuild, Images: []docker.Image{{Service: "credentials", Name: loaded.Config.Images.Credentials}}}); err != nil {
		return 1, a.redact(err, planned)
	}

	started := false
	defer func() {
		if !started {
			return
		}
		cleanupCtx, cancel := context.WithTimeout(context.Background(), a.deps.CleanupTimeout)
		defer cancel()
		err := a.cleanupComposeProject(cleanupCtx, invocation, hash, a.deps.UID, map[string]bool{"credentials": true}, "")
		status, resultErr = cleanupResult(status, resultErr, a.redact(err, planned), a.deps.Stderr)
	}()
	fmt.Fprintln(a.deps.Stderr, "Starting AWS credential broker...")
	started = true
	if _, _, err := a.docker.ComposeCapture(ctx, invocation, "up", "--detach", "--wait", "credentials"); err != nil {
		logCtx, cancel := context.WithTimeout(context.Background(), a.deps.CleanupTimeout)
		stdout, stderr, _ := a.docker.ComposeCapture(logCtx, invocation, "logs", "--no-color", "--tail", "100", "credentials")
		cancel()
		_, _ = fmt.Fprint(a.deps.Stderr, a.redactText(string(append(stdout, stderr...)), planned))
		profile := loaded.Config.AWS.Aliases[alias].Profile
		return 1, a.redact(fmt.Errorf("AWS credential check failed: %w; run aws sso login --profile %s", err, profile), planned)
	}
	fmt.Fprintf(a.deps.Stdout, "AWS credential check succeeded for alias %s\n", alias)
	return 0, nil
}
