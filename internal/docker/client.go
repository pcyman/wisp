// Package docker provides typed, injectable access to the Docker CLI.
package docker

import (
	"context"
	"fmt"
	"io"
	"os"

	"wisp/internal/process"
)

// Client invokes Docker without a shell.
type Client struct {
	runner process.Runner
	path   string
	env    []string
}

// Attached runs a Docker command with caller-owned stdio.
func (c *Client) Attached(ctx context.Context, args []string, env []string, stdin io.Reader, stdout, stderr io.Writer) error {
	if c == nil || c.runner == nil {
		return fmt.Errorf("Docker process runner is not configured")
	}
	return c.runner.Attached(ctx, process.Command{Path: c.path, Args: args, Env: SanitizeEnvironment(env, nil), Stdin: stdin, Stdout: stdout, Stderr: stderr})
}

func NewClient(runner process.Runner) *Client {
	return NewClientWithEnvironment(runner, os.Environ())
}

// NewClientWithEnvironment configures the host environment used by direct
// Docker commands. Wisp's private Compose protocol variables are always
// stripped; ordinary Docker connection variables remain available.
func NewClientWithEnvironment(runner process.Runner, environment []string) *Client {
	return &Client{runner: runner, path: "docker", env: SanitizeEnvironment(environment, nil)}
}

// NewClientWithPath is useful when Docker is installed under a nonstandard
// executable name and in process-level tests.
func NewClientWithPath(runner process.Runner, path string) *Client {
	return &Client{runner: runner, path: path, env: SanitizeEnvironment(os.Environ(), nil)}
}

func (c *Client) capture(ctx context.Context, args []string, dir string, env []string) ([]byte, []byte, error) {
	if c == nil || c.runner == nil {
		return nil, nil, fmt.Errorf("Docker process runner is not configured")
	}
	if env == nil {
		env = c.env
	}
	return c.runner.Capture(ctx, process.Command{Path: c.path, Args: args, Dir: dir, Env: env})
}

func (c *Client) attached(ctx context.Context, args []string, dir string, env []string) error {
	if c == nil || c.runner == nil {
		return fmt.Errorf("Docker process runner is not configured")
	}
	if env == nil {
		env = c.env
	}
	return c.runner.Attached(ctx, process.Command{Path: c.path, Args: args, Dir: dir, Env: env})
}
