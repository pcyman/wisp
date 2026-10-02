package app

import (
	"context"
	"fmt"
	"time"

	"wisp/internal/docker"
)

type centralSandbox struct {
	ID     string
	Labels map[string]string
}

func (a *App) waitCentralSandbox(ctx context.Context, plan SandboxPlan, runID string, ready func(centralSandbox)) (int, error) {
	expected := composeProjectLabels(plan.Project.Hash, plan.UID, "sandbox", plan.Project.ComposeProject)
	expected["wisp.run-id"] = runID
	startDeadline := time.Now().Add(30 * time.Second)
	started := false
	ticker := time.NewTicker(200 * time.Millisecond)
	defer ticker.Stop()
	for {
		if ctx.Err() != nil {
			// Cancellation is an intentional stop. Deferred cleanup still runs.
			return 0, nil
		}
		checkCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
		container, err := a.docker.InspectContainer(checkCtx, plan.Project.ContainerName)
		if err == nil && container != nil {
			err = docker.VerifyLabels(container.Labels, expected)
		}
		if err != nil {
			cancel()
			if ctx.Err() != nil {
				return 0, nil
			}
			return 1, fmt.Errorf("verify central sandbox: %w", err)
		}
		if container == nil || !container.Running {
			if container != nil && !started {
				logs, logErrors, _ := a.docker.Capture(checkCtx, "logs", "--tail", "30", container.ID)
				if len(logs)+len(logErrors) > 0 {
					fmt.Fprint(a.deps.Stderr, a.redactText(string(append(logs, logErrors...)), plan.Environment))
				}
			}
			cancel()
			if container != nil && container.ExitCode != 0 {
				if !started && container.ExitCode == 127 {
					return 127, fmt.Errorf("central session command unavailable; rebuild the sandbox image with wisp --rebuild")
				}
				return container.ExitCode, fmt.Errorf("agent container exited with status %d", container.ExitCode)
			}
			if !started {
				return 1, fmt.Errorf("central sandbox exited before its terminal session was ready")
			}
			return 0, nil
		}
		if !started {
			_, _, err = a.docker.Capture(checkCtx, "exec", container.ID, "tmux", "has-session", "-t", "wisp")
			if err == nil {
				started = true
				ready(centralSandbox{ID: container.ID, Labels: expected})
			} else if time.Now().After(startDeadline) {
				cancel()
				return 1, fmt.Errorf("central tmux session did not start; rebuild the sandbox image with wisp --rebuild")
			}
		}
		cancel()
		delay := 200 * time.Millisecond
		if started {
			delay = time.Second
		}
		ticker.Reset(delay)
		select {
		case <-ctx.Done():
			return 0, nil
		case <-ticker.C:
		}
	}
}

func (a *App) centralAttachmentArgs(ctx context.Context, sandbox centralSandbox) ([]string, error) {
	container, err := a.docker.InspectContainer(ctx, sandbox.ID)
	if err != nil {
		return nil, err
	}
	if container == nil || !container.Running {
		return nil, fmt.Errorf("central sandbox is no longer running")
	}
	if err := docker.VerifyLabels(container.Labels, sandbox.Labels); err != nil {
		return nil, fmt.Errorf("refusing central attachment: %w", err)
	}
	return []string{
		"exec", "--interactive", "--tty", "--user", fmt.Sprintf("%d:%d", a.deps.UID, a.deps.GID),
		sandbox.ID, "tmux", "-u", "attach-session", "-t", "wisp",
	}, nil
}

func (a *App) attachCentral(ctx context.Context, sandbox centralSandbox) error {
	args, err := a.centralAttachmentArgs(ctx, sandbox)
	if err != nil {
		return err
	}
	return a.docker.Attached(ctx, args, a.childEnvironment(nil), a.deps.Stdin, a.deps.Stdout, a.deps.Stderr)
}
