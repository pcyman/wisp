package docker

import (
	"context"
	"fmt"
	"io"
	"path/filepath"
	"sort"
	"strings"

	"wisp/internal/process"
)

var privateEnvironment = map[string]struct{}{
	"WISP_RUN_ID": {},
	"WISP_IMAGE":  {}, "WISP_CREDENTIALS_IMAGE": {}, "WISP_UID": {},
	"WISP_GID": {}, "WISP_AWS_ALIAS": {}, "WISP_AWS_AUTHORIZATION_TOKEN": {},
	"WISP_PROJECT_HASH": {}, "WISP_CLI_VERSION": {}, "OPENCODE_VERSION": {},
	"HUNK_VERSION": {}, "AWS_CLI_VERSION": {}, "KUBECTL_VERSION": {},
	"HELM_VERSION": {}, "TERRAFORM_VERSION": {}, "GO_VERSION": {},
	"BOTO3_VERSION": {},
}

// ComposeAttachedIO runs Compose attached to the supplied streams.
func (c *Client) ComposeAttachedIO(ctx context.Context, invocation ComposeInvocation, stdin io.Reader, stdout, stderr io.Writer, args ...string) error {
	argv, err := invocation.Args(args...)
	if err != nil {
		return err
	}
	if c == nil || c.runner == nil {
		return fmt.Errorf("Docker process runner is not configured")
	}
	return c.runner.Attached(ctx, process.Command{Path: c.path, Args: argv, Dir: invocation.RuntimeRoot, Env: invocation.Environment, Stdin: stdin, Stdout: stdout, Stderr: stderr})
}

// ComposeInvocation fixes the base Compose argv and sanitized environment for
// one invocation.
type ComposeInvocation struct {
	ProjectName string
	RuntimeRoot string
	Override    string
	Environment []string
}

func (i ComposeInvocation) Args(extra ...string) ([]string, error) {
	if i.ProjectName == "" || i.RuntimeRoot == "" || i.Override == "" {
		return nil, fmt.Errorf("Compose project name, runtime root, and override are required")
	}
	args := []string{
		"compose",
		"--project-name", i.ProjectName,
		"--project-directory", i.RuntimeRoot,
		"--file", filepath.Join(i.RuntimeRoot, "compose.yaml"),
		"--file", i.Override,
	}
	return append(args, extra...), nil
}

func (c *Client) ComposeCapture(ctx context.Context, invocation ComposeInvocation, args ...string) ([]byte, []byte, error) {
	argv, err := invocation.Args(args...)
	if err != nil {
		return nil, nil, err
	}
	return c.capture(ctx, argv, invocation.RuntimeRoot, invocation.Environment)
}

func (c *Client) ComposeAttached(ctx context.Context, invocation ComposeInvocation, args ...string) error {
	argv, err := invocation.Args(args...)
	if err != nil {
		return err
	}
	return c.attached(ctx, argv, invocation.RuntimeRoot, invocation.Environment)
}

// SanitizeEnvironment removes inherited Compose protocol values and appends
// planned values in stable key order. Empty planned values are retained.
func SanitizeEnvironment(inherited []string, planned map[string]string) []string {
	result := make([]string, 0, len(inherited)+len(planned))
	for _, entry := range inherited {
		key := entry
		if index := strings.IndexByte(entry, '='); index >= 0 {
			key = entry[:index]
		}
		_, private := privateEnvironment[key]
		_, replaced := planned[key]
		if private || replaced {
			continue
		}
		result = append(result, entry)
	}
	keys := make([]string, 0, len(planned))
	for key := range planned {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		result = append(result, key+"="+planned[key])
	}
	return result
}
