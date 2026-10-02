package app

import (
	"context"
	"time"

	"wisp/internal/agentstatus"
)

type centralStatusUpdate struct {
	runs     []string
	snapshot agentSnapshot
	err      error
}

// Separate latest-value mailboxes keep slow Docker calls and suspended terminal
// clients from blocking lifecycle events, input, or shutdown.
func centralLatest[T any](mailbox chan T, value T) {
	select {
	case mailbox <- value:
		return
	default:
	}
	select {
	case <-mailbox:
	default:
	}
	select {
	case mailbox <- value:
	default:
	}
}

func pollCentralStatus(ctx context.Context, requests <-chan []string, updates chan centralStatusUpdate, ticks <-chan time.Time, collect func(context.Context) (agentSnapshot, error)) {
	var runs []string
	for {
		select {
		case <-ctx.Done():
			return
		case next, ok := <-requests:
			if !ok {
				return
			}
			runs = next
		case _, ok := <-ticks:
			if !ok {
				return
			}
		}
		if len(runs) == 0 {
			continue
		}
		collectionCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
		snapshot, err := collect(collectionCtx)
		if err == nil {
			err = collectionCtx.Err()
		}
		cancel()
		if ctx.Err() != nil {
			return
		}
		centralLatest(updates, centralStatusUpdate{runs: runs, snapshot: snapshot, err: err})
	}
}

func centralStatusRuns(projects []centralProject) []string {
	var runs []string
	for _, p := range projects {
		if p.state == "running" {
			if id := p.sandbox.Labels[agentstatus.RunLabel]; id != "" {
				runs = append(runs, id)
			}
		}
	}
	return runs
}

func applyCentralStatus(projects []centralProject, update centralStatusUpdate) bool {
	targets := make(map[string]bool, len(update.runs))
	for _, id := range update.runs {
		targets[id] = true
	}
	entries := make(map[string]agentSnapshotEntry, len(update.snapshot.Agents))
	for _, entry := range update.snapshot.Agents {
		entries[entry.ID] = entry
	}
	applied := false
	for i := range projects {
		p := &projects[i]
		id := p.sandbox.Labels[agentstatus.RunLabel]
		if p.state != "running" || !targets[id] {
			continue
		}
		applied = true
		p.activityStale = update.err != nil
		if update.err != nil {
			continue
		}
		p.activity = agentstatus.Report{State: "unknown"}
		entry, ok := entries[id]
		if ok && entry.SandboxID == p.plan.Project.ContainerName && entry.Repo == p.plan.Project.RootDir && entry.Agent == p.plan.AgentKey {
			p.activity = agentstatus.Report{State: entry.State, Reason: entry.Reason, Reporter: entry.Reporter}
		}
	}
	return applied
}

func centralActivity(p centralProject) string {
	if p.state != "running" {
		return "-"
	}
	state := p.activity.State
	if state == "" {
		state = "unknown"
	}
	if p.activityStale {
		if state == "unknown" {
			return "unknown (unavailable)"
		}
		return state + " (stale)"
	}
	if p.activity.Reason != "" {
		return state + " (" + p.activity.Reason + ")"
	}
	if state == "unknown" && p.activity.Reporter != "" {
		return state + " (report " + p.activity.Reporter + ")"
	}
	return state
}
