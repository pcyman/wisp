package app

import (
	"context"
	"encoding/json"

	"wisp/internal/agentstatus"
	"wisp/internal/docker"
	"wisp/internal/lock"
)

func (a *App) agents(ctx context.Context) (int, error) {
	root, err := lock.RuntimeRoot(a.env.XDGRuntimeDir, a.env.TempDir, a.deps.UID)
	if err != nil {
		return 1, err
	}
	entries, err := agentstatus.List(root, a.deps.UID)
	if err != nil {
		return 1, err
	}
	type agent struct {
		ID        string `json:"id"`
		SandboxID string `json:"sandbox_id"`
		Repo      string `json:"repo"`
		Agent     string `json:"agent"`
		State     string `json:"state"`
		Reason    string `json:"reason,omitempty"`
		Reporter  string `json:"reporter"`
		UpdatedAt string `json:"updated_at,omitempty"`
	}
	output := struct {
		SchemaVersion int     `json:"schema_version"`
		Agents        []agent `json:"agents"`
	}{SchemaVersion: 1, Agents: []agent{}}
	for _, entry := range entries {
		container, err := a.docker.InspectContainer(ctx, entry.SandboxName)
		if err != nil {
			return 1, err
		}
		if container == nil || !container.Running {
			continue
		}
		labels := composeProjectLabels(entry.ProjectHash, entry.UID, "sandbox", entry.ComposeProject)
		labels[agentstatus.RunLabel] = entry.RunID
		if docker.VerifyLabels(container.Labels, labels) != nil {
			continue
		}
		output.Agents = append(output.Agents, agent{
			ID: entry.RunID, SandboxID: entry.SandboxName, Repo: entry.Repo, Agent: "opencode",
			State: entry.Report.State, Reason: entry.Report.Reason,
			Reporter: entry.Report.Reporter, UpdatedAt: entry.Report.UpdatedAt,
		})
	}
	if err := json.NewEncoder(a.deps.Stdout).Encode(output); err != nil {
		return 1, err
	}
	return 0, nil
}
