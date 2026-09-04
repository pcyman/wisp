package app

import (
	"fmt"

	"wisp/internal/cli"
	"wisp/internal/config"
)

func (a *App) config(request cli.Request) (int, error) {
	env := config.Environment{Home: a.env.Home, XDGConfigHome: a.env.XDGConfigHome, XDGDataHome: a.env.XDGDataHome}
	path, err := config.ResolvePath(request.Config.Path, a.deps.InvocationDir, env)
	if err != nil {
		return 1, err
	}
	switch request.Command {
	case cli.CommandConfigPath:
		fmt.Fprintln(a.deps.Stdout, path)
	case cli.CommandConfigInit:
		if err := config.Init(path); err != nil {
			return 1, err
		}
		fmt.Fprintf(a.deps.Stderr, "created configuration: %s\n%s\n", path, config.InitReminder)
	case cli.CommandConfigValidate:
		result, err := config.Validate(path, env)
		if err != nil {
			return 1, err
		}
		for _, warning := range result.Warnings {
			fmt.Fprintf(a.deps.Stderr, "warning: %s\n", warning)
		}
		fmt.Fprintf(a.deps.Stdout, "configuration is valid: %s\n", result.Path)
	}
	return 0, nil
}
