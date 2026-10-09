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
	Project             project.Project
	LinkedWorktree      *linkedWorktree
	ConfigPath          string
	ConfigSnapshot      []byte
	AWSEnabled          bool
	SelectedAWSAlias    string
	AWSConfigPath       string
	AWSCredentialsPath  string
	AWSSSOCachePath     string
	UID                 int
	GID                 int
	Images              config.ImageConfig
	Versions            config.VersionConfig
	BuildCPUs           int
	Rebuild             bool
	AgentKey            string
	AgentName           string
	Command             []string
	Mounts              []docker.Mount
	AgentDataRoot       string
	AgentDataMountIndex int
	SharedPiProfile     bool
	PiProfilePath       string
	Environment         map[string]string
	SandboxEnvironment  map[string]string
	RuntimeDirectories  RuntimeDirectories
	Warnings            []config.Warning
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

	azureEnvironment, err := planAzureEnvironment(options.ProcessEnv)
	if err != nil {
		return SandboxPlan{}, err
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
	loaded, err := config.LoadForRun(configPath, cfgEnv, request.Agent)
	if err != nil {
		return SandboxPlan{}, err
	}

	sandboxEnvironment := planEnvPassthrough(loaded.Config.EnvPassthrough, options.ProcessEnv)
	var gitRoot project.GitRootFunc
	if options.Runner != nil {
		gitRoot = func(ctx context.Context, dir string) (string, error) {
			stdout, _, err := options.Runner.Capture(ctx, process.Command{
				Path: "git",
				Args: []string{"-C", dir, "rev-parse", "--show-toplevel"},
				Env:  docker.SanitizeEnvironment(withoutPassthrough(options.ProcessEnv, sandboxEnvironment), nil),
			})
			return string(stdout), err
		}
	}
	resolvedProject, err := project.Resolve(ctx, request.Directory, options.InvocationDir, options.UID, gitRoot)
	if err != nil {
		return SandboxPlan{}, err
	}
	linkedWorktree, err := planLinkedWorktree(resolvedProject.RootDir)
	if err != nil {
		return SandboxPlan{}, fmt.Errorf("plan linked worktree: %w", err)
	}
	selectedAlias, err := config.SelectAWSAlias(loaded.Config, request.AWSAlias)
	if err != nil {
		return SandboxPlan{}, err
	}
	awsInputs := loaded.Config.AWS
	if selectedAlias == "" {
		awsInputs = config.AWSConfig{}
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
	agentDataRoot, err := planAgentDataRoot(options.Environment)
	if err != nil {
		return SandboxPlan{}, err
	}
	selectedAgent, err := agent.Select(loaded.SelectedAgent, "")
	if err != nil {
		return SandboxPlan{}, err
	}
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
	if options.Environment.Home != "" {
		skills := filepath.Join(options.Environment.Home, ".agents", "skills")
		physical, statErr := filepath.EvalSymlinks(skills)
		if statErr == nil {
			info, err := os.Stat(physical)
			if err != nil {
				return SandboxPlan{}, fmt.Errorf("inspect host skills directory: %w", err)
			}
			if !info.IsDir() {
				return SandboxPlan{}, fmt.Errorf("host skills path %q is not a directory", skills)
			}
			planned, err := docker.Bind(physical, "/home/sandbox/.agents/skills", true)
			if err != nil {
				return SandboxPlan{}, err
			}
			plannedMounts = append(plannedMounts, planned)
		} else if !os.IsNotExist(statErr) {
			return SandboxPlan{}, fmt.Errorf("resolve host skills directory: %w", statErr)
		}
	}
	agentMountStart := len(plannedMounts)
	agentMounts, err := selectedAgent.HostMounts(loaded.Config)
	if err != nil {
		return SandboxPlan{}, err
	}
	plannedMounts = append(plannedMounts, agentMounts...)
	agentDataMountIndex := agentMountStart
	sharedPiProfile := selectedAgent.Key() == "pi" && loaded.Config.Pi.ConfigPath != ""
	if sharedPiProfile {
		// Pi's project-local trust overlay must follow its shared parent mount.
		agentDataMountIndex++
	}
	for _, extra := range extraMounts {
		planned, err := docker.Bind(extra.Source, extra.Target, extra.Mode == mount.ReadOnly)
		if err != nil {
			return SandboxPlan{}, err
		}
		plannedMounts = append(plannedMounts, planned)
	}

	environment := plannedEnvironment(options, loaded.Config, selectedAlias, resolvedProject.Hash)
	for key, value := range azureEnvironment {
		environment[key] = value
	}

	return SandboxPlan{
		Project:             resolvedProject,
		LinkedWorktree:      linkedWorktree,
		ConfigPath:          loaded.Path,
		ConfigSnapshot:      append([]byte(nil), loaded.Snapshot...),
		AWSEnabled:          selectedAlias != "",
		SelectedAWSAlias:    selectedAlias,
		AWSConfigPath:       awsInputs.HostConfigPath,
		AWSCredentialsPath:  awsInputs.HostCredentialsPath,
		AWSSSOCachePath:     awsInputs.SSOCachePath,
		UID:                 options.UID,
		GID:                 options.GID,
		Images:              loaded.Config.Images,
		Versions:            loaded.Config.Build.Versions,
		BuildCPUs:           loaded.Config.Build.CPUs,
		Rebuild:             request.Rebuild,
		AgentKey:            selectedAgent.Key(),
		AgentName:           selectedAgent.Name(),
		Command:             append([]string(nil), selectedAgent.ContainerCommand()...),
		Mounts:              plannedMounts,
		AgentDataRoot:       agentDataRoot,
		AgentDataMountIndex: agentDataMountIndex,
		SharedPiProfile:     sharedPiProfile,
		PiProfilePath:       loaded.Config.Pi.ConfigPath,
		Environment:         environment,
		SandboxEnvironment:  sandboxEnvironment,
		RuntimeDirectories:  directories,
		Warnings:            append([]config.Warning(nil), loaded.Warnings...),
	}, nil
}

func planAgentDataRoot(environment HostEnvironment) (string, error) {
	base := environment.XDGDataHome
	if base == "" {
		if environment.Home == "" {
			return "", fmt.Errorf("determine agent data directory: neither XDG_DATA_HOME nor HOME is set")
		}
		base = filepath.Join(environment.Home, ".local", "share")
	}
	if !filepath.IsAbs(base) {
		return "", fmt.Errorf("agent data base %q is not absolute", base)
	}
	return filepath.Join(filepath.Clean(base), "wisp"), nil
}

func plannedEnvironment(options PlanOptions, cfg config.Config, alias, projectHash string) map[string]string {
	versions := cfg.Build.Versions
	environment := map[string]string{
		"WISP_IMAGE":             cfg.Images.Sandbox,
		"WISP_CREDENTIALS_IMAGE": cfg.Images.Credentials,
		"WISP_UID":               strconv.Itoa(options.UID),
		"WISP_GID":               strconv.Itoa(options.GID),
		"WISP_PROJECT_HASH":      projectHash,
		"WISP_CLI_VERSION":       options.CLIVersion,
		"OPENCODE_VERSION":       versions.OpenCode,
		"PI_VERSION":             versions.Pi,
		"HUNK_VERSION":           versions.Hunk,
		"AWS_CLI_VERSION":        versions.AWSCLI,
		"KUBECTL_VERSION":        versions.Kubectl,
		"HELM_VERSION":           versions.Helm,
		"TERRAFORM_VERSION":      versions.Terraform,
		"YQ_VERSION":             versions.YQ,
		"UV_VERSION":             versions.UV,
		"GO_VERSION":             versions.Go,
		"BOTO3_VERSION":          versions.Boto3,
	}
	if alias != "" {
		environment["WISP_AWS_ALIAS"] = alias
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
