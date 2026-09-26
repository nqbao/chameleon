package environment

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"syscall"

	"github.com/creack/pty"
)

// HostEnvironment runs commands natively on the host.
// When sandboxSetup is non-nil, every command is wrapped in sandbox-exec
// (Name() returns "sandbox" in that case). When session is non-nil, persistence
// is delegated to that session manager (e.g. TmuxSession).
//
// ResourceID semantics:
//   - session != nil: resourceID is the session manager's handle (e.g. tmux session name)
//   - session == nil: resourceID is the process PID as a string; not reattachable but
//     supports Alive (kill -0) and Cleanup (kill).
type HostEnvironment struct {
	baseEnv      []string
	sensitiveEnv []string
	name         string
	workspace    Workspace
	session      TermSessionManager
	sandboxSetup *SandboxExecSetup // nil = plain host, non-nil = wrap in sandbox-exec
	hostEnv      []string          // extra KEY=value pairs from config (host.env), applied before secrets
	secretEnv    []string          // pre-resolved NAME=value pairs from workspace secrets
}

// sensitiveHostVars are stripped from the inherited environment before any subprocess
// inherits it, preventing chameleon credentials from leaking into agents and terminals.
var sensitiveHostVars = []string{"CHAMELEON_TOKEN", "CHAMELEON_SECRET_PASSPHRASE"}

func NewHostEnvironment(spec EnvironmentSpec) *HostEnvironment {
	return &HostEnvironment{baseEnv: spec.BaseEnv, sensitiveEnv: append(append([]string{}, sensitiveHostVars...), spec.SensitiveEnv...), name: "host", workspace: spec.Workspace, session: spec.Session, hostEnv: spec.HostEnv, secretEnv: spec.SecretEnv}
}

func NewSandboxEnvironment(spec EnvironmentSpec) *HostEnvironment {
	sandboxSetup := spec.SandboxSetup
	if sandboxSetup == nil {
		sandboxSetup = &SandboxExecSetup{}
	}
	return &HostEnvironment{
		name:         "sandbox",
		baseEnv:      spec.BaseEnv,
		sensitiveEnv: append(append([]string{}, sensitiveHostVars...), spec.SensitiveEnv...),
		workspace:    spec.Workspace,
		session:      spec.Session,
		sandboxSetup: sandboxSetup,
		hostEnv:      spec.HostEnv,
		secretEnv:    spec.SecretEnv,
	}
}

func (h *HostEnvironment) Name() string { return h.name }

// Launch runs the spec with pipe I/O (no PTY). Used by chat and CLI agent runs.
func (h *HostEnvironment) Launch(ctx context.Context, spec LaunchSpec) (*Execution, error) {
	if len(spec.Argv) == 0 {
		return nil, fmt.Errorf("%s environment: command must not be empty", h.name)
	}
	execArgv := spec.Argv
	if h.sandboxSetup != nil {
		sandboxArgs := h.sandboxSetup.Args()
		execArgv = append(append([]string{"sandbox-exec"}, sandboxArgs...), "--", "/bin/sh", "-c", shellJoin(spec.Argv))
	}
	// Strip sensitive vars, apply docker.env, then inject secrets so secrets win.
	allUnset := make([]string, 0, len(h.sensitiveEnv)+len(spec.UnsetEnv))
	allUnset = append(allUnset, h.sensitiveEnv...)
	allUnset = append(allUnset, spec.UnsetEnv...)
	base := applyEnvOverrides(append([]string{}, h.baseEnv...), spec.Env, allUnset)
	base = applyEnvOverrides(base, h.hostEnv, nil)
	env := applyEnvOverrides(base, h.secretEnv, nil)
	cmd := commandContext(ctx, env, execArgv[0], execArgv[1:]...)
	cmd.Dir = h.workspace.Path
	if spec.Detach {
		cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	}
	if !spec.Interactive {
		// Own the process tree so cancellation also stops descendants. Interactive
		// commands keep the caller's foreground process group and terminal.
		ownProcessGroup(cmd)
	}

	if spec.Interactive {
		cmd.Stdin = spec.Stdin
		cmd.Stdout = spec.Stdout
		cmd.Stderr = spec.Stderr
		if err := cmd.Start(); err != nil {
			return nil, err
		}
		return &Execution{
			Resize:     func(cols, rows uint16) error { return nil },
			Kill:       func() error { return cmdKill(cmd) },
			Wait:       func() error { return cmd.Wait() },
			ResourceID: fmt.Sprint(cmd.Process.Pid),
			Cleanup:    func() {},
		}, nil
	}

	pr, pw, err := os.Pipe()
	if err != nil {
		return nil, err
	}
	cmd.Stdout = pw
	var prstderr *os.File
	if spec.MergeStderr {
		cmd.Stderr = pw
	} else {
		var pwstderr *os.File
		prstderr, pwstderr, err = os.Pipe()
		if err != nil {
			pr.Close()
			pw.Close()
			return nil, err
		}
		cmd.Stderr = pwstderr
		defer pwstderr.Close()
	}
	if err := cmd.Start(); err != nil {
		pr.Close()
		pw.Close()
		if prstderr != nil {
			prstderr.Close()
		}
		return nil, err
	}
	pw.Close()
	return &Execution{
		IO:         pr,
		Stderr:     prstderr,
		Resize:     func(cols, rows uint16) error { return nil },
		Kill:       func() error { return cmdKill(cmd) },
		Wait:       func() error { return cmd.Wait() },
		ResourceID: fmt.Sprint(cmd.Process.Pid),
		Cleanup:    func() {},
	}, nil
}

// LaunchTerm starts argv under a PTY with the given terminal size. Used by terminal sessions.
func (h *HostEnvironment) LaunchTerm(_ context.Context, spec TermSpec) (*Execution, error) {
	execArgv := spec.Argv
	if h.sandboxSetup != nil {
		if len(spec.Argv) == 0 {
			return nil, fmt.Errorf("%s environment: shell command must not be empty", h.name)
		}
		sandboxArgs := h.sandboxSetup.Args()
		execArgv = append(append([]string{"sandbox-exec"}, sandboxArgs...), "--", "/bin/sh", "-c", shellJoin(spec.Argv))
	}

	// For session-backed (tmux) terminals, cmd.Env set in startPTY only affects
	// the attach process, not the shell inside the window. Inject config env and
	// secrets and strip sensitive vars by prefixing argv with env(1) before
	// handing to Wrap. hostEnv comes first so secrets override on conflict.
	if h.session != nil {
		envArgs := make([]string, 0, 2+2*len(h.sensitiveEnv)+1+len(h.hostEnv)+len(h.secretEnv))
		envArgs = append(envArgs, "env")
		for _, v := range h.sensitiveEnv {
			envArgs = append(envArgs, "-u", v)
		}
		envArgs = append(envArgs, "--")
		envArgs = append(envArgs, h.hostEnv...)
		envArgs = append(envArgs, h.secretEnv...)
		execArgv = append(envArgs, execArgv...)
	}

	resourceID := ""
	if h.session != nil {
		var err error
		execArgv, resourceID, err = h.session.Wrap(spec.ID, h.workspace, execArgv, spec.Cols, spec.Rows)
		if err != nil {
			return nil, err
		}
	}

	errCleanup := func() {}
	if h.session != nil && resourceID != "" {
		errCleanup = func() { h.session.Cleanup(resourceID) }
	}

	exec_, err := h.startPTY(execArgv, spec.Cols, spec.Rows, resourceID, errCleanup)
	if err != nil {
		return nil, err
	}
	exec_.Cleanup = func() {}
	return exec_, nil
}

// Alive reports whether the process or session is still running.
// For session-backed environments, delegates to the session manager.
// For plain environments, uses kill -0 on the stored PID.
func (h *HostEnvironment) Alive(resourceID string) bool {
	if resourceID == "" {
		return false
	}
	if h.session != nil {
		return h.session.Alive(resourceID)
	}
	pid, err := strconv.Atoi(resourceID)
	if err != nil {
		return false
	}
	proc, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	return proc.Signal(syscall.Signal(0)) == nil
}

// Cleanup kills the process or destroys the session.
func (h *HostEnvironment) Cleanup(resourceID string) {
	if resourceID == "" {
		return
	}
	if h.session != nil {
		h.session.Cleanup(resourceID)
		return
	}
	pid, err := strconv.Atoi(resourceID)
	if err != nil {
		return
	}
	if proc, err := os.FindProcess(pid); err == nil {
		_ = proc.Kill()
	}
}

// Reattach is only supported for session-backed environments (tmux).
// Plain PTY sessions are not reattachable.
func (h *HostEnvironment) Reattach(resourceID string, cols, rows uint16) (*Execution, error) {
	if h.session == nil {
		return nil, fmt.Errorf("%s environment: plain PTY sessions are not reattachable", h.name)
	}
	attachArgv, err := h.session.Reattach(resourceID)
	if err != nil {
		return nil, err
	}
	return h.startPTY(attachArgv, cols, rows, resourceID, func() {})
}

// ResizeWindow sends a tmux resize-window command for TmuxSession-backed sessions.
func (h *HostEnvironment) ResizeWindow(resourceID string, cols, rows uint16) error {
	if ts, ok := h.session.(*TmuxSession); ok {
		return ts.command("-S", ts.Socket, "resize-window", "-t", resourceID,
			"-x", fmt.Sprint(cols), "-y", fmt.Sprint(rows)).Run()
	}
	return nil
}

// startPTY starts cmd under a PTY. When resourceID is empty (plain session),
// the process PID is used as the resource ID.
func (h *HostEnvironment) startPTY(argv []string, cols, rows uint16, resourceID string, cleanup func()) (*Execution, error) {
	cmd := exec.Command(argv[0], argv[1:]...)
	cmd.Dir = h.workspace.Path
	// Strip sensitive vars from the PTY env; apply config env then secrets.
	// (For session-backed terminals hostEnv is also injected via env(1) so it
	// reaches the window; here it covers the plain non-session PTY case.)
	base := applyEnvOverrides(append([]string{}, h.baseEnv...), nil, h.sensitiveEnv)
	base = applyEnvOverrides(base, h.hostEnv, nil)
	base = applyEnvOverrides(base, h.secretEnv, nil)
	cmd.Env = append(base, termEnv...)

	f, err := pty.StartWithSize(cmd, &pty.Winsize{Rows: rows, Cols: cols})
	if err != nil {
		cleanup()
		return nil, err
	}
	if resourceID == "" {
		resourceID = fmt.Sprint(cmd.Process.Pid)
	}
	return &Execution{
		IO:         f,
		Resize:     func(cols, rows uint16) error { return pty.Setsize(f, &pty.Winsize{Rows: rows, Cols: cols}) },
		Kill:       func() error { return cmdKill(cmd) },
		Wait:       func() error { return cmd.Wait() },
		ResourceID: resourceID,
		Cleanup:    cleanup,
	}, nil
}

// ApplyEnvOverrides builds an env slice from base, stripping any keys in unsetEnv
// or in env (to avoid duplicates), then appending the key=val pairs in env.
func ApplyEnvOverrides(base, env, unsetEnv []string) []string {
	return applyEnvOverrides(base, env, unsetEnv)
}

// applyEnvOverrides is the unexported implementation.
func applyEnvOverrides(base, env, unsetEnv []string) []string {
	if len(env) == 0 && len(unsetEnv) == 0 {
		return base
	}
	skip := make(map[string]bool, len(unsetEnv)+len(env))
	for _, k := range unsetEnv {
		skip[k] = true
	}
	for _, e := range env {
		name, _, _ := strings.Cut(e, "=")
		skip[name] = true
	}
	result := make([]string, 0, len(base)+len(env))
	for _, e := range base {
		name, _, _ := strings.Cut(e, "=")
		if !skip[name] {
			result = append(result, e)
		}
	}
	return append(result, env...)
}

// shellJoin joins command parts into a single string suitable for /bin/sh -c.
func shellJoin(parts []string) string {
	quoted := make([]string, len(parts))
	for i, p := range parts {
		quoted[i] = "'" + strings.ReplaceAll(p, "'", "'\\''") + "'"
	}
	return strings.Join(quoted, " ")
}
