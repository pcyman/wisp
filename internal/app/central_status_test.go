package app

import (
	"bytes"
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	"wisp/internal/agentstatus"
	"wisp/internal/project"
)

func centralStatusFixture() []centralProject {
	return []centralProject{{name: "app", state: "running", plan: SandboxPlan{Project: project.Project{RootDir: "/app", ContainerName: "sandbox"}, AgentKey: "pi", AgentName: "Pi"}, sandbox: centralSandbox{Labels: map[string]string{agentstatus.RunLabel: "current"}}, log: &centralLog{}}}
}

func TestCentralActivityTracksVerifiedReports(t *testing.T) {
	projects := centralStatusFixture()
	if centralActivity(projects[0]) != "unknown" {
		t.Fatal("manufactured activity before a report")
	}
	for _, test := range []struct{ state, reason, reporter, want string }{
		{"working", "", "ready", "working"},
		{"waiting", "question", "ready", "waiting (question)"},
		{"working", "retry", "ready", "working (retry)"},
		{"idle", "", "ready", "idle"},
		{"done", "", "ready", "done"},
		{"failed", "", "ready", "failed"},
		{"unknown", "", "missing", "unknown (report missing)"},
		{"unknown", "", "invalid", "unknown (report invalid)"},
		{"unknown", "", "unsupported", "unknown (report unsupported)"},
	} {
		entry := agentSnapshotEntry{ID: "current", SandboxID: "sandbox", Repo: "/app", Agent: "pi", State: test.state, Reason: test.reason, Reporter: test.reporter}
		if !applyCentralStatus(projects, centralStatusUpdate{runs: []string{"current"}, snapshot: agentSnapshot{Agents: []agentSnapshotEntry{entry}}}) {
			t.Fatal("update not applied")
		}
		if got := centralActivity(projects[0]); got != test.want {
			t.Fatalf("activity=%q want %q", got, test.want)
		}
		var out bytes.Buffer
		if err := renderCentral(&out, projects, []int{0}, 0, "", "", false, false, false, 100, 24); err != nil {
			t.Fatal(err)
		}
		if !bytes.Contains(out.Bytes(), []byte(test.want)) || !bytes.Contains(out.Bytes(), []byte("ACTIVITY")) {
			t.Fatalf("render=%q", out.String())
		}
	}
	projects[0].state = "closed"
	if centralActivity(projects[0]) != "-" || len(centralStatusRuns(projects)) != 0 {
		t.Fatal("closed project reports activity")
	}
}

func TestCentralActivityRejectsOldRunsAndMismatchedProjects(t *testing.T) {
	projects := centralStatusFixture()
	projects[0].activity = agentstatus.Report{State: "idle", Reporter: "ready"}
	old := centralStatusUpdate{runs: []string{"old"}, snapshot: agentSnapshot{Agents: []agentSnapshotEntry{{ID: "old", State: "working"}}}}
	if applyCentralStatus(projects, old) || centralActivity(projects[0]) != "idle" {
		t.Fatal("old run overwrote current activity")
	}
	for _, mismatch := range []string{"sandbox", "repo", "agent"} {
		entry := agentSnapshotEntry{ID: "current", SandboxID: "sandbox", Repo: "/app", Agent: "pi", State: "working"}
		switch mismatch {
		case "sandbox":
			entry.SandboxID = "other"
		case "repo":
			entry.Repo = "/other"
		case "agent":
			entry.Agent = "opencode"
		}
		applyCentralStatus(projects, centralStatusUpdate{runs: []string{"current"}, snapshot: agentSnapshot{Agents: []agentSnapshotEntry{entry}}})
		if centralActivity(projects[0]) != "unknown" {
			t.Fatalf("accepted mismatched %s", mismatch)
		}
	}
	projects[0].state = "stopping"
	if applyCentralStatus(projects, centralStatusUpdate{runs: []string{"current"}}) {
		t.Fatal("updated stopping project")
	}
}

func TestCentralActivityMarksCollectionErrorsStaleAndRecovers(t *testing.T) {
	projects := centralStatusFixture()
	projects[0].activity = agentstatus.Report{State: "working", Reporter: "ready"}
	applyCentralStatus(projects, centralStatusUpdate{runs: []string{"current"}, err: errors.New("Docker unavailable")})
	if got := centralActivity(projects[0]); got != "working (stale)" {
		t.Fatalf("got=%q", got)
	}
	applyCentralStatus(projects, centralStatusUpdate{runs: []string{"current"}, snapshot: agentSnapshot{Agents: []agentSnapshotEntry{{ID: "current", SandboxID: "sandbox", Repo: "/app", Agent: "pi", State: "idle", Reporter: "ready"}}}})
	if projects[0].activityStale || centralActivity(projects[0]) != "idle" {
		t.Fatal("did not recover")
	}
	applyCentralStatus(projects, centralStatusUpdate{runs: []string{"current"}})
	if centralActivity(projects[0]) != "unknown" {
		t.Fatal("missing verified container became idle or kept old status")
	}
	applyCentralStatus(projects, centralStatusUpdate{runs: []string{"current"}, err: errors.New("timeout")})
	if centralActivity(projects[0]) != "unknown (unavailable)" {
		t.Fatal("missing collection failure")
	}
}

func TestPollCentralStatusIdleLatestValueAndNonfatalErrors(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	requests := make(chan []string, 1)
	updates := make(chan centralStatusUpdate, 1)
	ticks := make(chan time.Time)
	calls := make(chan int, 4)
	done := make(chan struct{})
	go func() {
		defer close(done)
		count := 0
		pollCentralStatus(ctx, requests, updates, ticks, func(c context.Context) (agentSnapshot, error) {
			deadline, ok := c.Deadline()
			if !ok || time.Until(deadline) > 10*time.Second {
				t.Error("missing collection timeout")
			}
			count++
			calls <- count
			if count == 1 {
				return agentSnapshot{}, errors.New("temporary failure")
			}
			return agentSnapshot{Agents: []agentSnapshotEntry{{ID: "current", State: "idle"}}}, nil
		})
	}()
	ticks <- time.Now() // No running projects: collection must not contact Docker.
	select {
	case <-calls:
		t.Fatal("polled with no active runs")
	default:
	}
	requests <- []string{"current"}
	select {
	case <-calls:
	case <-time.After(time.Second):
		t.Fatal("no immediate collection")
	}
	// Leave the result unread. A subsequent poll must not block behind it.
	ticks <- time.Now()
	select {
	case <-calls:
	case <-time.After(time.Second):
		t.Fatal("blocked behind unread update")
	}
	ticks <- time.Now() // Unbuffered tick ensures the preceding update was published.
	select {
	case <-calls:
	case <-time.After(time.Second):
		t.Fatal("poller stopped after error")
	}
	update := <-updates
	if update.err != nil || !reflect.DeepEqual(update.runs, []string{"current"}) {
		t.Fatalf("update=%#v", update)
	}
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("poller did not stop")
	}
}

func TestPollCentralStatusCancellationDuringCollection(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	requests := make(chan []string, 1)
	updates := make(chan centralStatusUpdate, 1)
	started, done := make(chan struct{}), make(chan struct{})
	go func() {
		defer close(done)
		pollCentralStatus(ctx, requests, updates, nil, func(c context.Context) (agentSnapshot, error) {
			close(started)
			<-c.Done()
			return agentSnapshot{}, c.Err()
		})
	}()
	requests <- []string{"current"}
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("collection not started")
	}
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("collection not canceled")
	}
	select {
	case <-updates:
		t.Fatal("published shutdown cancellation as a status failure")
	default:
	}
}
