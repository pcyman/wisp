package process

import (
	"context"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestOSRunnerCapture(t *testing.T) {
	stdout, stderr, err := (OSRunner{}).Capture(context.Background(), Command{
		Path: os.Args[0],
		Args: []string{"-test.run=TestProcessHelper", "--", "one", "two words"},
		Env:  append(os.Environ(), "GO_WANT_PROCESS_HELPER=1"),
	})
	if err != nil {
		t.Fatal(err)
	}
	if string(stdout) != "one\ntwo words\n" || string(stderr) != "helper stderr\n" {
		t.Fatalf("stdout %q, stderr %q", stdout, stderr)
	}
}

func TestOSRunnerAttachedUsesProvidedStreams(t *testing.T) {
	var stdout, stderr strings.Builder
	err := (OSRunner{}).Attached(context.Background(), Command{
		Path:   os.Args[0],
		Args:   []string{"-test.run=TestProcessHelper", "--", "attached"},
		Env:    append(os.Environ(), "GO_WANT_PROCESS_HELPER=1"),
		Stdout: &stdout,
		Stderr: &stderr,
	})
	if err != nil {
		t.Fatal(err)
	}
	if stdout.String() != "attached\n" || stderr.String() != "helper stderr\n" {
		t.Fatalf("stdout %q, stderr %q", stdout.String(), stderr.String())
	}
}

func TestExitCode(t *testing.T) {
	cmd := exec.Command(os.Args[0], "-test.run=TestProcessHelper")
	cmd.Env = append(os.Environ(), "GO_WANT_PROCESS_HELPER_EXIT=1")
	err := cmd.Run()
	if got := ExitCode(err, 125); got != 23 {
		t.Fatalf("ExitCode = %d", got)
	}
	if got := ExitCode(context.Canceled, 125); got != 125 {
		t.Fatalf("fallback ExitCode = %d", got)
	}
}

func TestExitCodeForSignaledChildren(t *testing.T) {
	for _, test := range []struct {
		name   string
		signal syscall.Signal
		want   int
	}{{"SIGINT", syscall.SIGINT, 130}, {"SIGTERM", syscall.SIGTERM, 143}} {
		t.Run(test.name, func(t *testing.T) {
			cmd := exec.Command(os.Args[0], "-test.run=TestProcessHelper")
			cmd.Env = append(os.Environ(), "GO_WANT_PROCESS_SELF_SIGNAL="+test.signal.String())
			if got := ExitCode(cmd.Run(), 125); got != test.want {
				t.Fatalf("ExitCode = %d, want %d", got, test.want)
			}
		})
	}
}

func TestOSRunnerAttachedForwardsSignalToProcessGroup(t *testing.T) {
	ready := filepath.Join(t.TempDir(), "ready")
	var stdout strings.Builder
	done := make(chan error, 1)
	go func() {
		done <- (OSRunner{}).Attached(context.Background(), Command{
			Path:   os.Args[0],
			Args:   []string{"-test.run=TestProcessHelper"},
			Env:    append(os.Environ(), "GO_WANT_PROCESS_SIGNAL=1", "PROCESS_READY="+ready),
			Stdout: &stdout,
		})
	}()
	deadline := time.Now().Add(5 * time.Second)
	for {
		if _, err := os.Stat(ready); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("helper did not become ready")
		}
		time.Sleep(10 * time.Millisecond)
	}
	if err := syscall.Kill(os.Getpid(), syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("attached process did not receive SIGTERM")
	}
	if stdout.String() != "terminated\n" {
		t.Fatalf("stdout = %q", stdout.String())
	}
}

func TestProcessHelper(t *testing.T) {
	if signalName := os.Getenv("GO_WANT_PROCESS_SELF_SIGNAL"); signalName != "" {
		signalValue := syscall.SIGINT
		if signalName == syscall.SIGTERM.String() {
			signalValue = syscall.SIGTERM
		}
		_ = syscall.Kill(os.Getpid(), signalValue)
		time.Sleep(time.Second)
		os.Exit(2)
	}
	if os.Getenv("GO_WANT_PROCESS_HELPER_EXIT") == "1" {
		os.Exit(23)
	}
	if os.Getenv("GO_WANT_PROCESS_HELPER") != "1" {
		if os.Getenv("GO_WANT_PROCESS_SIGNAL") != "1" {
			return
		}
		signals := make(chan os.Signal, 1)
		signal.Notify(signals, syscall.SIGTERM)
		if err := os.WriteFile(os.Getenv("PROCESS_READY"), []byte("ready"), 0600); err != nil {
			os.Exit(2)
		}
		<-signals
		_, _ = os.Stdout.WriteString("terminated\n")
		os.Exit(0)
	}
	for _, arg := range os.Args {
		if arg == "--" {
			continue
		}
		if strings.HasPrefix(arg, "-test.") || arg == os.Args[0] {
			continue
		}
		_, _ = os.Stdout.WriteString(arg + "\n")
	}
	_, _ = os.Stderr.WriteString("helper stderr\n")
	os.Exit(0)
}
