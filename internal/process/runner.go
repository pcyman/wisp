// Package process provides the single seam through which Wisp starts external
// processes.
package process

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"syscall"
)

// Command describes a process without involving a shell. Args does not include
// Path.
type Command struct {
	Path   string
	Args   []string
	Dir    string
	Env    []string
	Stdin  io.Reader
	Stdout io.Writer
	Stderr io.Writer
}

// Runner supports buffered control commands and directly attached interactive
// commands.
type Runner interface {
	Capture(context.Context, Command) (stdout, stderr []byte, err error)
	Attached(context.Context, Command) error
}

// OSRunner executes commands with os/exec.
type OSRunner struct{}

func (OSRunner) Capture(ctx context.Context, command Command) ([]byte, []byte, error) {
	var stdout, stderr bytes.Buffer
	cmd := execCommand(ctx, command)
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	return stdout.Bytes(), stderr.Bytes(), err
}

// Attached runs the child in a separate process group and forwards SIGINT and
// SIGTERM to that group, leaving this process alive to perform cleanup.
func (OSRunner) Attached(ctx context.Context, command Command) error {
	cmd := execCommand(ctx, command)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error {
		if cmd.Process == nil {
			return os.ErrProcessDone
		}
		if err := syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL); err != nil {
			if errors.Is(err, syscall.ESRCH) {
				return os.ErrProcessDone
			}
			return err
		}
		return nil
	}
	if cmd.Stdin == nil {
		cmd.Stdin = os.Stdin
	}
	if cmd.Stdout == nil {
		cmd.Stdout = os.Stdout
	}
	if cmd.Stderr == nil {
		cmd.Stderr = os.Stderr
	}
	if err := cmd.Start(); err != nil {
		return err
	}

	signals := make(chan os.Signal, 2)
	signal.Notify(signals, os.Interrupt, syscall.SIGTERM)
	done := make(chan struct{})
	go func() {
		defer close(done)
		for sig := range signals {
			unixSignal, ok := sig.(syscall.Signal)
			if ok {
				_ = syscall.Kill(-cmd.Process.Pid, unixSignal)
			}
		}
	}()

	err := cmd.Wait()
	signal.Stop(signals)
	close(signals)
	<-done
	return err
}

func execCommand(ctx context.Context, command Command) *exec.Cmd {
	cmd := exec.CommandContext(ctx, command.Path, command.Args...)
	cmd.Dir = command.Dir
	cmd.Env = command.Env
	cmd.Stdin = command.Stdin
	cmd.Stdout = command.Stdout
	cmd.Stderr = command.Stderr
	return cmd
}

// ExitCode returns a child's exit status. It returns fallback for failures that
// do not contain an exit status, such as failure to find an executable.
func ExitCode(err error, fallback int) int {
	if err == nil {
		return 0
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		if status, ok := exitErr.Sys().(syscall.WaitStatus); ok && status.Signaled() {
			return 128 + int(status.Signal())
		}
		if code := exitErr.ExitCode(); code >= 0 {
			return code
		}
	}
	var status interface{ ExitCode() int }
	if errors.As(err, &status) {
		return status.ExitCode()
	}
	return fallback
}
