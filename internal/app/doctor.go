package app

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"syscall"

	"wisp/internal/config"
	"wisp/internal/docker"
	"wisp/internal/process"
	"wisp/internal/project"
)

type doctorReporter struct {
	out                  interface{ Write([]byte) (int, error) }
	passes, warns, fails int
}

func (r *doctorReporter) line(level, check, detail string) {
	if detail != "" {
		_, _ = fmt.Fprintf(r.out, "%s %s: %s\n", level, check, detail)
	} else {
		_, _ = fmt.Fprintf(r.out, "%s %s\n", level, check)
	}
	switch level {
	case "PASS":
		r.passes++
	case "WARN":
		r.warns++
	case "FAIL":
		r.fails++
	}
}

func (a *App) doctor(ctx context.Context, override string) int {
	report := &doctorReporter{out: a.deps.Stdout}
	if (runtime.GOOS == "linux" || runtime.GOOS == "darwin") && a.deps.UID >= 0 && a.deps.GID >= 0 {
		report.line("PASS", "host", fmt.Sprintf("%s UID=%d GID=%d", runtime.GOOS, a.deps.UID, a.deps.GID))
	} else if runtime.GOOS != "linux" && runtime.GOOS != "darwin" {
		report.line("FAIL", "host", runtime.GOOS+" is unsupported")
	} else {
		report.line("FAIL", "host", "UID and GID must be available")
	}

	configEnv := config.Environment{Home: a.env.Home, XDGConfigHome: a.env.XDGConfigHome, XDGDataHome: a.env.XDGDataHome}
	configPath, pathErr := config.ResolvePath(override, a.deps.InvocationDir, configEnv)
	var loaded config.Result
	var effective config.Config
	configResolved := false
	var rawConfig config.RawConfig
	var mountValidationErr error
	if pathErr != nil {
		report.line("FAIL", "effective config", pathErr.Error())
		report.line("FAIL", "config existence", "effective path is unavailable")
		report.line("FAIL", "config validation", "effective path is unavailable")
	} else {
		report.line("PASS", "effective config", configPath)
		info, err := os.Stat(configPath)
		if err != nil {
			report.line("FAIL", "config existence", fmt.Sprintf("%s: %v", configPath, err))
		} else if !info.Mode().IsRegular() {
			report.line("FAIL", "config existence", configPath+" is not a regular file")
		} else {
			report.line("PASS", "config existence", configPath)
			if physical, physicalErr := filepath.EvalSymlinks(configPath); physicalErr == nil {
				configPath = physical
			}
		}
		if data, readErr := os.ReadFile(configPath); readErr == nil {
			if rawConfig, err = config.Decode(data); err == nil {
				if effective, err = config.Resolve(rawConfig); err == nil {
					configResolved = true
				} else if len(rawConfig.Mounts) > 0 {
					withoutMounts := rawConfig
					withoutMounts.Mounts = nil
					if effective, err = config.Resolve(withoutMounts); err == nil {
						configResolved = true
						mountValidationErr = configValidationError(rawConfig)
					}
				}
			}
		}
		loaded, err = config.Load(configPath, configEnv)
		if err != nil {
			report.line("FAIL", "config validation", err.Error())
		} else {
			configPath = loaded.Path
			effective = loaded.Config
			configResolved = true
			report.line("PASS", "config validation", "strict parse and complete validation succeeded")
		}
	}

	if !configResolved {
		for _, check := range []string{"AWS support", "OpenCode config", "OpenCode auth", "Hunk config", "mounts", "mount collisions"} {
			report.line("FAIL", check, "not available because config validation failed")
		}
	} else {
		paths := config.DiagnoseHostPaths(effective, configPath, configEnv)
		if !effective.AWS.Enabled {
			report.line("PASS", "AWS support", "disabled")
		} else {
			if alias, err := config.SelectAWSAlias(effective, ""); err != nil {
				report.line("FAIL", "AWS alias", err.Error())
			} else {
				report.line("PASS", "AWS alias", alias)
			}
			reportHostPath(report, "AWS config", paths.AWSConfig, true)
			reportHostPath(report, "AWS credentials", paths.AWSCredentials, true)
			reportHostPath(report, "AWS SSO cache", paths.AWSSSOCache, true)
		}
		reportHostPath(report, "OpenCode config", paths.OpenCodeConfig, true)
		reportHostPath(report, "OpenCode auth", paths.OpenCodeAuth, true)
		reportHostPath(report, "Hunk config", paths.HunkConfig, true)
		if mountValidationErr == nil {
			for i, configured := range effective.Mounts {
				reportMount(report, i, configured, paths.Mounts[i])
			}
		} else {
			for i, rawMount := range rawConfig.Mounts {
				individual := rawConfig
				individual.Mounts = []config.RawMountConfig{rawMount}
				cfg, err := config.Resolve(individual)
				if err != nil {
					report.line("FAIL", fmt.Sprintf("mount %d", i+1), err.Error())
					continue
				}
				diagnostic := config.DiagnoseHostPaths(cfg, configPath, configEnv).Mounts[0]
				reportMount(report, i, cfg.Mounts[0], diagnostic)
			}
		}
		if len(rawConfig.Mounts) == 0 && len(effective.Mounts) == 0 {
			report.line("PASS", "mounts", "none configured")
		}
		if mountValidationErr != nil {
			report.line("FAIL", "mount collisions", mountValidationErr.Error())
		} else {
			report.line("PASS", "mount collisions", "all configured targets are distinct and non-overlapping")
		}
	}

	checkDirectory(report, "config directory", directoryOf(configPath), a.deps.UID)
	if directories, err := planRuntimeDirectories(a.planOptions()); err != nil {
		report.line("FAIL", "cache directory", err.Error())
		report.line("FAIL", "runtime directory", err.Error())
	} else {
		checkDirectory(report, "cache directory", directories.Assets, a.deps.UID)
		name := "runtime directory"
		if !usableRuntimeDirectory(a.env.XDGRuntimeDir, a.deps.UID) {
			name = "runtime fallback directory"
		}
		checkDirectory(report, name, directories.Private, a.deps.UID)
	}

	if stdout, _, err := a.deps.Runner.Capture(ctx, process.Command{Path: "git", Args: []string{"--version"}, Env: a.childEnvironment(nil)}); err != nil {
		report.line("WARN", "Git", "unavailable; non-worktree project identity will be used")
	} else {
		report.line("PASS", "Git", strings.TrimSpace(string(stdout)))
	}
	resolvedProject, projectErr := project.Resolve(ctx, ".", a.deps.InvocationDir, a.deps.UID, a.gitRoot)
	if projectErr != nil {
		report.line("FAIL", "current project", projectErr.Error())
	} else {
		report.line("PASS", "current project", resolvedProject.RootDir)
	}

	if value, err := a.docker.CheckCLI(ctx); err != nil {
		report.line("FAIL", "Docker executable", err.Error())
	} else {
		report.line("PASS", "Docker executable", value)
	}
	if value, err := a.docker.CheckDocker(ctx); err != nil {
		report.line("FAIL", "Docker daemon", err.Error())
	} else {
		report.line("PASS", "Docker daemon", value)
	}
	if composeVersion, err := a.docker.CheckCompose(ctx); err != nil {
		report.line("FAIL", "Compose", err.Error())
	} else {
		report.line("PASS", "Compose", composeVersion)
	}
	if buildx, err := a.docker.CheckBuildx(ctx); err != nil {
		report.line("FAIL", "Buildx", err.Error())
	} else {
		report.line("PASS", "Buildx", buildx)
	}
	if configResolved {
		images := []struct{ service, name string }{{"sandbox", effective.Images.Sandbox}}
		if effective.AWS.Enabled {
			images = append(images, struct{ service, name string }{"credentials", effective.Images.Credentials})
		}
		for _, image := range images {
			if exists, err := a.docker.ImageExists(ctx, image.name); err != nil {
				report.line("FAIL", image.service+" image", err.Error())
			} else if !exists {
				report.line("WARN", image.service+" image", image.name+" is missing; it will be built on run")
			} else {
				report.line("PASS", image.service+" image", image.name+" is present")
			}
		}
	} else {
		report.line("FAIL", "sandbox image", "not available because config validation failed")
	}
	if projectErr == nil {
		a.doctorResources(ctx, report, resolvedProject)
	} else {
		report.line("FAIL", "project container", "not available because project resolution failed")
		report.line("FAIL", "project resources", "not available because project resolution failed")
	}
	reportDockerEndpoint(report, a.deps.Environment)

	_, _ = fmt.Fprintf(a.deps.Stdout, "Summary: %d PASS, %d WARN, %d FAIL\n", report.passes, report.warns, report.fails)
	if report.fails > 0 {
		return 1
	}
	return 0
}

func reportHostPath(report *doctorReporter, name string, diagnostic config.HostPathDiagnostic, optional bool) {
	if diagnostic.Err != nil {
		report.line("FAIL", name, diagnostic.Err.Error())
	} else if optional && diagnostic.Warning != "" {
		report.line("WARN", name, diagnostic.Warning.String())
	} else {
		report.line("PASS", name, diagnostic.Path)
	}
}

func reportMount(report *doctorReporter, index int, configured config.MountConfig, diagnostic config.HostPathDiagnostic) {
	name := fmt.Sprintf("mount %d", index+1)
	if diagnostic.Err != nil {
		report.line("FAIL", name, diagnostic.Err.Error())
	} else {
		report.line("PASS", name, fmt.Sprintf("%s -> %s (%s)", diagnostic.Path, configured.Target, configured.Mode))
	}
}

func configValidationError(raw config.RawConfig) error {
	_, err := config.Resolve(raw)
	return err
}

func (a *App) doctorResources(ctx context.Context, report *doctorReporter, resolved project.Project) {
	container, err := a.docker.InspectContainer(ctx, resolved.ContainerName)
	if err != nil {
		report.line("FAIL", "project container", err.Error())
	} else if container == nil {
		report.line("PASS", "project container", "none")
	} else if err := docker.VerifyLabels(container.Labels, composeProjectLabels(resolved.Hash, a.deps.UID, "sandbox", resolved.ComposeProject)); err != nil {
		report.line("FAIL", "project container", "unknown name collision: "+err.Error())
	} else if container.Running {
		report.line("PASS", "project container", "active")
	} else {
		report.line("WARN", "project container", "stopped managed sandbox")
	}

	containers, err := a.docker.ProjectContainers(ctx, resolved.ComposeProject)
	if err != nil {
		report.line("FAIL", "project resources", err.Error())
	}
	managed, managedErr := a.docker.ManagedProjectContainers(ctx, resolved.Hash, a.deps.UID)
	if managedErr != nil {
		report.line("FAIL", "managed resources", managedErr.Error())
	}
	byName := make(map[string]docker.Container, len(containers)+len(managed))
	for _, current := range containers {
		byName[current.Name] = current
	}
	for _, current := range managed {
		byName[current.Name] = current
	}
	names := make([]string, 0, len(byName))
	for name := range byName {
		names = append(names, name)
	}
	sort.Strings(names)
	seen := 0
	for _, name := range names {
		current := byName[name]
		if current.Name == resolved.ContainerName {
			continue
		}
		seen++
		expected := composeProjectLabels(resolved.Hash, a.deps.UID, "", resolved.ComposeProject)
		if err := docker.VerifyLabels(current.Labels, expected); err != nil {
			report.line("FAIL", "resource "+current.Name, "mismatched ownership: "+err.Error())
			continue
		}
		kind := current.Labels["wisp.kind"]
		if kind != "sandbox" && kind != "credentials" {
			report.line("FAIL", "resource "+current.Name, fmt.Sprintf("invalid Wisp kind %q", kind))
			continue
		}
		state := "stopped"
		if current.Running {
			state = "active"
		}
		report.line("WARN", "resource "+current.Name, fmt.Sprintf("orphaned %s %s container", state, kind))
	}
	if seen == 0 && err == nil && managedErr == nil {
		report.line("PASS", "project resources", "no orphaned resources")
	}
}

func reportDockerEndpoint(report *doctorReporter, environment []string) {
	endpoint := environmentValue(environment, "DOCKER_HOST")
	dockerContext := environmentValue(environment, "DOCKER_CONTEXT")
	if endpoint != "" && !strings.HasPrefix(endpoint, "unix://") {
		report.line("WARN", "Docker endpoint", endpoint+" may be remote; remote daemons are unsupported")
		return
	}
	if dockerContext != "" && dockerContext != "default" && dockerContext != "desktop-linux" {
		report.line("WARN", "Docker endpoint", "context "+dockerContext+" may be remote; remote daemons are unsupported")
		return
	}
	detail := "local or no remote endpoint detected"
	if endpoint != "" {
		detail = endpoint
	}
	report.line("PASS", "Docker endpoint", detail)
}

func directoryOf(path string) string {
	if path == "" {
		return ""
	}
	return filepath.Dir(path)
}

func checkDirectory(report *doctorReporter, name, path string, uid int) {
	if path == "" {
		report.line("FAIL", name, "path is unavailable")
		return
	}
	info, err := os.Stat(path)
	if os.IsNotExist(err) {
		report.line("WARN", name, path+" does not exist yet")
		return
	}
	if err != nil {
		report.line("FAIL", name, fmt.Sprintf("%s: %v", path, err))
		return
	}
	if !info.IsDir() {
		report.line("FAIL", name, path+" is not a directory")
		return
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || int(stat.Uid) != uid {
		report.line("FAIL", name, path+" is not owned by the current UID")
		return
	}
	if info.Mode().Perm()&0200 == 0 {
		report.line("FAIL", name, path+" is not writable by its owner")
		return
	}
	report.line("PASS", name, path)
}

func environmentValue(environment []string, name string) string {
	prefix := name + "="
	for i := len(environment) - 1; i >= 0; i-- {
		if strings.HasPrefix(environment[i], prefix) {
			return strings.TrimPrefix(environment[i], prefix)
		}
	}
	return ""
}
