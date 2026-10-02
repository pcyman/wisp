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

type agentHostProcess struct {
	PID int `json:"pid"`
}

type agentSnapshotEntry struct {
	ID          string            `json:"id"`
	SandboxID   string            `json:"sandbox_id"`
	Repo        string            `json:"repo"`
	Agent       string            `json:"agent"`
	State       string            `json:"state"`
	Reason      string            `json:"reason,omitempty"`
	Reporter    string            `json:"reporter"`
	UpdatedAt   string            `json:"updated_at,omitempty"`
	HostProcess *agentHostProcess `json:"host_process,omitempty"`
}

type agentSnapshot struct {
	SchemaVersion int                  `json:"schema_version"`
	Agents        []agentSnapshotEntry `json:"agents"`
}

func (a *App) collectAgents(ctx context.Context) ([]byte, error) {
	snapshot, err := a.collectAgentSnapshot(ctx)
	if err != nil {
		return nil, err
	}
	data, err := json.Marshal(snapshot)
	if err != nil {
		return nil, err
	}
	return append(data, '\n'), nil
}

// collectAgentSnapshot is shared by JSON consumers and Central. Reports pass
// through the registry's bounded no-follow reader and verified container labels.
func (a *App) collectAgentSnapshot(ctx context.Context) (agentSnapshot, error) {
	root, err := lock.RuntimeRoot(a.env.XDGRuntimeDir, a.env.TempDir, a.deps.UID)
	if err != nil {
		return agentSnapshot{}, err
	}
	entries, err := agentstatus.List(root, a.deps.UID)
	if err != nil {
		return agentSnapshot{}, err
	}
	output := agentSnapshot{SchemaVersion: 1, Agents: []agentSnapshotEntry{}}
	for _, entry := range entries {
		container, err := a.docker.InspectContainer(ctx, entry.SandboxName)
		if err != nil {
			return agentSnapshot{}, err
		}
		if container == nil || !container.Running {
			continue
		}
		labels := composeProjectLabels(entry.ProjectHash, entry.UID, "sandbox", entry.ComposeProject)
		labels[agentstatus.RunLabel] = entry.RunID
		if docker.VerifyLabels(container.Labels, labels) != nil {
			continue
		}
		agentName := entry.Agent
		if agentName == "" {
			agentName = "opencode"
		}
		item := agentSnapshotEntry{
			ID: entry.RunID, SandboxID: entry.SandboxName, Repo: entry.Repo, Agent: agentName,
			State: entry.Report.State, Reason: entry.Report.Reason,
			Reporter: entry.Report.Reporter, UpdatedAt: entry.Report.UpdatedAt,
		}
		if entry.HostPID > 0 {
			item.HostProcess = &agentHostProcess{PID: entry.HostPID}
		}
		output.Agents = append(output.Agents, item)
	}
	return output, nil
}
