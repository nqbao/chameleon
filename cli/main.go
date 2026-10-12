// Package cli exposes the cham command without exiting the caller's process.
package cli

import (
	"context"
	"encoding/json"
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
		fmt.Fprintln(streams.Stdout, "usage: cham run --agent <runtime|persona.md> [-p prompt] [--stream] [--docker image|--sandbox]")
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
		fmt.Fprintln(streams.Stdout, "usage: cham run --agent <runtime|persona.md> [-p prompt] [task.md key=value ...]\nflags: --runtime --model --session --yolo --dir --docker --docker-socket --docker-args\n       --sandbox --sandbox-no-network --setup --shell --schema --tools --env --env-file --stream --events-jsonl --image")
		return 0
	}
	if err != nil {
		fmt.Fprintln(streams.Stderr, err)
		return 2
	}
	var jw *jsonlWriter
	if opts.eventsJSONL {
		jw = &jsonlWriter{enc: json.NewEncoder(streams.Stdout)}
	}
	// fail reports an error before the agent runs; with --events-jsonl it still
	// ends the stream with the terminal error event.
	fail := func(code int, err error) int {
		if jw != nil {
			return jw.finish(chameleon.Result{ExitCode: code}, err, streams.Stderr)
		}
		fmt.Fprintln(streams.Stderr, err)
		return code
	}
	if opts.prompt == "-" {
		data, err := io.ReadAll(streams.Stdin)
		if err != nil {
			return fail(1, fmt.Errorf("reading prompt from stdin: %w", err))
		}
		opts.prompt = strings.TrimRight(string(data), "\r\n")
		if strings.TrimSpace(opts.prompt) == "" {
			return fail(2, errors.New("-p -: stdin is empty"))
		}
	}
	if opts.sandboxMode && opts.dockerImage != "" {
		return fail(2, errors.New("--sandbox and --docker cannot be used together"))
	}
	options, err := OptionsFromEnv()
	if err != nil {
		return fail(1, err)
	}
	if opts.dir == "" {
		opts.dir, err = os.Getwd()
		if err != nil {
			return fail(1, err)
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
			return fail(1, err)
		}
	}
	envVars, err := resolveEnvVars(options.Home, opts.envFiles, opts.envFlags, os.LookupEnv)
	if err != nil {
		return fail(1, err)
	}
	var st streamPrinter
	var onStream func(adapter.StreamEvent)
	if jw != nil {
		onStream = jw.handle
	} else if opts.stream {
		st = streamPrinter{stdout: streams.Stdout, stderr: streams.Stderr}
		onStream = st.handle
	}
	result, err := options.Run(ctx, chameleon.Request{Runtime: opts.runtime, Model: opts.model, Prompt: opts.prompt, SystemPrompt: opts.systemPrompt, SessionID: opts.sessionID, Dir: opts.dir, Env: kind, Image: opts.dockerImage, DockerSocket: opts.dockerSocket, DockerArgs: opts.dockerArgs, NoNetwork: opts.sandboxNoNetwork, Yolo: opts.yolo, Interactive: opts.prompt == "", Setup: opts.setup, Shell: opts.shell, JSONSchema: opts.jsonSchema, Tools: opts.tools, Images: opts.images, EnvVars: envVars, ExtraArgs: opts.extraArgs, MCPConfig: opts.mcpConfig, Stdin: streams.Stdin, Stdout: streams.Stdout, Stderr: streams.Stderr, OnStreamEvent: onStream})
	st.finish()
	if jw != nil {
		return jw.finish(result, err, streams.Stderr)
	}
	if err != nil {
		if !st.text.covers(result.Content) {
			fmt.Fprintln(streams.Stderr, result.Content)
		}
		fmt.Fprintln(streams.Stderr, err)
		if result.ExitCode > 0 {
			return result.ExitCode
		}
		return 1
	}
	if !st.tools {
		for _, tc := range adapter.ResolveDetails(result.ToolCalls) {
			fmt.Fprintf(streams.Stderr, "  %s: %s\n", tc.Name, tc.Detail)
		}
	}
	if result.Content != "" && !st.chunks {
		fmt.Fprintln(streams.Stdout, result.Content)
	}
	if result.SessionID != "" {
		fmt.Fprintf(streams.Stderr, "session-id: %s\n", result.SessionID)
	}
	return 0
}

// streamPrinter renders live stream events: response text to stdout, tool calls
// to stderr. chunks and tools record what was already shown so the final
// summary does not repeat it.
type streamPrinter struct {
	stdout, stderr io.Writer
	chunks, tools  bool
	midLine        bool
	text           streamedText
}

func (p *streamPrinter) handle(ev adapter.StreamEvent) {
	switch ev.Type {
	case "chunk":
		if ev.Content == "" {
			return
		}
		p.chunks = true
		p.text.add(ev.Content)
		fmt.Fprint(p.stdout, ev.Content)
		p.midLine = !strings.HasSuffix(ev.Content, "\n")
	case "message":
		// A complete message ended; the next one starts on its own line.
		p.text.end()
		p.finish()
	case "toolCall":
		p.finish()
		p.tools = true
		if ev.Detail != "" {
			fmt.Fprintf(p.stderr, "  %s: %s\n", ev.Name, ev.Detail)
		} else {
			fmt.Fprintf(p.stderr, "  %s\n", ev.Name)
		}
	}
}

// finish ends a partially written line so later output starts cleanly.
func (p *streamPrinter) finish() {
	if p.midLine {
		fmt.Fprintln(p.stdout)
		p.midLine = false
	}
}

// jsonlEvent is the --events-jsonl wire format. Fields are only ever added.
type jsonlEvent struct {
	Type      string         `json:"type"` // text, thinking, tool, done, error
	Content   string         `json:"content,omitempty"`
	Name      string         `json:"name,omitempty"`
	Detail    string         `json:"detail,omitempty"`
	SessionID string         `json:"session_id,omitempty"`
	Message   string         `json:"message,omitempty"`
	ExitCode  int            `json:"exit_code,omitempty"`
	Usage     *adapter.Usage `json:"usage,omitempty"`
}

// jsonlWriter emits one JSON object per line on stdout and nothing else.
// text records that response text was streamed; ended that the previous
// message completed, so the next text starts with a newline. The first write
// error is kept and fails the run.
type jsonlWriter struct {
	enc        *json.Encoder
	text       bool
	streamed   streamedText
	writeError error
}

func (w *jsonlWriter) emit(ev jsonlEvent) {
	if err := w.enc.Encode(ev); err != nil && w.writeError == nil {
		w.writeError = err
	}
}

func (w *jsonlWriter) handle(ev adapter.StreamEvent) {
	switch ev.Type {
	case "chunk":
		if ev.Content == "" {
			return
		}
		w.text = true
		w.emit(jsonlEvent{Type: "text", Content: w.streamed.add(ev.Content)})
	case "message":
		w.streamed.end()
	case "thinking":
		w.emit(jsonlEvent{Type: "thinking", Content: ev.Content})
	case "toolCall":
		w.emit(jsonlEvent{Type: "tool", Name: ev.Name, Detail: ev.Detail})
	}
}

// finish writes the terminal done or error event and returns the exit status.
// A failed stdout write turns an otherwise successful run into a failure.
func (w *jsonlWriter) finish(result chameleon.Result, err error, stderr io.Writer) int {
	if err != nil {
		code := result.ExitCode
		if code <= 0 {
			code = 1
		}
		fmt.Fprintln(stderr, err)
		ev := jsonlEvent{Type: "error", Message: err.Error(), ExitCode: code}
		if !w.streamed.covers(result.Content) {
			// The agent's own failure text, unless it was already streamed.
			ev.Content = result.Content
			fmt.Fprintln(stderr, result.Content)
		}
		w.emit(ev)
		return code
	}
	if !w.text && result.Content != "" {
		w.emit(jsonlEvent{Type: "text", Content: result.Content})
	}
	w.emit(jsonlEvent{Type: "done", SessionID: result.SessionID, Usage: result.Usage})
	if w.writeError != nil {
		fmt.Fprintf(stderr, "writing events: %v\n", w.writeError)
		return 1
	}
	return 0
}

// streamedText tracks the response text already shown, so a failure
// explanation is reported unless the stream already carried it.
type streamedText struct {
	all, last string
	ended     bool
}

// add records a chunk and returns it, prefixed with a newline when it starts a
// new message after earlier text.
func (s *streamedText) add(chunk string) string {
	if s.ended {
		if s.all != "" {
			chunk = "\n" + chunk
		}
		s.last, s.ended = "", false
	}
	s.all += chunk
	s.last += chunk
	return chunk
}

// end marks the current message complete.
func (s *streamedText) end() { s.ended = true }

// covers reports whether content is empty or equals what was already streamed.
func (s *streamedText) covers(content string) bool {
	c := strings.TrimSpace(content)
	return c == "" || c == strings.TrimSpace(s.all) || c == strings.TrimSpace(s.last)
}
