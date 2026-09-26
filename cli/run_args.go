package cli

import (
	"encoding/json"
	"flag"
	"fmt"
	"github.com/nqbao/chameleon/runfile"
	"io"
	"os"
	"strings"
)

type runOptions struct {
	agent            string
	runtime          string
	systemPrompt     string
	model            string
	sessionID        string
	yolo             bool
	dir              string
	dockerImage      string
	dockerSocket     string
	dockerArgs       []string
	sandboxMode      bool
	sandboxNoNetwork bool
	prompt           string
	setup            bool
	shell            string
	jsonSchema       string
	tools            []string
	extraArgs        []string
	envFiles         []string
	envFlags         []string
}

func parseRunArgs(args []string) (runOptions, error) { return parseRunArgsIO(args, io.Discard) }

func parseRunArgsIO(args []string, stderr io.Writer) (runOptions, error) {
	var opts runOptions
	fs := flag.NewFlagSet("cham run", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	fs.StringVar(&opts.agent, "agent", "", "agent runtime name (claude, codex, opencode, …) or path to a persona .md file")
	fs.StringVar(&opts.runtime, "runtime", "", "agent runtime (alias for --agent when not using a persona file)")
	fs.StringVar(&opts.model, "model", "", "model name")
	fs.StringVar(&opts.sessionID, "session", "", "session ID to resume")
	fs.BoolVar(&opts.yolo, "yolo", false, "skip permission prompts")
	fs.StringVar(&opts.dir, "dir", "", "working directory (default: current dir)")
	fs.StringVar(&opts.dockerImage, "docker", "", "run agent inside a docker container using this image")
	fs.StringVar(&opts.dockerSocket, "docker-socket", os.Getenv("CHAMELEON_DOCKER_SOCKET"), "docker socket path (e.g. unix:///var/run/docker.sock); empty uses docker's default")
	var dockerArgsFlag string
	fs.StringVar(&dockerArgsFlag, "docker-args", "", "extra `docker run` flags, shell-quoted (e.g. \"--network=host -v /a:/b\"); requires --docker")
	fs.BoolVar(&opts.sandboxMode, "sandbox", false, "enable sandbox (codex/gemini: native --sandbox flag; pi/opencode: macOS sandbox-exec)")
	fs.BoolVar(&opts.sandboxNoNetwork, "sandbox-no-network", false, "deny outbound network inside sandbox (default: allow)")
	fs.StringVar(&opts.prompt, "p", "", "prompt (if omitted, runs agent interactively, passing remaining args through)")
	fs.BoolVar(&opts.setup, "setup", false, "run the agent's setup/login command instead")
	fs.StringVar(&opts.shell, "shell", "", "open a shell instead of running the agent, e.g. /bin/bash (docker mode only; defaults to /bin/sh)")
	fs.StringVar(&opts.jsonSchema, "schema", "", "JSON schema for structured output; inline JSON or path to a .json file (requires -p)")
	fs.Var((*repeatedFlag)(&opts.envFiles), "env-file", "file of NAME=value lines injected into the agent's environment; repeatable")
	fs.Var((*repeatedFlag)(&opts.envFlags), "env", "NAME=value (or NAME to copy from the current environment) injected into the agent's environment; repeatable")
	var toolsFlag string
	fs.StringVar(&toolsFlag, "tools", "", "comma-separated allowlist of tool names (e.g. \"Read,Edit,Bash(git *)\")")

	// First parse: stops at the .md filename (first non-flag positional).
	if err := fs.Parse(args); err != nil {
		return runOptions{}, err
	}

	// track which flags were explicitly set so frontmatter doesn't override them
	explicitFlags := map[string]bool{}
	fs.Visit(func(f *flag.Flag) { explicitFlags[f.Name] = true })

	if dockerArgsFlag != "" {
		args, err := splitArgs(dockerArgsFlag)
		if err != nil {
			return runOptions{}, fmt.Errorf("--docker-args: %w", err)
		}
		opts.dockerArgs = args
	}
	if toolsFlag != "" {
		opts.tools = parseToolsFlag(toolsFlag)
	}

	positional := fs.Args()

	if len(positional) > 0 && strings.HasSuffix(positional[0], ".md") {
		mdFile := positional[0]

		// Partition args after the .md filename into flag args and key=value pairs.
		// flag.FlagSet.Parse stops at the first non-flag positional, so we can't
		// pass the whole slice directly for interleaved input like
		// `name=alice --addr host:8080` or `-p "x=y"`.
		// Track expectFlagValue so that a flag's value token is never misrouted
		// to kvArgs even when it contains '='.
		var flagArgs, kvArgs []string
		expectFlagValue := false
		for _, arg := range positional[1:] {
			if expectFlagValue {
				flagArgs = append(flagArgs, arg)
				expectFlagValue = false
				continue
			}
			if strings.HasPrefix(arg, "-") {
				flagArgs = append(flagArgs, arg)
				// determine whether this flag consumes the next token as its value
				name := strings.TrimLeft(arg, "-")
				if !strings.Contains(name, "=") { // not --flag=value form
					if f := fs.Lookup(name); f != nil {
						if bf, ok := f.Value.(interface{ IsBoolFlag() bool }); !ok || !bf.IsBoolFlag() {
							expectFlagValue = true
						}
					}
				}
			} else if strings.Contains(arg, "=") {
				kvArgs = append(kvArgs, arg)
			} else {
				// bare non-flag token with no '=' — not a valid key=value arg;
				// pass through to SubstitutePlaceholders which will error clearly
				kvArgs = append(kvArgs, arg)
			}
		}
		if err := fs.Parse(flagArgs); err != nil {
			return runOptions{}, err
		}
		fs.Visit(func(f *flag.Flag) { explicitFlags[f.Name] = true })
		if toolsFlag != "" {
			opts.tools = parseToolsFlag(toolsFlag)
		}

		fm, body, err := runfile.ParseMarkdownFile(mdFile)
		if err != nil {
			return runOptions{}, err
		}

		// apply frontmatter only for fields not set on the CLI
		if !explicitFlags["agent"] && fm.Agent != "" {
			opts.agent = fm.Agent
		}
		if !explicitFlags["runtime"] && fm.Runtime != "" {
			opts.runtime = fm.Runtime
		}
		if !explicitFlags["model"] && fm.Model != "" {
			opts.model = fm.Model
		}
		if !explicitFlags["yolo"] && fm.Yolo {
			opts.yolo = fm.Yolo
		}
		if !explicitFlags["docker"] && fm.Docker != "" {
			opts.dockerImage = fm.Docker
		}
		if !explicitFlags["docker-socket"] && fm.DockerSocket != "" {
			opts.dockerSocket = fm.DockerSocket
		}
		if !explicitFlags["sandbox"] && fm.Sandbox {
			opts.sandboxMode = fm.Sandbox
		}
		if !explicitFlags["sandbox-no-network"] && fm.SandboxNoNetwork {
			opts.sandboxNoNetwork = fm.SandboxNoNetwork
		}
		if !explicitFlags["session"] && fm.Session != "" {
			opts.sessionID = fm.Session
		}
		if !explicitFlags["dir"] && fm.Dir != "" {
			opts.dir = fm.Dir
		}
		if !explicitFlags["shell"] && fm.Shell != "" {
			opts.shell = fm.Shell
		}
		if !explicitFlags["setup"] && fm.Setup {
			opts.setup = fm.Setup
		}
		if !explicitFlags["tools"] && len(fm.Tools) > 0 {
			opts.tools = fm.Tools
		}

		if opts.prompt == "" && strings.TrimSpace(body) != "" && !opts.setup && opts.shell == "" {
			prompt, err := runfile.SubstitutePlaceholders(body, mdFile, kvArgs)
			if err != nil {
				return runOptions{}, err
			}
			opts.prompt = prompt
		} else if len(kvArgs) > 0 {
			if opts.prompt != "" {
				fmt.Fprintf(stderr, "cham run: warning: key=value args ignored when -p is supplied\n")
			} else if opts.setup || opts.shell != "" {
				fmt.Fprintf(stderr, "cham run: warning: key=value args ignored in setup/shell mode\n")
			} else {
				// body is whitespace-only → interactive mode; kvArgs have nowhere to go
				fmt.Fprintf(stderr, "cham run: warning: key=value args ignored (body is empty)\n")
			}
		}

		// no extraArgs from positionals when using .md
	} else {
		opts.extraArgs = positional
	}

	// Resolve --agent: persona .md file or bare runtime name.
	if opts.agent != "" {
		if err := applyAgentArg(opts.agent, explicitFlags, &opts); err != nil {
			return runOptions{}, err
		}
	}

	if strings.TrimSpace(opts.runtime) == "" {
		return runOptions{}, fmt.Errorf("--agent (or --runtime) is required")
	}

	if opts.jsonSchema != "" {
		if opts.prompt == "" {
			return runOptions{}, fmt.Errorf("--schema requires -p")
		}
		// Resolve file path vs inline JSON: if the value is already valid JSON,
		// use it as-is; otherwise treat it as a file path.
		if !json.Valid([]byte(opts.jsonSchema)) {
			data, err := os.ReadFile(opts.jsonSchema)
			if err != nil {
				return runOptions{}, fmt.Errorf("--schema: %w", err)
			}
			opts.jsonSchema = strings.TrimSpace(string(data))
		}
	}

	return opts, nil
}

// applyAgentArg resolves the --agent value: if it is a .md path, the file's
// frontmatter supplies config defaults and the body becomes the system prompt;
// otherwise the value is treated as a runtime name.
func applyAgentArg(agent string, explicitFlags map[string]bool, opts *runOptions) error {
	if !strings.HasSuffix(agent, ".md") {
		// bare runtime name — only set if --runtime was not explicit
		if !explicitFlags["runtime"] {
			opts.runtime = agent
		}
		return nil
	}

	fm, body, err := runfile.ParseMarkdownFile(agent)
	if err != nil {
		return fmt.Errorf("--agent: %w", err)
	}

	// Persona frontmatter supplies defaults; explicit CLI flags and previously
	// applied task-.md frontmatter both take precedence.
	if !explicitFlags["runtime"] && opts.runtime == "" && fm.Runtime != "" {
		opts.runtime = fm.Runtime
	}
	if !explicitFlags["model"] && opts.model == "" && fm.Model != "" {
		opts.model = fm.Model
	}
	if !explicitFlags["yolo"] && !opts.yolo && fm.Yolo {
		opts.yolo = fm.Yolo
	}
	if !explicitFlags["docker"] && opts.dockerImage == "" && fm.Docker != "" {
		opts.dockerImage = fm.Docker
	}
	if !explicitFlags["docker-socket"] && opts.dockerSocket == "" && fm.DockerSocket != "" {
		opts.dockerSocket = fm.DockerSocket
	}
	if !explicitFlags["sandbox"] && !opts.sandboxMode && fm.Sandbox {
		opts.sandboxMode = fm.Sandbox
	}
	if !explicitFlags["sandbox-no-network"] && !opts.sandboxNoNetwork && fm.SandboxNoNetwork {
		opts.sandboxNoNetwork = fm.SandboxNoNetwork
	}
	if !explicitFlags["tools"] && len(opts.tools) == 0 && len(fm.Tools) > 0 {
		opts.tools = fm.Tools
	}

	if body := strings.TrimSpace(body); body != "" {
		opts.systemPrompt = body
	}
	return nil
}

// splitArgs splits s into words, honoring single quotes, double quotes and
// backslash escapes. It does not expand variables or run a shell.
func splitArgs(s string) ([]string, error) {
	var args []string
	var cur strings.Builder
	inWord := false
	var quote rune
	escaped := false
	for _, r := range s {
		switch {
		case escaped:
			cur.WriteRune(r)
			escaped = false
		case r == '\\' && quote != '\'':
			escaped, inWord = true, true
		case quote != 0:
			if r == quote {
				quote = 0
			} else {
				cur.WriteRune(r)
			}
		case r == '\'' || r == '"':
			quote, inWord = r, true
		case r == ' ' || r == '\t' || r == '\n':
			if inWord {
				args = append(args, cur.String())
				cur.Reset()
				inWord = false
			}
		default:
			cur.WriteRune(r)
			inWord = true
		}
	}
	if quote != 0 || escaped {
		return nil, fmt.Errorf("unterminated quote or escape")
	}
	if inWord {
		args = append(args, cur.String())
	}
	return args, nil
}

func parseToolsFlag(s string) []string {
	var tools []string
	for _, t := range strings.Split(s, ",") {
		if t = strings.TrimSpace(t); t != "" {
			tools = append(tools, t)
		}
	}
	return tools
}
