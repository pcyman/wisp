package cli

import (
	"reflect"
	"strings"
	"testing"
)

func TestParseRun(t *testing.T) {
	tests := []struct {
		name string
		args []string
		want RunRequest
		help bool
	}{
		{name: "implicit default", want: RunRequest{Directory: "."}},
		{name: "explicit default", args: []string{"run"}, want: RunRequest{Directory: "."}},
		{name: "implicit directory", args: []string{"../service"}, want: RunRequest{Directory: "../service"}},
		{name: "explicit reserved directory", args: []string{"run", "config"}, want: RunRequest{Directory: "config"}},
		{name: "explicit path named like command", args: []string{"./config"}, want: RunRequest{Directory: "./config"}},
		{name: "options before directory", args: []string{"--aws", "prod", "--rebuild", "dir"}, want: RunRequest{Directory: "dir", AWSAlias: "prod", Rebuild: true}},
		{name: "agent override", args: []string{"--agent", "pi", "dir"}, want: RunRequest{Directory: "dir", Agent: "pi"}},
		{name: "options after directory", args: []string{"dir", "--config=cfg.toml", "--aws=dev"}, want: RunRequest{Directory: "dir", ConfigPath: "cfg.toml", AWSAlias: "dev"}},
		{name: "repeatable mounts", args: []string{"--mount", "one", "--mount=two", "--mount-rw", "three", "--mount-rw=four"}, want: RunRequest{Directory: ".", ReadOnlyMounts: []string{"one", "two"}, ReadWriteMounts: []string{"three", "four"}}},
		{name: "delimiter path", args: []string{"run", "--", "-project"}, want: RunRequest{Directory: "-project"}},
		{name: "delimiter ordinary path", args: []string{"--", "dir"}, want: RunRequest{Directory: "dir"}},
		{name: "help short", args: []string{"-h"}, help: true},
		{name: "help long explicit", args: []string{"run", "--help"}, help: true, want: RunRequest{Directory: "."}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := Parse(tt.args)
			if err != nil {
				t.Fatalf("Parse() error = %v", err)
			}
			if tt.args != nil && len(tt.args) == 1 && tt.args[0] == "-h" {
				if got.Command != CommandRoot || !got.ShowHelp {
					t.Fatalf("Parse() = %#v, want root help", got)
				}
				return
			}
			if got.Command != CommandRun || got.ShowHelp != tt.help || !reflect.DeepEqual(got.Run, tt.want) {
				t.Fatalf("Parse() = %#v, want run %#v, help %v", got, tt.want, tt.help)
			}
		})
	}
}

func TestParseRunErrors(t *testing.T) {
	tests := []struct {
		name string
		args []string
		want string
	}{
		{name: "unknown option", args: []string{"--wat"}, want: "unknown option"},
		{name: "second positional", args: []string{"one", "two"}, want: "at most one directory"},
		{name: "second positional after delimiter", args: []string{"--", "one", "two"}, want: "at most one directory"},
		{name: "positional then delimiter positional", args: []string{"one", "--", "two"}, want: "at most one directory"},
		{name: "aws missing", args: []string{"--aws"}, want: "nonempty value"},
		{name: "aws empty", args: []string{"--aws="}, want: "nonempty value"},
		{name: "config missing", args: []string{"--config"}, want: "nonempty value"},
		{name: "agent missing", args: []string{"--agent"}, want: "nonempty value"},
		{name: "duplicate agent", args: []string{"--agent=pi", "--agent", "opencode"}, want: "only be specified once"},
		{name: "mount empty", args: []string{"--mount="}, want: "nonempty value"},
		{name: "mount rw missing", args: []string{"--mount-rw"}, want: "nonempty value"},
		{name: "duplicate aws", args: []string{"--aws", "one", "--aws=two"}, want: "only be specified once"},
		{name: "duplicate config", args: []string{"--config=a", "--config", "b"}, want: "only be specified once"},
		{name: "duplicate rebuild", args: []string{"--rebuild", "--rebuild"}, want: "only be specified once"},
		{name: "rebuild value", args: []string{"--rebuild=yes"}, want: "unknown option"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := Parse(tt.args)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("Parse() error = %v, want containing %q", err, tt.want)
			}
		})
	}
}

func TestParseHerdrUsesRunGrammar(t *testing.T) {
	tests := []struct {
		name string
		args []string
		want RunRequest
		help bool
	}{
		{name: "default", args: []string{"herdr"}, want: RunRequest{Directory: "."}},
		{name: "relative directory", args: []string{"herdr", "."}, want: RunRequest{Directory: "."}},
		{name: "absolute directory", args: []string{"herdr", "/tmp/project"}, want: RunRequest{Directory: "/tmp/project"}},
		{name: "options before directory", args: []string{"herdr", "--aws", "dev", "--mount", "../shared", "--rebuild", "/tmp/project"}, want: RunRequest{Directory: "/tmp/project", AWSAlias: "dev", Rebuild: true, ReadOnlyMounts: []string{"../shared"}}},
		{name: "options after directory", args: []string{"herdr", "/tmp/project", "--aws", "dev", "--config=wisp.toml", "--mount-rw", "../shared"}, want: RunRequest{Directory: "/tmp/project", ConfigPath: "wisp.toml", AWSAlias: "dev", ReadWriteMounts: []string{"../shared"}}},
		{name: "help", args: []string{"herdr", "--help"}, want: RunRequest{Directory: "."}, help: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := Parse(tt.args)
			if err != nil {
				t.Fatalf("Parse() error = %v", err)
			}
			if got.Command != CommandHerdr || got.ShowHelp != tt.help || !reflect.DeepEqual(got.Run, tt.want) {
				t.Fatalf("Parse() = %#v, want Herdr run %#v, help %v", got, tt.want, tt.help)
			}
			if payload := got.Payload(); payload != nil {
				t.Fatalf("Parse() payload = %q, want nil", payload)
			}
		})
	}
}

func TestParseHerdrReservesCommandName(t *testing.T) {
	reserved, err := Parse([]string{"herdr"})
	if err != nil || reserved.Command != CommandHerdr {
		t.Fatalf("Parse(herdr) = %#v, %v; want Herdr command", reserved, err)
	}
	for _, args := range [][]string{{"./herdr"}, {"run", "herdr"}} {
		req, parseErr := Parse(args)
		if parseErr != nil {
			t.Fatalf("Parse(%q) error = %v", args, parseErr)
		}
		if req.Command != CommandRun || req.Run.Directory != args[len(args)-1] {
			t.Fatalf("Parse(%q) = %#v, want ordinary run", args, req)
		}
	}
}

func TestParseHerdrErrorsMatchRun(t *testing.T) {
	for _, suffix := range [][]string{{"--wat"}, {"one", "two"}, {"--aws"}, {"--rebuild", "--rebuild"}} {
		_, runErr := Parse(append([]string{"run"}, suffix...))
		_, herdrErr := Parse(append([]string{"herdr"}, suffix...))
		if runErr == nil || herdrErr == nil || runErr.Error() != herdrErr.Error() {
			t.Fatalf("suffix %q errors = run %v, Herdr %v; want equal errors", suffix, runErr, herdrErr)
		}
	}
}

func TestParseHerdrRejectsPi(t *testing.T) {
	if _, err := Parse([]string{"herdr", "--agent", "pi"}); err == nil || !strings.Contains(err.Error(), "only supports") {
		t.Fatalf("Parse() error = %v", err)
	}
}

func TestRunNeverHasPayload(t *testing.T) {
	inputs := [][]string{
		nil,
		{"project"},
		{"run", "project"},
		{"--aws", "prod", "project"},
		{"project", "--mount", "shared", "--mount-rw=editable"},
		{"--", "-project"},
	}
	for _, args := range inputs {
		req, err := Parse(args)
		if err != nil {
			t.Fatalf("Parse(%q) error = %v", args, err)
		}
		if req.Command != CommandRun {
			t.Fatalf("Parse(%q) command = %q, want run", args, req.Command)
		}
		if payload := req.Payload(); payload != nil {
			t.Fatalf("Parse(%q) payload = %q, want nil", args, payload)
		}
	}

	invalid := [][]string{{"one", "two"}, {"--", "one", "--help"}, {"project", "prompt"}}
	for _, args := range invalid {
		if req, err := Parse(args); err == nil {
			t.Fatalf("Parse(%q) = %#v, want syntax error rather than forwarded args", args, req)
		}
	}
}

func TestParseExecAndHunk(t *testing.T) {
	tests := []struct {
		name    string
		args    []string
		command Command
		dir     string
		payload []string
		help    bool
	}{
		{name: "exec default directory", args: []string{"exec", "--", "git", "status"}, command: CommandExec, dir: ".", payload: []string{"git", "status"}},
		{name: "exec directory and exact payload", args: []string{"exec", "my project", "--", "sh", "two words", "--flag", ""}, command: CommandExec, dir: "my project", payload: []string{"sh", "two words", "--flag", ""}},
		{name: "exec payload delimiter preserved", args: []string{"exec", "--", "tool", "--", "x"}, command: CommandExec, dir: ".", payload: []string{"tool", "--", "x"}},
		{name: "exec help", args: []string{"exec", "--help"}, command: CommandExec, help: true},
		{name: "hunk default", args: []string{"hunk"}, command: CommandHunk, dir: ".", payload: []string{"diff", "--watch"}},
		{name: "hunk directory default", args: []string{"hunk", "dir"}, command: CommandHunk, dir: "dir", payload: []string{"diff", "--watch"}},
		{name: "hunk args", args: []string{"hunk", "dir", "--", "--help", "two words"}, command: CommandHunk, dir: "dir", payload: []string{"--help", "two words"}},
		{name: "hunk empty args defaults", args: []string{"hunk", "--"}, command: CommandHunk, dir: ".", payload: []string{"diff", "--watch"}},
		{name: "hunk launcher help", args: []string{"hunk", "-h"}, command: CommandHunk, help: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req, err := Parse(tt.args)
			if err != nil {
				t.Fatalf("Parse() error = %v", err)
			}
			if req.Command != tt.command || req.ShowHelp != tt.help {
				t.Fatalf("Parse() command/help = %q/%v, want %q/%v", req.Command, req.ShowHelp, tt.command, tt.help)
			}
			if tt.help {
				return
			}
			var dir string
			if tt.command == CommandExec {
				dir = req.Exec.Directory
			} else {
				dir = req.Hunk.Directory
			}
			if dir != tt.dir || !reflect.DeepEqual(req.Payload(), tt.payload) {
				t.Fatalf("Parse() dir/payload = %q/%q, want %q/%q", dir, req.Payload(), tt.dir, tt.payload)
			}
		})
	}
}

func TestParseExecAndHunkErrors(t *testing.T) {
	tests := [][]string{
		{"exec"},
		{"exec", "--"},
		{"exec", "one", "two", "--", "cmd"},
		{"exec", "--unknown", "--", "cmd"},
		{"hunk", "one", "two"},
		{"hunk", "--unknown"},
	}
	for _, args := range tests {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			if req, err := Parse(args); err == nil {
				t.Fatalf("Parse(%q) = %#v, want error", args, req)
			}
		})
	}
}

func TestParseAdministrativeCommands(t *testing.T) {
	tests := []struct {
		name string
		args []string
		want Request
	}{
		{name: "config init", args: []string{"config", "init"}, want: Request{Command: CommandConfigInit}},
		{name: "config path separated", args: []string{"config", "path", "--config", "file"}, want: Request{Command: CommandConfigPath, Config: ConfigRequest{Path: "file"}}},
		{name: "config validate inline", args: []string{"config", "validate", "--config=file"}, want: Request{Command: CommandConfigValidate, Config: ConfigRequest{Path: "file"}}},
		{name: "aws check", args: []string{"aws", "check"}, want: Request{Command: CommandAWSCheck}},
		{name: "aws options around alias", args: []string{"aws", "check", "--rebuild", "prod", "--config=file"}, want: Request{Command: CommandAWSCheck, AWSCheck: AWSCheckRequest{Alias: "prod", ConfigPath: "file", Rebuild: true}}},
		{name: "aws options after alias", args: []string{"aws", "check", "prod", "--config", "file", "--rebuild"}, want: Request{Command: CommandAWSCheck, AWSCheck: AWSCheckRequest{Alias: "prod", ConfigPath: "file", Rebuild: true}}},
		{name: "doctor", args: []string{"doctor", "--config=doctor.toml"}, want: Request{Command: CommandDoctor, Config: ConfigRequest{Path: "doctor.toml"}}},
		{name: "version", args: []string{"version"}, want: Request{Command: CommandVersion}},
		{name: "root version", args: []string{"--version"}, want: Request{Command: CommandVersion}},
		{name: "config help", args: []string{"config", "--help"}, want: Request{Command: CommandConfig, ShowHelp: true}},
		{name: "aws help", args: []string{"aws", "-h"}, want: Request{Command: CommandAWS, ShowHelp: true}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := Parse(tt.args)
			if err != nil {
				t.Fatalf("Parse() error = %v", err)
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("Parse() = %#v, want %#v", got, tt.want)
			}
		})
	}
}

func TestParseAdministrativeErrors(t *testing.T) {
	tests := [][]string{
		{"config"},
		{"config", "unknown"},
		{"config", "init", "extra"},
		{"config", "path", "--config"},
		{"config", "validate", "--config=a", "--config=b"},
		{"config", "init", "--rebuild"},
		{"aws"},
		{"aws", "unknown"},
		{"aws", "check", "one", "two"},
		{"aws", "check", "--"},
		{"aws", "check", "--aws", "alias"},
		{"aws", "check", "--rebuild", "--rebuild"},
		{"aws", "check", "--config="},
		{"doctor", "extra"},
		{"doctor", "--config=a", "--config=b"},
		{"version", "extra"},
		{"--version", "extra"},
	}
	for _, args := range tests {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			if req, err := Parse(args); err == nil {
				t.Fatalf("Parse(%q) = %#v, want error", args, req)
			}
		})
	}
}

func TestParseHelp(t *testing.T) {
	topics := map[string]Command{
		"run": CommandRun, "herdr": CommandHerdr, "exec": CommandExec, "hunk": CommandHunk,
		"config": CommandConfig, "aws": CommandAWS, "doctor": CommandDoctor,
		"version": CommandVersion, "help": CommandHelp,
	}
	for topic, command := range topics {
		req, err := Parse([]string{"help", topic})
		if err != nil {
			t.Fatalf("Parse(help %s) error = %v", topic, err)
		}
		if req.Command != command || !req.ShowHelp {
			t.Fatalf("Parse(help %s) = %#v", topic, req)
		}
		if Help(command) == "" {
			t.Fatalf("Help(%q) is empty", command)
		}
	}

	for _, args := range [][]string{{"help", "unknown"}, {"help", "run", "extra"}} {
		if _, err := Parse(args); err == nil {
			t.Fatalf("Parse(%q) succeeded, want error", args)
		}
	}
}
