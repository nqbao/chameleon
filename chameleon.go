// Package chameleon runs agent CLIs on the host, in Docker, or in a sandbox.
// Callers supply configuration and inherited environment explicitly; the library
// does not read config files or CHAMELEON_* variables.
package chameleon

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/nqbao/chameleon/adapter"
	"github.com/nqbao/chameleon/environment"
)

// Options contains caller-owned configuration. BaseEnv is the complete inherited
// subprocess environment; nil means empty. UserHome and TempDir configure sandbox
// paths. Home contains the secret store.
type Options struct {
	Home, DockerHome, WorkspaceDir, UserHome, TempDir string
	Passphrase                                        []byte
	BaseEnv, SensitiveEnv                             []string
}

type Workspace = environment.Workspace

// DockerOpts configures container creation and optional terminal persistence.
type DockerOpts struct {
	Image, Socket, TmuxConfig string
	RunArgs                   []string // extra `docker run` flags, verbatim; trusted input that can break isolation
	Providers                 []environment.CredentialProvider
	InnerTmux                 bool
	ExtraVolumes              []string
}

type EnvKind string

const (
	Host    EnvKind = "host"
	Docker  EnvKind = "docker"
	Sandbox EnvKind = "sandbox"
)

// Request describes one invocation. Interactive IO is supplied by the caller;
// nil writers discard output. OnStreamEvent runs synchronously while reading.
type Request struct {
	Runtime, Model, Prompt, SystemPrompt, SessionID string
	Dir                                             string
	Env                                             EnvKind
	Image, DockerSocket                             string
	DockerArgs                                      []string // extra `docker run` flags, trusted input that can break isolation; docker only
	NoNetwork, Yolo, Interactive, Setup             bool
	Shell, JSONSchema                               string
	Tools, ExtraArgs, Files, Images                 []string
	Stdin                                           io.Reader
	Stdout, Stderr                                  io.Writer
	OnStreamEvent                                   func(adapter.StreamEvent)
}

type Result struct {
	Content, SessionID, RawOutput string
	ToolCalls                     []adapter.ToolCall
	ExitCode                      int
}

// Run builds and executes an agent command, returning its parsed output and exit
// status. It never exits the calling process or writes to process-wide stdout.
func (o Options) Run(ctx context.Context, req Request) (Result, error) {
	fail := func(err error) (Result, error) { return Result{ExitCode: 1}, err }
	if req.Dir == "" {
		return fail(fmt.Errorf("working directory is required"))
	}
	dir, err := filepath.Abs(req.Dir)
	if err != nil {
		return fail(err)
	}
	req.Dir = dir
	if err := guardArgs(&req); err != nil {
		return fail(err)
	}
	kind := req.Env
	if kind == "" {
		kind = Host
	}
	if err := validateEnvironment(req.Runtime, string(kind), req.Image, o.BaseEnv); err != nil {
		return fail(err)
	}
	if kind != Docker && req.Image != "" {
		return fail(fmt.Errorf("image requires docker environment"))
	}
	if kind != Docker && len(req.DockerArgs) > 0 {
		return fail(fmt.Errorf("docker args require docker environment"))
	}
	rt, err := adapter.AgentRuntimeFor(req.Runtime)
	if err != nil {
		return fail(err)
	}
	ws := Workspace{Path: req.Dir}
	var spec environment.EnvironmentSpec
	var env environment.Environment
	switch kind {
	case Docker:
		var providers []environment.CredentialProvider
		if p := rt.Credentials(); p != nil {
			providers = append(providers, p)
		}
		spec, err = o.DockerSpec(ws, DockerOpts{Image: req.Image, Socket: req.DockerSocket, RunArgs: req.DockerArgs, Providers: providers})
		env = environment.NewDockerEnvironment(spec)
	case Sandbox:
		switch adapter.NormalizeChatRuntime(req.Runtime) {
		case "codex", "gemini", "claude":
			spec, err = o.HostSpec(ws, nil)
			env = environment.NewHostEnvironment(spec)
			if req.NoNetwork && req.Stderr != nil {
				fmt.Fprintf(req.Stderr, "warning: --sandbox-no-network has no effect for %s\n", rt.Name())
			}
		default:
			spec, err = o.SandboxSpec(ws, req.NoNetwork)
			env = environment.NewSandboxEnvironment(spec)
		}
	default:
		spec, err = o.HostSpec(ws, nil)
		env = environment.NewHostEnvironment(spec)
	}
	if err != nil {
		return fail(err)
	}
	workspaceDir := req.Dir
	if kind == Docker {
		workspaceDir = environment.ContainerWorkspaceDir(ws)
	}
	interactive := req.Interactive || req.Setup || req.Shell != ""
	var cmd adapter.AgentCommand
	switch {
	case req.Shell != "":
		if kind != Docker {
			return fail(fmt.Errorf("--shell requires --docker"))
		}
		cmd.Name = req.Shell
	case req.Setup:
		argv := rt.SetupCmd()
		if len(argv) == 0 {
			return fail(fmt.Errorf("no setup command for %s", rt.Name()))
		}
		cmd.Name = argv[0]
		cmd.Args = argv[1:]
	default:
		cmd, err = rt.BuildCommand(adapter.AgentCommandContext{Prompt: req.Prompt, SystemPrompt: req.SystemPrompt, WorkspaceDir: workspaceDir, SessionID: req.SessionID, Model: req.Model, Yolo: req.Yolo, Interactive: interactive, Sandbox: kind == Sandbox, ExtraArgs: req.ExtraArgs, JSONSchema: req.JSONSchema, Tools: req.Tools, Files: req.Files, Images: req.Images})
		if err != nil {
			return fail(err)
		}
		if interactive {
			cmd.Args = append(cmd.Args, req.ExtraArgs...)
		}
	}
	defer func() {
		for _, p := range cmd.TempFiles {
			_ = os.Remove(p)
		}
	}()
	execution, err := env.Launch(ctx, environment.LaunchSpec{Argv: append([]string{cmd.Name}, cmd.Args...), Env: cmd.Env, UnsetEnv: cmd.UnsetEnv, Interactive: interactive, MergeStderr: true, ExtraVolumes: cmd.TempMounts, Stdin: req.Stdin, Stdout: req.Stdout, Stderr: req.Stderr})
	if err != nil {
		return fail(err)
	}
	defer execution.Cleanup()
	result := Result{}
	var output []byte
	var readErr error
	if !interactive {
		// A descendant that escaped the process group can hold the pipe open;
		// closing it on cancellation unblocks the read.
		stop := context.AfterFunc(ctx, func() { _ = execution.IO.Close() })
		output, readErr = readOutput(execution.IO, rt, req.OnStreamEvent)
		stop()
		_ = execution.IO.Close()
	}
	waitErr := execution.Wait()
	if waitErr != nil {
		result.ExitCode = 1
		var ee *exec.ExitError
		if errors.As(waitErr, &ee) && ee.ExitCode() > 0 {
			result.ExitCode = ee.ExitCode()
		}
	}
	if interactive {
		return result, waitErr
	}
	result.RawOutput = strings.TrimSpace(string(output))
	parsed, parseErr := rt.ParseOutput(output)
	result.Content = parsed.Content
	result.SessionID = parsed.SessionID
	result.ToolCalls = parsed.ToolCalls
	if result.Content == "" {
		result.Content = strings.TrimSpace(adapter.NormalizeToolOutput(string(output)))
	}
	if waitErr != nil || readErr != nil || parseErr != nil || parsed.IsError {
		if result.ExitCode == 0 {
			result.ExitCode = 1
		}
		err = errors.Join(waitErr, readErr, parseErr)
		if err == nil {
			err = fmt.Errorf("agent returned an error")
		}
		return result, err
	}
	if req.JSONSchema != "" {
		if cmd.OutputFile != "" {
			data, err := os.ReadFile(cmd.OutputFile)
			if err != nil {
				return fail(err)
			}
			result.Content = strings.TrimSpace(string(data))
		}
		if !json.Valid([]byte(result.Content)) {
			return fail(fmt.Errorf("agent response is not valid JSON"))
		}
	}
	return result, nil
}

func readOutput(r io.Reader, rt adapter.AgentRuntime, onEvent func(adapter.StreamEvent)) ([]byte, error) {
	if onEvent == nil {
		return io.ReadAll(r)
	}
	var buf bytes.Buffer
	scanner := bufio.NewScanner(io.TeeReader(r, &buf))
	scanner.Buffer(make([]byte, 65536), 1<<20)
	for scanner.Scan() {
		for _, ev := range rt.ParseStreamLine(scanner.Bytes()) {
			onEvent(ev)
		}
	}
	err := scanner.Err()
	if err != nil {
		_, copyErr := io.Copy(&buf, r)
		if errors.Is(err, bufio.ErrTooLong) {
			err = copyErr
		} else {
			err = errors.Join(err, copyErr)
		}
	}
	return buf.Bytes(), err
}

// guardArgs prevents caller-supplied values from being parsed as agent CLI flags.
// Identifier-like values (model, session, tools, files, images, docker socket) must not start
// with "-". Free-text prompts are prefixed with a space instead, which does not
// change their meaning to the model.
func guardArgs(req *Request) error {
	check := func(kind string, values ...string) error {
		for _, v := range values {
			if strings.HasPrefix(v, "-") {
				return fmt.Errorf("invalid %s %q: must not start with '-'", kind, v)
			}
		}
		return nil
	}
	for _, c := range []struct {
		kind   string
		values []string
	}{
		{"model", []string{req.Model}},
		{"session", []string{req.SessionID}},
		{"tool", req.Tools},
		{"file", req.Files},
		{"image", req.Images},
		{"docker socket", []string{req.DockerSocket}},
	} {
		if err := check(c.kind, c.values...); err != nil {
			return err
		}
	}
	if strings.HasPrefix(req.Prompt, "-") {
		req.Prompt = " " + req.Prompt
	}
	if strings.HasPrefix(req.SystemPrompt, "-") {
		req.SystemPrompt = " " + req.SystemPrompt
	}
	return nil
}
