package environment

import (
	"context"
	"fmt"
	"os/exec"
)

// PlainSession is a no-op session manager: it passes argv through unchanged
// and provides no persistent resource. Reattach is not supported.
type PlainSession struct{}

func (p *PlainSession) Wrap(_ string, _ Workspace, argv []string, _, _ uint16) ([]string, string, error) {
	return argv, "", nil
}

func (p *PlainSession) Alive(_ string) bool { return false }

func (p *PlainSession) Reattach(_ string) ([]string, error) {
	return nil, fmt.Errorf("plain session does not support reattach")
}

func (p *PlainSession) Cleanup(_ string) {}

// TmuxSession manages persistent terminal sessions via tmux.
// Wrap spawns a detached tmux window and returns the attach command.
// Alive/Reattach/Cleanup operate on the named tmux session.
type TmuxSession struct {
	BaseEnv []string
	Prefix  string // default cham-
	Socket  string
	Config  string
}

func (t *TmuxSession) Wrap(id string, workspace Workspace, argv []string, cols, rows uint16) ([]string, string, error) {
	prefix := t.Prefix
	if prefix == "" {
		prefix = "cham-"
	}
	tmuxName := prefix + id
	newArgs := []string{
		"-S", t.Socket, "-f", t.Config,
		"new-session", "-d", "-s", tmuxName,
		"-x", fmt.Sprint(cols), "-y", fmt.Sprint(rows),
		"-c", workspace.Path,
	}
	newArgs = append(newArgs, argv...)
	if out, err := t.command(newArgs...).CombinedOutput(); err != nil {
		return nil, "", fmt.Errorf("tmux new-session: %w: %s", err, out)
	}
	attachArgv := []string{"tmux", "-S", t.Socket, "attach-session", "-t", tmuxName}
	return attachArgv, tmuxName, nil
}

func (t *TmuxSession) Alive(resourceID string) bool {
	return t.command("-S", t.Socket, "has-session", "-t", resourceID).Run() == nil
}

func (t *TmuxSession) Reattach(resourceID string) ([]string, error) {
	return []string{"tmux", "-S", t.Socket, "attach-session", "-t", resourceID}, nil
}

func (t *TmuxSession) Cleanup(resourceID string) {
	_ = t.command("-S", t.Socket, "kill-session", "-t", resourceID).Run()
}

func (t *TmuxSession) command(args ...string) *exec.Cmd {
	return commandContext(context.Background(), append([]string{}, t.BaseEnv...), "tmux", args...)
}
