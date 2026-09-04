package docker

import (
	"context"
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

var versionPattern = regexp.MustCompile(`(?i)(?:^|[^0-9])v?([0-9]+)\.([0-9]+)(?:\.[0-9]+)?`)

const versionFormat = "{{.Server.Version}}"

// Prerequisites contains parsed versions useful to doctor output.
type Prerequisites struct {
	Docker  string
	Compose string
}

// CheckCLI verifies that the Docker executable can be invoked without
// contacting or changing the daemon.
func (c *Client) CheckCLI(ctx context.Context) (string, error) {
	stdout, stderr, err := c.capture(ctx, []string{"--version"}, "", nil)
	if err != nil {
		return "", commandError("docker --version", stderr, err)
	}
	value := strings.TrimSpace(string(stdout))
	if value == "" {
		return "", fmt.Errorf("docker --version returned no output")
	}
	return value, nil
}

// CheckDocker verifies Docker CLI/daemon availability without requiring Compose.
func (c *Client) CheckDocker(ctx context.Context) (string, error) {
	stdout, stderr, err := c.capture(ctx, []string{"version", "--format", versionFormat}, "", nil)
	if err != nil {
		return "", commandError("docker version", stderr, err)
	}
	value := strings.TrimSpace(string(stdout))
	if err := requireVersion(value, 20, 10, "Docker"); err != nil {
		return "", err
	}
	return value, nil
}

// CheckCompose verifies the Compose plugin independently for diagnostics.
func (c *Client) CheckCompose(ctx context.Context) (string, error) {
	stdout, stderr, err := c.capture(ctx, []string{"compose", "version", "--short"}, "", nil)
	if err != nil {
		return "", commandError("docker compose version", stderr, err)
	}
	value := strings.TrimSpace(string(stdout))
	if err := requireVersion(value, 2, 24, "Docker Compose"); err != nil {
		return "", err
	}
	return value, nil
}

// CheckPrerequisites verifies daemon reachability and the minimum Docker and
// Compose versions needed by Wisp.
func (c *Client) CheckPrerequisites(ctx context.Context) (Prerequisites, error) {
	dockerText, err := c.CheckDocker(ctx)
	if err != nil {
		return Prerequisites{}, err
	}

	composeText, err := c.CheckCompose(ctx)
	if err != nil {
		return Prerequisites{}, err
	}
	return Prerequisites{Docker: dockerText, Compose: composeText}, nil
}

// CheckBuildx verifies the minimum Buildx version needed for builds.
func (c *Client) CheckBuildx(ctx context.Context) (string, error) {
	stdout, stderr, err := c.capture(ctx, []string{"buildx", "version"}, "", nil)
	if err != nil {
		return "", commandError("docker buildx version", stderr, err)
	}
	text := strings.TrimSpace(string(stdout))
	if err := requireVersion(text, 0, 11, "Docker Buildx"); err != nil {
		return "", err
	}
	return text, nil
}

func requireVersion(value string, major, minor int, component string) error {
	match := versionPattern.FindStringSubmatch(value)
	if match == nil {
		return fmt.Errorf("cannot parse %s version from %q", component, value)
	}
	gotMajor, _ := strconv.Atoi(match[1])
	gotMinor, _ := strconv.Atoi(match[2])
	if gotMajor < major || gotMajor == major && gotMinor < minor {
		return fmt.Errorf("%s %d.%d or newer is required; found %s", component, major, minor, value)
	}
	return nil
}

func commandError(operation string, stderr []byte, err error) error {
	detail := strings.TrimSpace(string(stderr))
	if detail == "" {
		return fmt.Errorf("%s: %w", operation, err)
	}
	return fmt.Errorf("%s: %s: %w", operation, detail, err)
}
