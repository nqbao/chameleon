package chameleon

import (
	"context"
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/nqbao/chameleon/adapter"
	"github.com/nqbao/chameleon/environment"
	"github.com/nqbao/chameleon/secrets"
)

// Re-export SandboxExecSetup from the environment package.
type SandboxExecSetup = environment.SandboxExecSetup

// SandboxCommand wraps an AgentCommand with sandbox-exec, returning a ready-to-run *exec.Cmd.
// This replaces the old SandboxExecSetup.Command() method which would have created a
// circular dependency between environment and adapter packages.
func SandboxCommand(setup *environment.SandboxExecSetup, agentCmd adapter.AgentCommand) *exec.Cmd {
	return SandboxCommandContext(context.Background(), setup, agentCmd)
}

// SandboxCommandContext wraps an AgentCommand with sandbox-exec using a context.
func SandboxCommandContext(ctx context.Context, setup *environment.SandboxExecSetup, agentCmd adapter.AgentCommand) *exec.Cmd {
	sandboxArgs := setup.Args()
	shCmd := shellJoin(append([]string{agentCmd.Name}, agentCmd.Args...))
	allArgs := append(sandboxArgs, "--", "/bin/sh", "-c", shCmd)
	return exec.CommandContext(ctx, "sandbox-exec", allArgs...)
}

// sandboxWritablePaths returns writable paths and deny-within paths for a workspace.
func sandboxWritablePaths(workspacePath, userHome, tempDir string) (writable, denyWithin []string) {
	resolved, err := filepath.EvalSymlinks(workspacePath)
	if err != nil {
		resolved = workspacePath
	}
	writable = append(writable, resolved)

	tmpDir := tempDir
	if tmpDir == "" {
		tmpDir = "/tmp"
	}
	if resolvedTmp, err := filepath.EvalSymlinks(tmpDir); err == nil {
		tmpDir = resolvedTmp
	}
	writable = append(writable, tmpDir)

	homeDir := userHome
	if homeDir != "" {
		for _, d := range []string{
			".pi",                   // pi
			".config/opencode",      // opencode
			".local/share/opencode", // opencode
			".pyenv/shims",          // pyenv rehash writes shims during package installs
		} {
			p := filepath.Join(homeDir, d)
			if rp, err := filepath.EvalSymlinks(p); err == nil {
				writable = append(writable, rp)
			} else {
				writable = append(writable, p) // dir may not exist yet on first run
			}
		}
	}

	denyWithin = append(denyWithin, filepath.Join(resolved, ".git", "hooks"))
	return writable, denyWithin
}

// normalizePath expands a leading ~ and resolves symlinks so the path matches
// what the sandbox kernel sees. Falls back to the tilde-expanded form on error.
func (o Options) normalizePath(p string) string {
	if p == "~" || strings.HasPrefix(p, "~/") {
		home := o.UserHome
		if home == "" {
			return p
		}
		p = filepath.Join(home, p[1:])
	}
	if resolved, err := filepath.EvalSymlinks(p); err == nil {
		return resolved
	}
	return p
}

// resolvePaths expands tildes and resolves symlinks on each path, keeping the original on error.
func (o Options) resolvePaths(paths []string) []string {
	resolved := make([]string, 0, len(paths))
	for _, p := range paths {
		resolved = append(resolved, o.normalizePath(p))
	}
	return resolved
}

// shellJoin joins command parts into a single string suitable for /bin/sh -c.
func shellJoin(parts []string) string {
	quoted := make([]string, len(parts))
	for i, p := range parts {
		quoted[i] = "'" + strings.ReplaceAll(p, "'", "'\\''") + "'"
	}
	return strings.Join(quoted, " ")
}

// SandboxSpec constructs an EnvironmentSpec for sandbox environments,
// computing writable paths and sandbox settings.
func (o Options) SandboxSpec(ws Workspace, noNetwork bool) (environment.EnvironmentSpec, error) {
	writable, denyWithin := sandboxWritablePaths(ws.Path, o.UserHome, o.TempDir)
	secretEnv, err := o.resolveWorkspaceSecrets(ws.Path)
	if err != nil {
		return environment.EnvironmentSpec{}, err
	}
	var denyReadDirs []string
	if o.Home != "" {
		denyReadDirs = append(denyReadDirs, o.normalizePath(o.Home))
	}
	return environment.EnvironmentSpec{
		BaseEnv: o.BaseEnv, SensitiveEnv: o.SensitiveEnv, DockerHome: o.DockerHome,
		Workspace: environment.Workspace{ID: ws.ID, Name: ws.Name, Path: ws.Path},
		SandboxSetup: &environment.SandboxExecSetup{
			WritablePaths: writable,
			DenyWithin:    denyWithin,
			DenyReadDirs:  denyReadDirs,
			AllowNetwork:  !noNetwork,
		},
		SecretEnv: secretEnv,
	}, nil
}

// DockerSpec constructs an EnvironmentSpec for docker environments,
// for the given image and credential providers.
func (o Options) DockerSpec(ws Workspace, d DockerOpts) (environment.EnvironmentSpec, error) {
	secretEnv, err := o.resolveWorkspaceSecrets(ws.Path)
	if err != nil {
		return environment.EnvironmentSpec{}, err
	}
	return environment.EnvironmentSpec{
		BaseEnv: o.BaseEnv, SensitiveEnv: o.SensitiveEnv, DockerHome: o.DockerHome,
		Workspace:     environment.Workspace{ID: ws.ID, Name: ws.Name, Path: ws.Path},
		Image:         d.Image,
		DockerSocket:  d.Socket,
		Providers:     d.Providers,
		DockerRunArgs: d.RunArgs,
		InnerTmux:     d.InnerTmux,
		TmuxConfig:    d.TmuxConfig,
		ExtraVolumes:  d.ExtraVolumes,
		SecretEnv:     secretEnv,
	}, nil
}

// HostSpec constructs an EnvironmentSpec for host environments.
func (o Options) HostSpec(ws Workspace, session environment.TermSessionManager) (environment.EnvironmentSpec, error) {
	secretEnv, err := o.resolveWorkspaceSecrets(ws.Path)
	if err != nil {
		return environment.EnvironmentSpec{}, err
	}
	return environment.EnvironmentSpec{
		BaseEnv: o.BaseEnv, SensitiveEnv: o.SensitiveEnv, DockerHome: o.DockerHome,
		Workspace: environment.Workspace{ID: ws.ID, Name: ws.Name, Path: ws.Path},
		Session:   session,
		SecretEnv: secretEnv,
	}, nil
}

// resolveWorkspaceSecrets resolves declared secrets. Missing values fail closed.
func (o Options) resolveWorkspaceSecrets(workspacePath string) ([]string, error) {
	ws, err := secrets.LoadWorkspaceSecrets(workspacePath, o.WorkspaceDir)
	if err != nil {
		return nil, err
	}
	if len(ws.Secrets) == 0 {
		return nil, nil
	}
	if o.Home == "" {
		return nil, fmt.Errorf("home is required for workspace secrets")
	}
	backend := &secrets.LocalBackend{Dir: filepath.Join(o.Home, "secrets"), KeyFile: filepath.Join(o.Home, "key"), Passphrase: o.Passphrase}
	return secrets.Resolve(backend, ws.Secrets)
}
