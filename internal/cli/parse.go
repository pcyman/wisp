// Package cli parses Wisp's command line without performing I/O.
package cli

import (
	"fmt"
	"strings"
)

type Command string

const (
	CommandRoot           Command = ""
	CommandRun            Command = "run"
	CommandAgents         Command = "agents"
	CommandHerdr          Command = "herdr"
	CommandExec           Command = "exec"
	CommandHunk           Command = "hunk"
	CommandConfig         Command = "config"
	CommandConfigInit     Command = "config init"
	CommandConfigPath     Command = "config path"
	CommandConfigValidate Command = "config validate"
	CommandAWS            Command = "aws"
	CommandAWSCheck       Command = "aws check"
	CommandDoctor         Command = "doctor"
	CommandVersion        Command = "version"
	CommandHelp           Command = "help"
)

type Request struct {
	Command  Command
	ShowHelp bool

	Run      RunRequest
	Exec     ExecRequest
	Hunk     HunkRequest
	Config   ConfigRequest
	AWSCheck AWSCheckRequest
}

type RunRequest struct {
	Directory       string
	ConfigPath      string
	AWSAlias        string
	Rebuild         bool
	ReadOnlyMounts  []string
	ReadWriteMounts []string
}

type ExecRequest struct {
	Directory string
	Command   []string
}

type HunkRequest struct {
	Directory string
	Args      []string
}

type ConfigRequest struct {
	Path string
}

type AWSCheckRequest struct {
	Alias      string
	ConfigPath string
	Rebuild    bool
}

// Payload returns a defensive copy of argv intended for a process inside an
// existing sandbox. Run requests never have a payload.
func (r Request) Payload() []string {
	var payload []string
	switch r.Command {
	case CommandExec:
		payload = r.Exec.Command
	case CommandHunk:
		payload = r.Hunk.Args
	}
	return append([]string(nil), payload...)
}

// Parse converts argv (excluding the executable name) into a request.
func Parse(args []string) (Request, error) {
	if len(args) == 0 {
		return parseRun(nil)
	}

	if len(args) == 1 {
		switch args[0] {
		case "-h", "--help":
			return Request{Command: CommandRoot, ShowHelp: true}, nil
		case "--version":
			return Request{Command: CommandVersion}, nil
		}
	}

	switch args[0] {
	case "agents":
		if len(args) == 2 && (args[1] == "--help" || args[1] == "-h") {
			return Request{Command: CommandAgents, ShowHelp: true}, nil
		}
		if len(args) != 2 || args[1] != "--json" {
			return Request{}, syntaxf("agents requires --json and accepts no other arguments")
		}
		return Request{Command: CommandAgents}, nil
	case "run":
		return parseRun(args[1:])
	case "herdr":
		return parseHerdr(args[1:])
	case "exec":
		return parseExec(args[1:])
	case "hunk":
		return parseHunk(args[1:])
	case "config":
		return parseConfig(args[1:])
	case "aws":
		return parseAWS(args[1:])
	case "doctor":
		return parseDoctor(args[1:])
	case "version":
		if len(args) != 1 {
			return Request{}, syntaxf("version accepts no arguments")
		}
		return Request{Command: CommandVersion}, nil
	case "help":
		return parseHelp(args[1:])
	default:
		return parseRun(args)
	}
}

func parseHerdr(args []string) (Request, error) {
	req, err := parseRun(args)
	if err != nil {
		return Request{}, err
	}
	req.Command = CommandHerdr
	return req, nil
}

func parseRun(args []string) (Request, error) {
	req := Request{Command: CommandRun}
	var directorySet, awsSet, configSet, rebuildSet bool
	options := true

	for i := 0; i < len(args); i++ {
		arg := args[i]
		if options && arg == "--" {
			options = false
			continue
		}
		if options && (arg == "-h" || arg == "--help") {
			req.ShowHelp = true
			continue
		}
		if options && arg == "--rebuild" {
			if rebuildSet {
				return Request{}, syntaxf("--rebuild may only be specified once")
			}
			rebuildSet, req.Run.Rebuild = true, true
			continue
		}
		if options && strings.HasPrefix(arg, "-") {
			name, inline, value := splitOption(arg)
			switch name {
			case "--aws":
				if awsSet {
					return Request{}, syntaxf("--aws may only be specified once")
				}
				value, i = optionValue(args, i, name, inline, value)
				if value == "" {
					return Request{}, syntaxf("%s requires a nonempty value", name)
				}
				awsSet, req.Run.AWSAlias = true, value
			case "--config":
				if configSet {
					return Request{}, syntaxf("--config may only be specified once")
				}
				value, i = optionValue(args, i, name, inline, value)
				if value == "" {
					return Request{}, syntaxf("%s requires a nonempty value", name)
				}
				configSet, req.Run.ConfigPath = true, value
			case "--mount", "--mount-rw":
				value, i = optionValue(args, i, name, inline, value)
				if value == "" {
					return Request{}, syntaxf("%s requires a nonempty value", name)
				}
				if name == "--mount" {
					req.Run.ReadOnlyMounts = append(req.Run.ReadOnlyMounts, value)
				} else {
					req.Run.ReadWriteMounts = append(req.Run.ReadWriteMounts, value)
				}
			default:
				return Request{}, syntaxf("unknown option %q", arg)
			}
			continue
		}
		if directorySet {
			return Request{}, syntaxf("run accepts at most one directory")
		}
		directorySet, req.Run.Directory = true, arg
	}

	if !directorySet {
		req.Run.Directory = "."
	}
	return req, nil
}

func parseExec(args []string) (Request, error) {
	req := Request{Command: CommandExec}
	delimiter := index(args, "--")
	prefix := args
	if delimiter >= 0 {
		prefix = args[:delimiter]
		req.Exec.Command = append([]string(nil), args[delimiter+1:]...)
	}

	if help, err := parsePayloadPrefix(prefix, &req.Exec.Directory, "exec"); err != nil {
		return Request{}, err
	} else if help {
		req.ShowHelp = true
		return req, nil
	}
	if delimiter < 0 {
		return Request{}, syntaxf("exec requires -- followed by a command")
	}
	if len(req.Exec.Command) == 0 {
		return Request{}, syntaxf("exec requires at least one command token after --")
	}
	return req, nil
}

func parseHunk(args []string) (Request, error) {
	req := Request{Command: CommandHunk}
	delimiter := index(args, "--")
	prefix := args
	if delimiter >= 0 {
		prefix = args[:delimiter]
		req.Hunk.Args = append([]string(nil), args[delimiter+1:]...)
	}

	if help, err := parsePayloadPrefix(prefix, &req.Hunk.Directory, "hunk"); err != nil {
		return Request{}, err
	} else if help {
		req.ShowHelp = true
		return req, nil
	}
	if len(req.Hunk.Args) == 0 {
		req.Hunk.Args = []string{"diff", "--watch"}
	}
	return req, nil
}

func parsePayloadPrefix(args []string, directory *string, command string) (bool, error) {
	for _, arg := range args {
		if arg == "-h" || arg == "--help" {
			return true, nil
		}
		if strings.HasPrefix(arg, "-") {
			return false, syntaxf("unknown %s option %q", command, arg)
		}
		if *directory != "" {
			return false, syntaxf("%s accepts at most one directory before --", command)
		}
		*directory = arg
	}
	if *directory == "" {
		*directory = "."
	}
	return false, nil
}

func parseConfig(args []string) (Request, error) {
	if len(args) == 1 && (args[0] == "-h" || args[0] == "--help") {
		return Request{Command: CommandConfig, ShowHelp: true}, nil
	}
	if len(args) == 0 {
		return Request{}, syntaxf("config requires an action: init, path, or validate")
	}

	var command Command
	switch args[0] {
	case "init":
		command = CommandConfigInit
	case "path":
		command = CommandConfigPath
	case "validate":
		command = CommandConfigValidate
	default:
		return Request{}, syntaxf("unknown config action %q", args[0])
	}
	req := Request{Command: command}
	path, err := parseConfigOption(args[1:])
	if err != nil {
		return Request{}, err
	}
	req.Config.Path = path
	return req, nil
}

func parseAWS(args []string) (Request, error) {
	if len(args) == 1 && (args[0] == "-h" || args[0] == "--help") {
		return Request{Command: CommandAWS, ShowHelp: true}, nil
	}
	if len(args) == 0 || args[0] != "check" {
		return Request{}, syntaxf("aws requires the check action")
	}

	req := Request{Command: CommandAWSCheck}
	var aliasSet, configSet, rebuildSet bool
	for i := 1; i < len(args); i++ {
		arg := args[i]
		name, inline, value := splitOption(arg)
		switch {
		case name == "--config":
			if configSet {
				return Request{}, syntaxf("--config may only be specified once")
			}
			value, i = optionValue(args, i, name, inline, value)
			if value == "" {
				return Request{}, syntaxf("%s requires a nonempty value", name)
			}
			configSet, req.AWSCheck.ConfigPath = true, value
		case arg == "--rebuild":
			if rebuildSet {
				return Request{}, syntaxf("--rebuild may only be specified once")
			}
			rebuildSet, req.AWSCheck.Rebuild = true, true
		case strings.HasPrefix(arg, "-"):
			return Request{}, syntaxf("unknown aws check option %q", arg)
		default:
			if aliasSet {
				return Request{}, syntaxf("aws check accepts at most one alias")
			}
			aliasSet, req.AWSCheck.Alias = true, arg
		}
	}
	return req, nil
}

func parseDoctor(args []string) (Request, error) {
	req := Request{Command: CommandDoctor}
	path, err := parseConfigOption(args)
	if err != nil {
		return Request{}, err
	}
	req.Config.Path = path
	return req, nil
}

func parseConfigOption(args []string) (string, error) {
	var path string
	set := false
	for i := 0; i < len(args); i++ {
		arg := args[i]
		name, inline, value := splitOption(arg)
		if name != "--config" {
			return "", syntaxf("unexpected argument %q", arg)
		}
		if set {
			return "", syntaxf("--config may only be specified once")
		}
		value, i = optionValue(args, i, name, inline, value)
		if value == "" {
			return "", syntaxf("--config requires a nonempty value")
		}
		set, path = true, value
	}
	return path, nil
}

func parseHelp(args []string) (Request, error) {
	if len(args) > 1 {
		return Request{}, syntaxf("help accepts at most one command")
	}
	if len(args) == 0 {
		return Request{Command: CommandRoot, ShowHelp: true}, nil
	}
	command, ok := helpCommand(args[0])
	if !ok {
		return Request{}, syntaxf("unknown help topic %q", args[0])
	}
	return Request{Command: command, ShowHelp: true}, nil
}

func helpCommand(topic string) (Command, bool) {
	switch Command(topic) {
	case CommandAgents, CommandRun, CommandHerdr, CommandExec, CommandHunk, CommandConfig, CommandAWS,
		CommandDoctor, CommandVersion, CommandHelp:
		return Command(topic), true
	default:
		return CommandRoot, false
	}
}

func splitOption(arg string) (name string, inline bool, value string) {
	name, value, inline = strings.Cut(arg, "=")
	return name, inline, value
}

func optionValue(args []string, i int, name string, inline bool, value string) (string, int) {
	if inline {
		return value, i
	}
	if i+1 >= len(args) {
		return "", i
	}
	return args[i+1], i + 1
}

func index(args []string, target string) int {
	for i, arg := range args {
		if arg == target {
			return i
		}
	}
	return -1
}

func syntaxf(format string, args ...any) error {
	return fmt.Errorf(format, args...)
}
