package main

import (
	"context"
	"fmt"
	"io"
	"os"

	wisproot "wisp"
	"wisp/internal/app"
	"wisp/internal/cli"
	"wisp/internal/process"
	"wisp/internal/version"
)

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

func run(args []string, stdout, stderr io.Writer) int {
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

	application, err := app.New(app.Dependencies{
		RuntimeAssets: wisproot.RuntimeAssets,
		Runner:        process.OSRunner{}, Stdin: os.Stdin, Stdout: stdout, Stderr: stderr,
		Environment: os.Environ(), UID: os.Getuid(), GID: os.Getgid(), Version: version.String(),
	})
	if err != nil {
		fmt.Fprintf(stderr, "error: %v\n", err)
		return 1
	}
	return application.Execute(context.Background(), req)
}
