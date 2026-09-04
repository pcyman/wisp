package docker

import (
	"context"
	"fmt"

	"wisp/internal/process"
)

type fakeResult struct {
	stdout string
	stderr string
	err    error
}

type fakeRunner struct {
	captureResults  []fakeResult
	attachedResults []error
	captured        []process.Command
	attached        []process.Command
}

func (f *fakeRunner) Capture(_ context.Context, command process.Command) ([]byte, []byte, error) {
	f.captured = append(f.captured, command)
	if len(f.captureResults) == 0 {
		return nil, nil, fmt.Errorf("unexpected capture: %v", command.Args)
	}
	result := f.captureResults[0]
	f.captureResults = f.captureResults[1:]
	return []byte(result.stdout), []byte(result.stderr), result.err
}

func (f *fakeRunner) Attached(_ context.Context, command process.Command) error {
	f.attached = append(f.attached, command)
	if len(f.attachedResults) == 0 {
		return fmt.Errorf("unexpected attached command: %v", command.Args)
	}
	result := f.attachedResults[0]
	f.attachedResults = f.attachedResults[1:]
	return result
}
