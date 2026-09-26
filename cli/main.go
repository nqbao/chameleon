// Package cli exposes the cham command without exiting the caller's process.
package cli

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	chameleon "github.com/nqbao/chameleon"
	"github.com/nqbao/chameleon/adapter"
)

// IO supplies all command input and output. Nil outputs discard; nil input is EOF.
type IO struct {
	Stdin          io.Reader
	Stdout, Stderr io.Writer
}

// OptionsFromEnv resolves CLI defaults from CHAMELEON_* variables. Home holds
// the secret store and defaults to ~/.chameleon.
func OptionsFromEnv() (chameleon.Options, error) {
	userHome, err := os.UserHomeDir()
	if err != nil {
		return chameleon.Options{}, err
	}
	home := os.Getenv("CHAMELEON_HOME")
	if home == "" {
		home = filepath.Join(userHome, ".chameleon")
	}
	home, err = filepath.Abs(home)
	if err != nil {
		return chameleon.Options{}, err
	}
	dockerHome := os.Getenv("CHAMELEON_DOCKER_HOME")
	if dockerHome == "" {
		dockerHome = filepath.Join(home, "docker")
	}
	return chameleon.Options{Home: home, DockerHome: dockerHome, WorkspaceDir: ".chameleon", UserHome: userHome, TempDir: os.TempDir(), Passphrase: []byte(os.Getenv("CHAMELEON_SECRET_PASSPHRASE")), BaseEnv: os.Environ()}, nil
}

// Main dispatches a command and returns its process exit status.
func Main(ctx context.Context, args []string, streams IO) int {
	if streams.Stdout == nil {
		streams.Stdout = io.Discard
	}
	if streams.Stderr == nil {
		streams.Stderr = io.Discard
	}
	if streams.Stdin == nil {
		streams.Stdin = strings.NewReader("")
	}
	if len(args) == 0 || args[0] == "--help" || args[0] == "-h" || args[0] == "help" {
		fmt.Fprintln(streams.Stdout, "usage: cham run --agent <runtime|persona.md> [-p prompt] [--docker image|--sandbox]")
		return 0
	}
	switch args[0] {
	case "run":
		return run(ctx, args[1:], streams)
	default:
		fmt.Fprintf(streams.Stderr, "unknown command %q\n", args[0])
		return 2
	}
}

func run(ctx context.Context, args []string, streams IO) int {
	opts, err := parseRunArgsIO(args, streams.Stderr)
	if errors.Is(err, flag.ErrHelp) {
		fmt.Fprintln(streams.Stdout, "usage: cham run --agent <runtime|persona.md> [-p prompt] [task.md key=value ...]\nflags: --runtime --model --session --yolo --dir --docker --docker-socket --docker-args\n       --sandbox --sandbox-no-network --setup --shell --schema --tools --env --env-file")
		return 0
	}
	if err != nil {
		fmt.Fprintln(streams.Stderr, err)
		return 2
	}
	if opts.sandboxMode && opts.dockerImage != "" {
		fmt.Fprintln(streams.Stderr, "--sandbox and --docker cannot be used together")
		return 2
	}
	options, err := OptionsFromEnv()
	if err != nil {
		fmt.Fprintln(streams.Stderr, err)
		return 1
	}
	if opts.dir == "" {
		opts.dir, err = os.Getwd()
		if err != nil {
			fmt.Fprintln(streams.Stderr, err)
			return 1
		}
	}
	kind := chameleon.Host
	if opts.sandboxMode {
		kind = chameleon.Sandbox
	}
	if opts.dockerImage != "" {
		kind = chameleon.Docker
	}
	if kind == chameleon.Docker {
		opts.dockerArgs, err = resolveDockerArgs(options.Home, opts.dir, opts.dockerArgs, streams.Stderr)
		if err != nil {
			fmt.Fprintln(streams.Stderr, err)
			return 1
		}
	}
	envVars, err := resolveEnvVars(options.Home, opts.envFiles, opts.envFlags, os.LookupEnv)
	if err != nil {
		fmt.Fprintln(streams.Stderr, err)
		return 1
	}
	result, err := options.Run(ctx, chameleon.Request{Runtime: opts.runtime, Model: opts.model, Prompt: opts.prompt, SystemPrompt: opts.systemPrompt, SessionID: opts.sessionID, Dir: opts.dir, Env: kind, Image: opts.dockerImage, DockerSocket: opts.dockerSocket, DockerArgs: opts.dockerArgs, NoNetwork: opts.sandboxNoNetwork, Yolo: opts.yolo, Interactive: opts.prompt == "", Setup: opts.setup, Shell: opts.shell, JSONSchema: opts.jsonSchema, Tools: opts.tools, EnvVars: envVars, ExtraArgs: opts.extraArgs, Stdin: streams.Stdin, Stdout: streams.Stdout, Stderr: streams.Stderr})
	if err != nil {
		if result.Content != "" {
			fmt.Fprintln(streams.Stderr, result.Content)
		}
		fmt.Fprintln(streams.Stderr, err)
		if result.ExitCode > 0 {
			return result.ExitCode
		}
		return 1
	}
	for _, tc := range adapter.ResolveDetails(result.ToolCalls) {
		fmt.Fprintf(streams.Stderr, "  %s: %s\n", tc.Name, tc.Detail)
	}
	if result.Content != "" {
		fmt.Fprintln(streams.Stdout, result.Content)
	}
	if result.SessionID != "" {
		fmt.Fprintf(streams.Stderr, "session-id: %s\n", result.SessionID)
	}
	return 0
}
