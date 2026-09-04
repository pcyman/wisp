package docker

import (
	"context"
	"errors"
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"

	"wisp/internal/lock"
)

type Image struct {
	Service string
	Name    string
}

type BuildRequest struct {
	Invocation ComposeInvocation
	LockRoot   string
	UID        int
	CPUs       int
	Images     []Image
	Rebuild    bool
}

// EnsureImages inspects every requested image independently and builds only
// missing services unless Rebuild is set. It returns the services built.
func (c *Client) EnsureImages(ctx context.Context, request BuildRequest) (built []string, err error) {
	if request.UID < 0 || request.CPUs <= 0 || request.CPUs > math.MaxInt/100000 {
		return nil, fmt.Errorf("invalid build UID or CPU count")
	}
	seen := make(map[string]struct{}, len(request.Images))
	for _, image := range request.Images {
		if image.Service == "" || image.Name == "" {
			return nil, fmt.Errorf("build image service and name are required")
		}
		if _, duplicate := seen[image.Service]; duplicate {
			return nil, fmt.Errorf("duplicate build service %q", image.Service)
		}
		seen[image.Service] = struct{}{}
		exists := false
		if !request.Rebuild {
			var inspectErr error
			exists, inspectErr = c.ImageExists(ctx, image.Name)
			if inspectErr != nil {
				return nil, inspectErr
			}
		}
		if request.Rebuild || !exists {
			built = append(built, image.Service)
		}
	}
	if len(built) == 0 {
		return nil, nil
	}
	buildLock, err := lock.Acquire(ctx, request.LockRoot, "build-"+strconv.Itoa(request.UID))
	if err != nil {
		return nil, fmt.Errorf("acquire build lock: %w", err)
	}
	defer func() {
		if closeErr := buildLock.Close(); closeErr != nil {
			err = errors.Join(err, fmt.Errorf("release build lock: %w", closeErr))
		}
	}()
	if !request.Rebuild {
		candidates := make(map[string]struct{}, len(built))
		for _, service := range built {
			candidates[service] = struct{}{}
		}
		built = built[:0]
		for _, image := range request.Images {
			if _, candidate := candidates[image.Service]; !candidate {
				continue
			}
			exists, inspectErr := c.ImageExists(ctx, image.Name)
			if inspectErr != nil {
				return nil, inspectErr
			}
			if !exists {
				built = append(built, image.Service)
			}
		}
		if len(built) == 0 {
			return nil, nil
		}
	}
	if _, err := c.CheckBuildx(ctx); err != nil {
		return nil, err
	}

	builder := fmt.Sprintf("wisp-%d-%dcpu", request.UID, request.CPUs)
	stopBuilder, builderErr := c.ensureBuilder(ctx, builder, request.CPUs)
	if stopBuilder {
		defer func() {
			cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 15*time.Second)
			defer cancel()
			_, stderr, stopErr := c.capture(cleanupCtx, []string{"buildx", "stop", builder}, "", nil)
			if stopErr != nil {
				err = errors.Join(err, commandError("stop Buildx builder "+builder, stderr, stopErr))
			}
		}()
	}
	if builderErr != nil {
		return nil, builderErr
	}

	_, stderr, err := c.capture(ctx, []string{"buildx", "inspect", builder, "--bootstrap"}, "", nil)
	if err != nil {
		return nil, commandError("bootstrap Buildx builder "+builder, stderr, err)
	}
	if err := c.ComposeAttached(ctx, request.Invocation, append([]string{"build", "--builder", builder}, built...)...); err != nil {
		return nil, fmt.Errorf("build Docker images: %w", err)
	}
	return built, nil
}

func (c *Client) ensureBuilder(ctx context.Context, name string, cpus int) (bool, error) {
	stdout, stderr, err := c.capture(ctx, []string{"buildx", "inspect", name, "--format", "{{.Driver}}"}, "", nil)
	if err == nil {
		if driver := strings.TrimSpace(string(stdout)); driver != "docker-container" {
			return false, fmt.Errorf("Buildx builder %q uses driver %q, expected docker-container", name, driver)
		}
		return true, nil
	}
	if !missingBuilder(stderr) {
		return false, commandError("inspect Buildx builder "+name, stderr, err)
	}
	quota := strconv.Itoa(cpus * 100000)
	_, stderr, err = c.capture(ctx, []string{
		"buildx", "create", "--name", name, "--driver", "docker-container",
		"--driver-opt", "cpu-period=100000", "--driver-opt", "cpu-quota=" + quota,
	}, "", nil)
	if err != nil {
		// Creation may have reached the daemon before the CLI failed. A stop is
		// safe and keeps partially started builder state from surviving.
		return true, commandError("create Buildx builder "+name, stderr, err)
	}
	return true, nil
}

func missingBuilder(stderr []byte) bool {
	text := strings.ToLower(string(stderr))
	return strings.Contains(text, "no builder") || strings.Contains(text, "not found") || strings.Contains(text, "does not exist")
}
