package docker

import (
	"context"
	"errors"
	"path/filepath"
	"reflect"
	"sync"
	"testing"
	"time"

	"wisp/internal/lock"
	"wisp/internal/process"
)

func TestEnsureImagesBuildsMissingAndCreatesBuilder(t *testing.T) {
	runner := &fakeRunner{
		captureResults: []fakeResult{
			{stderr: "No such image", err: errors.New("exit 1")},
			{stdout: "[]"},
			{stderr: "No such image", err: errors.New("exit 1")},
			{stdout: "github.com/docker/buildx v0.11.0"},
			{stderr: "no builder found", err: errors.New("exit 1")},
			{stdout: "builder"},
			{stdout: "bootstrapped"},
			{stdout: "removed"},
		},
		attachedResults: []error{nil},
	}
	root := t.TempDir()
	invocation := ComposeInvocation{ProjectName: "wisp-7-hash", RuntimeRoot: root, Override: filepath.Join(root, "override.yaml"), Environment: []string{"WISP_UID=7"}}
	built, err := NewClient(runner).EnsureImages(context.Background(), BuildRequest{
		Invocation: invocation,
		LockRoot:   root,
		UID:        7,
		CPUs:       3,
		Images:     []Image{{Service: "sandbox", Name: "wisp:local"}, {Service: "credentials", Name: "credentials:local"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(built, []string{"sandbox"}) {
		t.Fatalf("built = %v", built)
	}
	if got := runner.captured[5].Args; !reflect.DeepEqual(got, []string{"buildx", "create", "--name", "wisp-7-3cpu", "--driver", "docker-container", "--driver-opt", "cpu-period=100000", "--driver-opt", "cpu-quota=300000"}) {
		t.Fatalf("create args = %v", got)
	}
	wantBuildSuffix := []string{"build", "--builder", "wisp-7-3cpu", "sandbox"}
	gotBuild := runner.attached[0].Args
	if !reflect.DeepEqual(gotBuild[len(gotBuild)-len(wantBuildSuffix):], wantBuildSuffix) {
		t.Fatalf("build args = %v", gotBuild)
	}
	if got := runner.captured[len(runner.captured)-1].Args; !reflect.DeepEqual(got, []string{"buildx", "rm", "--keep-state", "--force", "wisp-7-3cpu"}) {
		t.Fatalf("remove args = %v", got)
	}
}

func TestEnsureImagesRemovesBuilderAfterBuildFailure(t *testing.T) {
	runner := &fakeRunner{
		captureResults: []fakeResult{
			{stdout: "github.com/docker/buildx v0.11.0"},
			{stdout: "Name: wisp-10-2cpu\nDriver: docker-container\n"},
			{stdout: "bootstrapped"},
			{stdout: "removed"},
		},
		attachedResults: []error{errors.New("build failed")},
	}
	root := t.TempDir()
	_, err := NewClient(runner).EnsureImages(context.Background(), BuildRequest{
		Invocation: ComposeInvocation{ProjectName: "project", RuntimeRoot: root, Override: filepath.Join(root, "override.yaml")},
		LockRoot:   root, UID: 10, CPUs: 2, Rebuild: true,
		Images: []Image{{Service: "credentials", Name: "credentials:local"}},
	})
	if err == nil {
		t.Fatal("build failure was ignored")
	}
	if got := runner.captured[len(runner.captured)-1].Args; !reflect.DeepEqual(got, []string{"buildx", "rm", "--keep-state", "--force", "wisp-10-2cpu"}) {
		t.Fatalf("final command = %v (build error: %v)", got, err)
	}
}

func TestBuilderDriver(t *testing.T) {
	driver, err := builderDriver([]byte("Name: builder\nDriver:        docker-container\nNodes:\n"))
	if err != nil || driver != "docker-container" {
		t.Fatalf("driver = %q, error = %v", driver, err)
	}
	if _, err := builderDriver([]byte("Name: builder\nNodes:\n")); err == nil {
		t.Fatal("missing driver was accepted")
	}
}

func TestEnsureImagesSkipsBuildWhenPresent(t *testing.T) {
	runner := &fakeRunner{captureResults: []fakeResult{{stdout: "[]"}}}
	built, err := NewClient(runner).EnsureImages(context.Background(), BuildRequest{
		LockRoot: t.TempDir(), UID: 1, CPUs: 1,
		Images: []Image{{Service: "sandbox", Name: "wisp:local"}},
	})
	if err != nil || built != nil {
		t.Fatalf("built = %v, error = %v", built, err)
	}
	if len(runner.attached) != 0 {
		t.Fatal("unexpected build")
	}
}

func TestEnsureImagesRechecksMissingImageAfterContendedLock(t *testing.T) {
	root := t.TempDir()
	held, err := lock.Try(root, "build-1")
	if err != nil {
		t.Fatal(err)
	}
	runner := &imageAppearsRunner{firstInspect: make(chan struct{})}
	done := make(chan struct{})
	var built []string
	var ensureErr error
	go func() {
		built, ensureErr = NewClient(runner).EnsureImages(context.Background(), BuildRequest{
			LockRoot: root, UID: 1, CPUs: 1,
			Images: []Image{{Service: "sandbox", Name: "wisp:local"}},
		})
		close(done)
	}()
	<-runner.firstInspect
	select {
	case <-done:
		t.Fatal("EnsureImages did not wait for the build lock")
	case <-time.After(50 * time.Millisecond):
	}
	if err := held.Close(); err != nil {
		t.Fatal(err)
	}
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("EnsureImages did not finish after releasing the build lock")
	}
	if ensureErr != nil || built != nil {
		t.Fatalf("built = %v, error = %v", built, ensureErr)
	}
	if runner.inspectCount() != 2 {
		t.Fatalf("image inspect count = %d, want pre-lock and post-lock checks", runner.inspectCount())
	}
}

type imageAppearsRunner struct {
	mu           sync.Mutex
	inspects     int
	firstInspect chan struct{}
}

func (r *imageAppearsRunner) Capture(_ context.Context, command process.Command) ([]byte, []byte, error) {
	r.mu.Lock()
	r.inspects++
	count := r.inspects
	r.mu.Unlock()
	if count == 1 {
		close(r.firstInspect)
		return nil, []byte("No such image"), errors.New("exit 1")
	}
	return []byte("[]"), nil, nil
}

func (*imageAppearsRunner) Attached(context.Context, process.Command) error {
	return errors.New("unexpected build")
}

func (r *imageAppearsRunner) inspectCount() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.inspects
}

func TestEnsureImagesRemovesAfterBuilderCreateFailure(t *testing.T) {
	runner := &fakeRunner{captureResults: []fakeResult{
		{stdout: "github.com/docker/buildx v0.11.0"},
		{stderr: "no builder found", err: errors.New("exit 1")},
		{stderr: "create failed", err: errors.New("exit 1")},
		{stdout: "removed"},
	}}
	root := t.TempDir()
	_, err := NewClient(runner).EnsureImages(context.Background(), BuildRequest{
		Invocation: ComposeInvocation{ProjectName: "project", RuntimeRoot: root, Override: filepath.Join(root, "override.yaml")},
		LockRoot:   root, UID: 10, CPUs: 2, Rebuild: true,
		Images: []Image{{Service: "credentials", Name: "credentials:local"}},
	})
	if err == nil {
		t.Fatal("builder creation failure was ignored")
	}
	if got := runner.captured[len(runner.captured)-1].Args; !reflect.DeepEqual(got, []string{"buildx", "rm", "--keep-state", "--force", "wisp-10-2cpu"}) {
		t.Fatalf("final command = %v", got)
	}
}
