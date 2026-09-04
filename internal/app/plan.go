// Package app coordinates validated inputs into plans consumed by lifecycle
// code. Planning performs no Docker operations.
package app

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"syscall"

	"wisp/internal/agent"
	"wisp/internal/cli"
	"wisp/internal/config"
	"wisp/internal/docker"
	"wisp/internal/mount"
	"wisp/internal/process"
	"wisp/internal/project"
)

const projectTarget = "/workspace/current"

// HostEnvironment is the explicit host state that can affect a run plan.
type HostEnvironment struct {
	Home          string
	XDGConfigHome string
	XDGDataHome   string
	XDGCacheHome  string
	XDGRuntimeDir string
	TempDir       string
	Term          string
	ColorTerm     string
}

// PlanOptions supplies host facts and injectable nondeterministic inputs.
type PlanOptions struct {
	InvocationDir string
	UID           int
	GID           int
	CLIVersion    string
	Environment   HostEnvironment
	Runner        process.Runner
	ProcessEnv    []string
}

// RuntimeDirectories are roots selected during planning but created later by
// lifecycle code.
type RuntimeDirectories struct {
	Assets  string
	Private string
}

// SandboxPlan is the complete validated input to the Docker run lifecycle.
// Callers should treat it as immutable after construction.
type SandboxPlan struct {
	Project            project.Project
	ConfigPath         string
	ConfigSnapshot     []byte
	SelectedAWSAlias   string
	AWSProfile         string
	AWSConfigPath      string
	AWSSSOCachePath    string
	UID                int
	GID                int
	Images             config.ImageConfig
	Versions           config.VersionConfig
	BuildCPUs          int
	Rebuild            bool
	AgentName          string
	Command            []string
	Mounts             []docker.Mount
	Environment        map[string]string
	RuntimeDirectories RuntimeDirectories
	Warnings           []config.Warning
}

// PlanRun resolves and validates every run input before lifecycle code can
// perform a Docker side effect.
func PlanRun(ctx context.Context, request cli.RunRequest, options PlanOptions) (SandboxPlan, error) {
	if options.UID < 0 {
		return SandboxPlan{}, fmt.Errorf("UID must not be negative")
	}
	if options.GID < 0 {
		return SandboxPlan{}, fmt.Errorf("GID must not be negative")
	}
	if !filepath.IsAbs(options.InvocationDir) {
		return SandboxPlan{}, fmt.Errorf("invocation directory %q is not absolute", options.InvocationDir)
	}

	cfgEnv := config.Environment{
		Home:          options.Environment.Home,
		XDGConfigHome: options.Environment.XDGConfigHome,
		XDGDataHome:   options.Environment.XDGDataHome,
	}
	configPath, err := config.ResolvePath(request.ConfigPath, options.InvocationDir, cfgEnv)
	if err != nil {
		return SandboxPlan{}, err
	}
	loaded, err := config.Load(configPath, cfgEnv)
	if err != nil {
		return SandboxPlan{}, err
	}

	var gitRoot project.GitRootFunc
	if options.Runner != nil {
		gitRoot = func(ctx context.Context, dir string) (string, error) {
			stdout, _, err := options.Runner.Capture(ctx, process.Command{
				Path: "git",
				Args: []string{"-C", dir, "rev-parse", "--show-toplevel"},
				Env:  docker.SanitizeEnvironment(options.ProcessEnv, nil),
			})
			return string(stdout), err
		}
	}
	resolvedProject, err := project.Resolve(ctx, request.Directory, options.InvocationDir, options.UID, gitRoot)
	if err != nil {
		return SandboxPlan{}, err
	}
	selectedAlias, err := config.SelectAWSAlias(loaded.Config, request.AWSAlias)
	if err != nil {
		return SandboxPlan{}, err
	}

	specs := make([]mount.Spec, len(loaded.Config.Mounts))
	for i, configured := range loaded.Config.Mounts {
		specs[i] = mount.Spec{
			Source: configured.Source,
			Target: configured.Target,
			Mode:   mount.Mode(configured.Mode),
		}
	}
	extraMounts, err := mount.Plan(
		specs,
		request.ReadOnlyMounts,
		request.ReadWriteMounts,
		filepath.Dir(loaded.Path),
		options.InvocationDir,
		options.Environment.Home,
	)
	if err != nil {
		return SandboxPlan{}, err
	}

	directories, err := planRuntimeDirectories(options)
	if err != nil {
		return SandboxPlan{}, err
	}
	selectedAgent := agent.Default()
	projectMount, err := docker.Bind(resolvedProject.RootDir, projectTarget, false)
	if err != nil {
		return SandboxPlan{}, err
	}
	plannedMounts := []docker.Mount{projectMount}
	if options.Environment.Home != "" {
		gitConfig := filepath.Join(options.Environment.Home, ".gitconfig")
		if physical, statErr := filepath.EvalSymlinks(gitConfig); statErr == nil {
			if info, infoErr := os.Stat(physical); infoErr == nil && info.Mode().IsRegular() {
				planned, bindErr := docker.Bind(physical, "/home/sandbox/.gitconfig", true)
				if bindErr != nil {
					return SandboxPlan{}, bindErr
				}
				plannedMounts = append(plannedMounts, planned)
			}
		}
	}
	agentMounts, err := selectedAgent.HostMounts(loaded.Config)
	if err != nil {
		return SandboxPlan{}, err
	}
	plannedMounts = append(plannedMounts, agentMounts...)
	for _, extra := range extraMounts {
		planned, err := docker.Bind(extra.Source, extra.Target, extra.Mode == mount.ReadOnly)
		if err != nil {
			return SandboxPlan{}, err
		}
		plannedMounts = append(plannedMounts, planned)
	}

	environment := plannedEnvironment(options, loaded.Config, selectedAlias, resolvedProject.Hash)

	return SandboxPlan{
		Project:            resolvedProject,
		ConfigPath:         loaded.Path,
		ConfigSnapshot:     append([]byte(nil), loaded.Snapshot...),
		SelectedAWSAlias:   selectedAlias,
		AWSProfile:         loaded.Config.AWS.Aliases[selectedAlias].Profile,
		AWSConfigPath:      loaded.Config.AWS.HostConfigPath,
		AWSSSOCachePath:    loaded.Config.AWS.SSOCachePath,
		UID:                options.UID,
		GID:                options.GID,
		Images:             loaded.Config.Images,
		Versions:           loaded.Config.Build.Versions,
		BuildCPUs:          loaded.Config.Build.CPUs,
		Rebuild:            request.Rebuild,
		AgentName:          selectedAgent.Name(),
		Command:            append([]string(nil), selectedAgent.ContainerCommand()...),
		Mounts:             plannedMounts,
		Environment:        environment,
		RuntimeDirectories: directories,
		Warnings:           append([]config.Warning(nil), loaded.Warnings...),
	}, nil
}

func plannedEnvironment(options PlanOptions, cfg config.Config, alias, projectHash string) map[string]string {
	versions := cfg.Build.Versions
	environment := map[string]string{
		"WISP_IMAGE":             cfg.Images.Sandbox,
		"WISP_CREDENTIALS_IMAGE": cfg.Images.Credentials,
		"WISP_UID":               strconv.Itoa(options.UID),
		"WISP_GID":               strconv.Itoa(options.GID),
		"WISP_AWS_ALIAS":         alias,
		"WISP_PROJECT_HASH":      projectHash,
		"WISP_CLI_VERSION":       options.CLIVersion,
		"OPENCODE_VERSION":       versions.OpenCode,
		"HUNK_VERSION":           versions.Hunk,
		"AWS_CLI_VERSION":        versions.AWSCLI,
		"KUBECTL_VERSION":        versions.Kubectl,
		"HELM_VERSION":           versions.Helm,
		"TERRAFORM_VERSION":      versions.Terraform,
		"GO_VERSION":             versions.Go,
		"BOTO3_VERSION":          versions.Boto3,
	}
	if options.Environment.Term != "" {
		environment["TERM"] = options.Environment.Term
	}
	if options.Environment.ColorTerm != "" {
		environment["COLORTERM"] = options.Environment.ColorTerm
	}
	return environment
}

func planRuntimeDirectories(options PlanOptions) (RuntimeDirectories, error) {
	cacheBase := options.Environment.XDGCacheHome
	if cacheBase == "" {
		if options.Environment.Home == "" {
			return RuntimeDirectories{}, fmt.Errorf("determine runtime asset cache: neither XDG_CACHE_HOME nor HOME is set")
		}
		cacheBase = filepath.Join(options.Environment.Home, ".cache")
	}
	if !filepath.IsAbs(cacheBase) {
		return RuntimeDirectories{}, fmt.Errorf("runtime asset cache base %q is not absolute", cacheBase)
	}

	privateRoot := ""
	if usableRuntimeDirectory(options.Environment.XDGRuntimeDir, options.UID) {
		privateRoot = filepath.Join(filepath.Clean(options.Environment.XDGRuntimeDir), "wisp")
	} else {
		privateBase := options.Environment.TempDir
		if privateBase == "" {
			privateBase = "/tmp"
		}
		if !filepath.IsAbs(privateBase) {
			return RuntimeDirectories{}, fmt.Errorf("temporary directory %q is not absolute", privateBase)
		}
		privateRoot = filepath.Join(filepath.Clean(privateBase), "wisp-"+strconv.Itoa(options.UID))
	}

	return RuntimeDirectories{
		Assets:  filepath.Join(filepath.Clean(cacheBase), "wisp", "runtime"),
		Private: privateRoot,
	}, nil
}

func usableRuntimeDirectory(name string, uid int) bool {
	if name == "" || !filepath.IsAbs(name) {
		return false
	}
	info, err := os.Stat(name)
	if err != nil || !info.IsDir() || info.Mode().Perm()&0o300 != 0o300 {
		return false
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	return ok && int(stat.Uid) == uid
}
