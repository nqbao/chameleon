package environment

import (
	"context"
	"io"
)

// Workspace is a slim representation of a workspace used by the environment
// package. It is distinct from store.Workspace (which includes Sessions).
type Workspace struct {
	ID   string
	Name string
	Path string
}

// LaunchSpec describes a non-PTY command invocation.
type LaunchSpec struct {
	Stdin          io.Reader
	Stdout, Stderr io.Writer
	ID             string // session identifier; used as container name for durable docker runs
	Argv           []string
	Env            []string // extra KEY=VAL pairs appended to the inherited environment
	UnsetEnv       []string // keys to strip from the inherited environment before Env is applied
	Detach         bool     // put the child in its own session (Setsid); set true for server-side runs
	Interactive    bool     // inherit parent stdio instead of piping; Execution.IO will be nil
	MergeStderr    bool     // route stderr into IO (same pipe as stdout); when false Execution.Stderr is a separate reader
	ExtraVolumes   []string // docker only: additional "-v src:dst" mount specs
}

// TermSpec describes a PTY-backed command invocation.
type TermSpec struct {
	ID       string // session identifier; used as container / tmux window name
	Argv     []string
	Env      []string
	UnsetEnv []string
	Cols     uint16
	Rows     uint16
}

// Execution is a live handle to a running process or attached session.
// Returned by Environment.Launch and TermEnvironment.LaunchTerm/Reattach.
type Execution struct {
	IO         io.ReadWriteCloser // PTY file (terminal) or stdout pipe (piped); nil when Interactive
	Stderr     io.ReadCloser      // separate stderr pipe; non-nil only when MergeStderr is false
	Resize     func(cols, rows uint16) error
	Kill       func() error
	Wait       func() error
	ResourceID string // persistent handle: tmux session name, container name, or PID string
	Cleanup    func() // remove temp files etc.; called once on final teardown
}

// Environment is a place where commands can run.
// Launch runs the spec with pipe I/O — no PTY, suitable for chat and CLI agent runs.
type Environment interface {
	Name() string
	Launch(ctx context.Context, spec LaunchSpec) (*Execution, error)
	Alive(resourceID string) bool
	Cleanup(resourceID string)
}

// TermEnvironment extends Environment with PTY terminal support.
// LaunchTerm starts argv under a PTY with the given terminal size.
// Reattach reconnects to a persistent resource (tmux session, docker container).
// ResizeWindow propagates a resize to the persistent resource.
type TermEnvironment interface {
	Environment
	LaunchTerm(ctx context.Context, spec TermSpec) (*Execution, error)
	Reattach(resourceID string, cols, rows uint16) (*Execution, error)
	ResizeWindow(resourceID string, cols, rows uint16) error
}

// EnvironmentSpec holds the configuration for constructing any Environment.
// Only the fields relevant to the chosen environment type need to be set.
type EnvironmentSpec struct {
	BaseEnv       []string // inherited subprocess environment, supplied by caller
	SensitiveEnv  []string // additional names to strip from host subprocesses
	DockerHome    string   // credential mount root
	Workspace     Workspace
	Session       TermSessionManager   // host/sandbox: persistent session manager (tmux, etc.)
	Providers     []CredentialProvider // credentials to mount (docker) or ignore (host)
	Image         string               // docker: container image
	DockerSocket  string               // docker: socket path override
	InnerTmux     bool                 // docker: run tmux as container's main process
	TmuxConfig    string               // docker: host path to tmux.conf to mount
	DockerRunArgs []string             // docker: extra docker run args (from config)
	DockerEnvArgs []string             // docker: pre-formatted -e flags (from config)
	ExtraInit     string               // docker: extra init script fragment (from config)
	ExtraVolumes  []string             // docker: additional "-v src:dst" mount specs
	SandboxSetup  *SandboxExecSetup    // nil = plain host, non-nil = wrap in sandbox-exec
	HostEnv       []string             // host/sandbox: extra KEY=value pairs from config (host.env)
	SecretEnv     []string             // pre-resolved NAME=value pairs from workspace secrets
}

// TermSessionManager wraps a command for persistent session management.
// PlainSession and TmuxSession are the two built-in implementations.
type TermSessionManager interface {
	// Wrap creates a new session identified by id, running argv as its program.
	// Returns execArgv (the command to pass to Environment.Launch) and the
	// resource ID to use for subsequent Alive/Reattach/Cleanup calls.
	// For PlainSession, execArgv == argv and resourceID == "".
	// For TmuxSession, a detached tmux window is spawned; execArgv is the
	// attach command and resourceID is the tmux session name.
	Wrap(id string, workspace Workspace, argv []string, cols, rows uint16) (execArgv []string, resourceID string, err error)
	// Alive reports whether the resource is still running.
	Alive(resourceID string) bool
	// Reattach returns the argv that reconnects to an existing resource.
	Reattach(resourceID string) ([]string, error)
	// Cleanup destroys the session resource.
	Cleanup(resourceID string)
}
