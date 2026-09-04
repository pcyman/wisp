package docker

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
)

// Container is the subset of Docker inspect data needed by lifecycle code.
type Container struct {
	ID      string
	Name    string
	Running bool
	Labels  map[string]string
}

// ImageExists distinguishes an absent image from an inspect failure.
func (c *Client) ImageExists(ctx context.Context, image string) (bool, error) {
	if strings.TrimSpace(image) == "" {
		return false, fmt.Errorf("image name is empty")
	}
	_, stderr, err := c.capture(ctx, []string{"image", "inspect", image}, "", nil)
	if err == nil {
		return true, nil
	}
	if missingImage(stderr) {
		return false, nil
	}
	return false, commandError("inspect image "+image, stderr, err)
}

// InspectContainer returns (nil, nil) only when Docker reports that the named
// container does not exist.
func (c *Client) InspectContainer(ctx context.Context, name string) (*Container, error) {
	if strings.TrimSpace(name) == "" {
		return nil, fmt.Errorf("container name is empty")
	}
	stdout, stderr, err := c.capture(ctx, []string{"container", "inspect", name}, "", nil)
	if err != nil {
		if missingContainer(stderr) {
			return nil, nil
		}
		return nil, commandError("inspect container "+name, stderr, err)
	}
	var values []struct {
		ID     string `json:"Id"`
		Name   string `json:"Name"`
		Config struct {
			Labels map[string]string `json:"Labels"`
		} `json:"Config"`
		State struct {
			Running bool `json:"Running"`
		} `json:"State"`
	}
	if err := json.Unmarshal(stdout, &values); err != nil {
		return nil, fmt.Errorf("decode container inspect for %q: %w", name, err)
	}
	if len(values) != 1 {
		return nil, fmt.Errorf("container inspect for %q returned %d objects", name, len(values))
	}
	return &Container{ID: values[0].ID, Name: strings.TrimPrefix(values[0].Name, "/"), Running: values[0].State.Running, Labels: values[0].Config.Labels}, nil
}

// ProjectContainers discovers all containers owned by a Compose project and
// inspects each one so callers can validate labels before cleanup.
func (c *Client) ProjectContainers(ctx context.Context, projectName string) ([]Container, error) {
	if strings.TrimSpace(projectName) == "" {
		return nil, fmt.Errorf("Compose project name is empty")
	}
	stdout, stderr, err := c.capture(ctx, []string{"container", "ls", "--all", "--filter", "label=com.docker.compose.project=" + projectName, "--format", "{{.Names}}"}, "", nil)
	if err != nil {
		return nil, commandError("discover Compose project "+projectName, stderr, err)
	}
	names := strings.Fields(string(stdout))
	containers := make([]Container, 0, len(names))
	for _, name := range names {
		container, err := c.InspectContainer(ctx, name)
		if err != nil {
			return nil, err
		}
		if container != nil {
			containers = append(containers, *container)
		}
	}
	return containers, nil
}

// ManagedProjectContainers discovers containers that claim Wisp ownership for
// a host UID and project hash, including resources detached from Compose.
func (c *Client) ManagedProjectContainers(ctx context.Context, projectHash string, uid int) ([]Container, error) {
	if strings.TrimSpace(projectHash) == "" || uid < 0 {
		return nil, fmt.Errorf("project hash and non-negative owner UID are required")
	}
	stdout, stderr, err := c.capture(ctx, []string{
		"container", "ls", "--all",
		"--filter", "label=wisp.managed=true",
		"--filter", "label=wisp.owner-uid=" + strconv.Itoa(uid),
		"--filter", "label=wisp.project-hash=" + projectHash,
		"--format", "{{.Names}}",
	}, "", nil)
	if err != nil {
		return nil, commandError("discover managed project containers", stderr, err)
	}
	names := strings.Fields(string(stdout))
	containers := make([]Container, 0, len(names))
	for _, name := range names {
		container, err := c.InspectContainer(ctx, name)
		if err != nil {
			return nil, err
		}
		if container != nil {
			containers = append(containers, *container)
		}
	}
	return containers, nil
}

// RemoveContainer force-removes a container after the caller has verified its labels.
func (c *Client) RemoveContainer(ctx context.Context, name string) error {
	_, stderr, err := c.capture(ctx, []string{"container", "rm", "--force", name}, "", nil)
	if err != nil && !missingContainer(stderr) {
		return commandError("remove container "+name, stderr, err)
	}
	return nil
}

// VerifyLabels requires every expected Wisp label to be present and exactly
// equal. Unknown additional labels are harmless.
func VerifyLabels(actual, expected map[string]string) error {
	for key, want := range expected {
		got, ok := actual[key]
		if !ok {
			return fmt.Errorf("container is missing required label %q", key)
		}
		if got != want {
			return fmt.Errorf("container label %q is %q, expected %q", key, got, want)
		}
	}
	return nil
}

func missingImage(stderr []byte) bool {
	text := strings.ToLower(string(stderr))
	return strings.Contains(text, "no such image") || strings.Contains(text, "no such object")
}

func missingContainer(stderr []byte) bool {
	text := strings.ToLower(string(stderr))
	return strings.Contains(text, "no such container") || strings.Contains(text, "no such object")
}
