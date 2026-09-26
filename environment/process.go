package environment

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"
)

// waitDelay bounds how long Wait blocks on caller-supplied I/O after the process exits or is cancelled.
const waitDelay = 2 * time.Second

// LookPath resolves name against the PATH in env. When env carries no PATH
// entry, the parent process PATH is used. Names containing a path separator are
// returned unchanged.
func LookPath(name string, env []string) (string, error) {
	if strings.ContainsRune(name, '/') {
		return name, nil
	}
	path := ""
	found := false
	for _, e := range env {
		if v, ok := strings.CutPrefix(e, "PATH="); ok {
			path, found = v, true
		}
	}
	if !found {
		return exec.LookPath(name)
	}
	for _, dir := range filepath.SplitList(path) {
		if dir == "" {
			dir = "."
		}
		p := filepath.Join(dir, name)
		if info, err := os.Stat(p); err == nil && !info.IsDir() && info.Mode()&0o111 != 0 {
			return p, nil
		}
	}
	return "", &exec.Error{Name: name, Err: exec.ErrNotFound}
}

// commandContext is exec.CommandContext with the executable resolved against env
// rather than the parent PATH. The returned command has Env set to env.
func commandContext(ctx context.Context, env []string, name string, args ...string) *exec.Cmd {
	resolved, err := LookPath(name, env)
	if err != nil {
		resolved = name
	}
	cmd := exec.CommandContext(ctx, resolved, args...)
	cmd.Args[0] = name
	cmd.Env = env
	cmd.WaitDelay = waitDelay
	return cmd
}

// ownProcessGroup places cmd in its own process group (or session, when already
// detached) and makes context cancellation kill the whole group.
func ownProcessGroup(cmd *exec.Cmd) {
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	if !cmd.SysProcAttr.Setsid {
		cmd.SysProcAttr.Setpgid = true
	}
	cmd.Cancel = func() error { return cmdKill(cmd) }
}

// cmdKill kills a command process, and its whole process group when it owns one.
func cmdKill(cmd *exec.Cmd) error {
	if cmd.Process == nil {
		return nil
	}
	if a := cmd.SysProcAttr; a != nil && (a.Setpgid || a.Setsid) {
		if err := syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL); err == nil {
			return nil
		}
	}
	return cmd.Process.Kill()
}
