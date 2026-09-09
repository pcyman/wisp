package app

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"io"
	"io/fs"
	"os"
	"strings"
	"time"

	"golang.org/x/term"

	"wisp/internal/cli"
	"wisp/internal/docker"
	"wisp/internal/process"
	"wisp/internal/version"
)

// Dependencies contains host integration points. Tests can replace every
// external-process and terminal decision without starting Docker.
type Dependencies struct {
	RuntimeAssets  fs.FS
	Runner         process.Runner
	Stdin          io.Reader
	Stdout         io.Writer
	Stderr         io.Writer
	Environment    []string
	InvocationDir  string
	UID            int
	GID            int
	Version        string
	Random         io.Reader
	IsTerminal     func(io.Reader, io.Writer) bool
	CleanupTimeout time.Duration
}

type App struct {
	deps   Dependencies
	docker *docker.Client
	env    HostEnvironment
}

const cleanupAuthorizationPlaceholder = "Basic 0000000000000000000000000000000000000000000000000000000000000000"

func New(deps Dependencies) (*App, error) {
	if deps.Runner == nil {
		deps.Runner = process.OSRunner{}
	}
	if deps.Stdin == nil {
		deps.Stdin = os.Stdin
	}
	if deps.Stdout == nil {
		deps.Stdout = os.Stdout
	}
	if deps.Stderr == nil {
		deps.Stderr = os.Stderr
	}
	if deps.Environment == nil {
		deps.Environment = os.Environ()
	}
	if deps.InvocationDir == "" {
		cwd, err := os.Getwd()
		if err != nil {
			return nil, fmt.Errorf("determine invocation directory: %w", err)
		}
		deps.InvocationDir = cwd
	}
	if deps.UID == 0 && os.Getuid() != 0 {
		deps.UID = os.Getuid()
	}
	if deps.GID == 0 && os.Getgid() != 0 {
		deps.GID = os.Getgid()
	}
	if deps.Version == "" {
		deps.Version = version.String()
	}
	if deps.CleanupTimeout <= 0 {
		deps.CleanupTimeout = 15 * time.Second
	}
	if deps.IsTerminal == nil {
		deps.IsTerminal = func(stdin io.Reader, stdout io.Writer) bool {
			in, inOK := stdin.(interface{ Fd() uintptr })
			out, outOK := stdout.(interface{ Fd() uintptr })
			return inOK && outOK && term.IsTerminal(int(in.Fd())) && term.IsTerminal(int(out.Fd()))
		}
	}
	return &App{deps: deps, docker: docker.NewClientWithEnvironment(deps.Runner, deps.Environment), env: hostEnvironment(deps.Environment)}, nil
}

// Execute dispatches one parsed request and returns the process status.
func (a *App) Execute(ctx context.Context, request cli.Request) int {
	var status int
	var err error
	switch request.Command {
	case cli.CommandAgents:
		status, err = a.agents(ctx, request.Agents)
	case cli.CommandConfigPath, cli.CommandConfigInit, cli.CommandConfigValidate:
		status, err = a.config(request)
	case cli.CommandRun:
		status, err = a.run(ctx, request.Run)
	case cli.CommandExec:
		status, err = a.exec(ctx, request.Exec.Directory, request.Exec.Command)
	case cli.CommandHunk:
		status, err = a.exec(ctx, request.Hunk.Directory, append([]string{"hunk"}, request.Hunk.Args...))
	case cli.CommandAWSCheck:
		status, err = a.awsCheck(ctx, request.AWSCheck)
	case cli.CommandDoctor:
		return a.doctor(ctx, request.Config.Path)
	default:
		return 2
	}
	if err != nil {
		fmt.Fprintf(a.deps.Stderr, "error: %v\n", err)
	}
	return status
}

func hostEnvironment(values []string) HostEnvironment {
	lookup := func(name string) string {
		prefix := name + "="
		for i := len(values) - 1; i >= 0; i-- {
			if strings.HasPrefix(values[i], prefix) {
				return strings.TrimPrefix(values[i], prefix)
			}
		}
		return ""
	}
	return HostEnvironment{
		Home: lookup("HOME"), XDGConfigHome: lookup("XDG_CONFIG_HOME"), XDGDataHome: lookup("XDG_DATA_HOME"),
		XDGCacheHome: lookup("XDG_CACHE_HOME"), XDGRuntimeDir: lookup("XDG_RUNTIME_DIR"),
		TempDir: lookup("TMPDIR"), Term: lookup("TERM"), ColorTerm: lookup("COLORTERM"),
	}
}

func (a *App) planOptions() PlanOptions {
	return PlanOptions{InvocationDir: a.deps.InvocationDir, UID: a.deps.UID, GID: a.deps.GID, CLIVersion: a.deps.Version, Environment: a.env, Runner: a.deps.Runner, ProcessEnv: a.childEnvironment(nil)}
}

func (a *App) childEnvironment(planned map[string]string) []string {
	return docker.SanitizeEnvironment(a.deps.Environment, planned)
}

func cloneEnvironment(source map[string]string) map[string]string {
	cloned := make(map[string]string, len(source)+1)
	for key, value := range source {
		cloned[key] = value
	}
	return cloned
}

func (a *App) redact(err error, environment map[string]string) error {
	if err == nil {
		return nil
	}
	return fmt.Errorf("%s", a.redactText(err.Error(), environment))
}

func (a *App) redactText(message string, environment map[string]string) string {
	if token := environment["WISP_AWS_AUTHORIZATION_TOKEN"]; token != "" {
		message = strings.ReplaceAll(message, token, "[REDACTED]")
	}
	return message
}

func (a *App) addAuthorizationToken(environment map[string]string) error {
	random := a.deps.Random
	if random == nil {
		random = rand.Reader
	}
	token := make([]byte, 32)
	if _, err := io.ReadFull(random, token); err != nil {
		return fmt.Errorf("generate AWS authorization token: %w", err)
	}
	environment["WISP_AWS_AUTHORIZATION_TOKEN"] = "Basic " + hex.EncodeToString(token)
	return nil
}
