package main

import (
	"context"
	"fmt"
	"io"
	"os"

	wisproot "wisp"
	"wisp/internal/app"
	"wisp/internal/cli"
	"wisp/internal/herdr"
	"wisp/internal/process"
	"wisp/internal/version"
)

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

type hostProcess struct {
	argv0       string
	environment []string
	executable  func() (string, error)
	exec        func(string, herdr.Plan) error
}

func run(args []string, stdout, stderr io.Writer) int {
	return runWithHost(args, stdout, stderr, hostProcess{
		argv0: os.Args[0], environment: os.Environ(), executable: os.Executable, exec: herdr.Exec,
	})
}

func runWithHost(args []string, stdout, stderr io.Writer, host hostProcess) int {
	req, err := cli.Parse(args)
	if err != nil {
		fmt.Fprintf(stderr, "error: %v\n", err)
		return 2
	}
	if req.ShowHelp {
		fmt.Fprint(stdout, cli.Help(req.Command))
		return 0
	}
	if req.Command == cli.CommandVersion {
		fmt.Fprintf(stdout, "wisp %s\n", version.String())
		return 0
	}
	if req.Command == cli.CommandHerdr {
		argv := append([]string{host.argv0}, args...)
		plan, planErr := herdr.BuildPlan(argv, host.environment)
		if planErr != nil {
			fmt.Fprintf(stderr, "error: prepare Herdr integration: %v\n", planErr)
			return 1
		}
		if plan.Required {
			executable, executableErr := host.executable()
			if executableErr != nil {
				fmt.Fprintf(stderr, "error: locate Wisp executable for Herdr integration: %v\n", executableErr)
				return 1
			}
			if execErr := host.exec(executable, plan); execErr != nil {
				fmt.Fprintf(stderr, "error: enable Herdr integration: %v\n", execErr)
				return 1
			}
			return 0
		}
		req.Command = cli.CommandRun
	}

	application, err := app.New(app.Dependencies{
		RuntimeAssets: wisproot.RuntimeAssets,
		Runner:        process.OSRunner{}, Stdin: os.Stdin, Stdout: stdout, Stderr: stderr,
		Environment: host.environment, UID: os.Getuid(), GID: os.Getgid(), Version: version.String(),
	})
	if err != nil {
		fmt.Fprintf(stderr, "error: %v\n", err)
		return 1
	}
	return application.Execute(context.Background(), req)
}
