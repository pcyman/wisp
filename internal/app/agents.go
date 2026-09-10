package app

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"time"

	"wisp/internal/agentstatus"
	"wisp/internal/cli"
	"wisp/internal/docker"
	"wisp/internal/lock"
)

func (a *App) agents(ctx context.Context, request cli.AgentsRequest) (int, error) {
	var ticks <-chan time.Time
	if request.Watch {
		ticker := time.NewTicker(time.Second)
		defer ticker.Stop()
		ticks = ticker.C
	}
	if err := writeAgentSnapshots(ctx, a.deps.Stdout, ticks, a.collectAgents); err != nil {
		return 1, err
	}
	return 0, nil
}

// A nil ticks channel selects one-shot output; watch emits only changed snapshots.
func writeAgentSnapshots(ctx context.Context, out io.Writer, ticks <-chan time.Time, collect func(context.Context) ([]byte, error)) error {
	var previous []byte
	for {
		if ctx.Err() != nil {
			return nil
		}
		collectionCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
		snapshot, err := collect(collectionCtx)
		if err == nil {
			err = collectionCtx.Err()
		}
		cancel()
		if ctx.Err() != nil {
			return nil
		}
		if err != nil {
			return err
		}
		if !bytes.Equal(snapshot, previous) {
			n, err := out.Write(snapshot)
			if err != nil {
				return err
			}
			if n != len(snapshot) {
				return io.ErrShortWrite
			}
			previous = snapshot
		}
		if ticks == nil {
			return nil
		}
		select {
		case <-ctx.Done():
			return nil
		case _, ok := <-ticks:
			if !ok {
				return nil
			}
		}
	}
}

func (a *App) collectAgents(ctx context.Context) ([]byte, error) {
	root, err := lock.RuntimeRoot(a.env.XDGRuntimeDir, a.env.TempDir, a.deps.UID)
	if err != nil {
		return nil, err
	}
	entries, err := agentstatus.List(root, a.deps.UID)
	if err != nil {
		return nil, err
	}
	type hostProcess struct {
		PID int `json:"pid"`
	}
	type agent struct {
		ID          string       `json:"id"`
		SandboxID   string       `json:"sandbox_id"`
		Repo        string       `json:"repo"`
		Agent       string       `json:"agent"`
		State       string       `json:"state"`
		Reason      string       `json:"reason,omitempty"`
		Reporter    string       `json:"reporter"`
		UpdatedAt   string       `json:"updated_at,omitempty"`
		HostProcess *hostProcess `json:"host_process,omitempty"`
	}
	output := struct {
		SchemaVersion int     `json:"schema_version"`
		Agents        []agent `json:"agents"`
	}{SchemaVersion: 1, Agents: []agent{}}
	for _, entry := range entries {
		container, err := a.docker.InspectContainer(ctx, entry.SandboxName)
		if err != nil {
			return nil, err
		}
		if container == nil || !container.Running {
			continue
		}
		labels := composeProjectLabels(entry.ProjectHash, entry.UID, "sandbox", entry.ComposeProject)
		labels[agentstatus.RunLabel] = entry.RunID
		if docker.VerifyLabels(container.Labels, labels) != nil {
			continue
		}
		item := agent{
			ID: entry.RunID, SandboxID: entry.SandboxName, Repo: entry.Repo, Agent: "opencode",
			State: entry.Report.State, Reason: entry.Report.Reason,
			Reporter: entry.Report.Reporter, UpdatedAt: entry.Report.UpdatedAt,
		}
		if entry.HostPID > 0 {
			item.HostProcess = &hostProcess{PID: entry.HostPID}
		}
		output.Agents = append(output.Agents, item)
	}
	data, err := json.Marshal(output)
	if err != nil {
		return nil, err
	}
	return append(data, '\n'), nil
}
