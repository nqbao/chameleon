package environment

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/creack/pty"
)

// ContainerWorkspaceDir returns the path inside a container where the workspace is mounted.
func ContainerWorkspaceDir(workspace Workspace) string {
	return "/workspace/" + filepath.Base(workspace.Path)
}

// containerWorkspaceDir is the unexported alias used internally.
func containerWorkspaceDir(workspace Workspace) string {
	return ContainerWorkspaceDir(workspace)
}

const dockerTmuxConfigPath = "/cham-tmux.conf"

// termEnv is the standard terminal environment appended to all PTY processes.
var termEnv = []string{"TERM=xterm-256color", "COLORTERM=truecolor"}

// hostTimezoneArgs returns docker run flags to pass the host timezone to a container.
func hostTimezoneArgs(base []string) []string {
	if tz := envValue(base, "TZ"); tz != "" {
		return []string{"-e", "TZ=" + tz}
	}
	// Symlink case: /etc/localtime -> /usr/share/zoneinfo/Region/City
	if target, err := os.Readlink("/etc/localtime"); err == nil {
		if !filepath.IsAbs(target) {
			target = filepath.Join("/etc", target)
		}
		for _, prefix := range []string{"/usr/share/zoneinfo/", "/var/db/timezone/zoneinfo/"} {
			if after, ok := strings.CutPrefix(target, prefix); ok {
				return []string{"-e", "TZ=" + after}
			}
		}
	}

	// Regular-file case: /etc/timezone contains the timezone name as plain text.
	if data, err := os.ReadFile("/etc/timezone"); err == nil {
		if tz := strings.TrimSpace(string(data)); tz != "" {
			return []string{"-e", "TZ=" + tz}
		}
	}

	return nil
}

// currentHostTermEnvFlags returns docker -e flags for terminal env vars from the host.
func currentHostTermEnvFlags(base []string) []string {
	var flags []string
	for _, name := range []string{"TERM", "COLORTERM", "LANG", "LC_ALL"} {
		if value := envValue(base, name); value != "" {
			flags = append(flags, "-e", name+"="+value)
		}
	}
	return flags
}

// envFlags converts a list of env strings to "-e", value pairs for docker run.
func envFlags(envs []string) []string {
	args := make([]string, 0, len(envs)*2)
	for _, env := range envs {
		args = append(args, "-e", env)
	}
	return args
}

// DockerEnvironment runs terminal sessions inside Docker containers.
// The container is the persistent resource: it runs detached and survives PTY
// disconnects, so Alive/Reattach always operate on the container name.
//
// When innerTmux is true the container's main process is tmux, which in turn
// runs argv. ResizeWindow routes resize commands through "docker exec".
// When innerTmux is false argv runs directly in the container and ResizeWindow
// is a no-op.
type DockerEnvironment struct {
	extraVolumes  []string
	baseEnv       []string
	dockerHome    string
	workspace     Workspace
	image         string
	dockerSocket  string
	providers     []CredentialProvider
	innerTmux     bool   // docker+tmux: container runs tmux as its main process
	tmuxConfig    string // host path to tmux.conf to mount at /cham-tmux.conf
	dockerRunArgs []string
	dockerEnvArgs []string
	extraInit     string
	secretEnv     []string // pre-resolved NAME=value pairs; injected via --env-file
}

func NewDockerEnvironment(spec EnvironmentSpec) *DockerEnvironment {
	return &DockerEnvironment{
		workspace:     spec.Workspace,
		extraVolumes:  append([]string{}, spec.ExtraVolumes...),
		baseEnv:       append([]string{}, spec.BaseEnv...),
		dockerHome:    spec.DockerHome,
		image:         spec.Image,
		dockerSocket:  spec.DockerSocket,
		providers:     spec.Providers,
		innerTmux:     spec.InnerTmux,
		tmuxConfig:    spec.TmuxConfig,
		dockerRunArgs: spec.DockerRunArgs,
		dockerEnvArgs: spec.DockerEnvArgs,
		extraInit:     spec.ExtraInit,
		secretEnv:     spec.SecretEnv,
	}
}

// prepareSecretEnvFile writes secretEnv to a 0600 temp file and returns
// the --env-file flag args and a cleanup func. Returns nil args if secretEnv is empty.
func (de *DockerEnvironment) prepareSecretEnvFile() (args []string, cleanup func(), err error) {
	if len(de.secretEnv) == 0 {
		return nil, func() {}, nil
	}
	for _, kv := range de.secretEnv {
		if strings.ContainsAny(kv, "\r\n") {
			name, _, _ := strings.Cut(kv, "=")
			return nil, func() {}, fmt.Errorf("secret %q contains a newline; docker env files cannot represent it", name)
		}
	}
	f, err := os.CreateTemp("", "cham-secrets-*.env")
	if err != nil {
		return nil, func() {}, fmt.Errorf("create secret env file: %w", err)
	}
	path := f.Name()
	rmCleanup := func() { os.Remove(path) }
	var writeErr error
	for _, kv := range de.secretEnv {
		if _, werr := fmt.Fprintln(f, kv); werr != nil {
			writeErr = werr
			break
		}
	}
	if closeErr := f.Close(); writeErr == nil {
		writeErr = closeErr
	}
	if writeErr != nil {
		rmCleanup()
		return nil, func() {}, fmt.Errorf("write secret env file: %w", writeErr)
	}
	return []string{"--env-file", path}, rmCleanup, nil
}

func (de *DockerEnvironment) Name() string { return "docker" }

// dockerArgs prepends -H <socket> when a docker socket is configured.
func (de *DockerEnvironment) dockerArgs(args ...string) []string {
	if de.dockerSocket != "" {
		out := make([]string, 0, len(args)+2)
		out = append(out, "-H", de.dockerSocket)
		return append(out, args...)
	}
	return args
}

func (de *DockerEnvironment) containerDir() string {
	return containerWorkspaceDir(de.workspace)
}

// tmuxConfigMount returns the host path of the tmux config file if it exists
// and should be mounted. Returns ("", false) when no valid file is configured.
func (de *DockerEnvironment) tmuxConfigMount() (string, bool) {
	if de.tmuxConfig == "" || de.tmuxConfig == "/dev/null" {
		return "", false
	}
	info, err := os.Stat(de.tmuxConfig)
	if err != nil || info.IsDir() {
		return "", false
	}
	return de.tmuxConfig, true
}

// imageCmd returns the CMD baked into the image, falling back to ["sh"].
func (de *DockerEnvironment) imageCmd(image string) []string {
	out, err := de.command(context.Background(), de.dockerArgs(
		"image", "inspect", "--format", "{{json .Config.Cmd}}", image,
	)...).Output()
	if err == nil {
		var parts []string
		if json.Unmarshal(bytes.TrimSpace(out), &parts) == nil && len(parts) > 0 {
			return parts
		}
	}
	return []string{"sh"}
}

// buildTmuxCmd builds the tmux invocation that runs argv as the window process
// inside the container, matching existing docker+tmux behaviour.
func (de *DockerEnvironment) buildTmuxCmd(id string, argv []string, cols, rows uint16) []string {
	args := []string{"tmux"}
	if _, ok := de.tmuxConfigMount(); ok {
		args = append(args, "-f", dockerTmuxConfigPath)
	}
	tmuxName := "cham-" + id
	args = append(args,
		"new-session", "-A", "-s", tmuxName,
		"-x", fmt.Sprint(cols), "-y", fmt.Sprint(rows),
		"-c", de.containerDir(),
	)
	return append(args, argv...)
}

// prepareInitScript creates the credential init script and returns the docker
// mount/env args to inject, the container entrypoint ("/cham-init.sh" or ""),
// and a cleanup func that removes the temp file.
func (de *DockerEnvironment) prepareInitScript() (credArgs []string, initEntry string, cleanup func(), err error) {
	cleanup = func() {}
	if len(de.providers) == 0 && de.extraInit == "" {
		return nil, "", cleanup, nil
	}
	setup := &DockerContainerSetup{
		DockerHome: de.dockerHome,
		Providers:  de.providers,
		ExtraInit:  de.extraInit,
	}
	if derr := setup.EnsureDirs(); derr != nil {
		return nil, "", cleanup, derr
	}
	f, ferr := os.CreateTemp("", "cham-init-*.sh")
	if ferr != nil {
		return nil, "", func() {}, fmt.Errorf("create init script: %w", ferr)
	}
	_, _ = f.WriteString(setup.InitScript())
	_ = f.Chmod(0755)
	f.Close()
	initPath := f.Name()
	credArgs = setup.MountArgs(initPath)
	return credArgs, "/cham-init.sh", func() { os.Remove(initPath) }, nil
}

// Launch runs the spec inside a one-shot container with pipe I/O (no PTY).
// Used by chat and CLI agent runs. The container is removed when argv exits.
// spec.UnsetEnv is ignored — docker containers do not inherit the host environment.
func (de *DockerEnvironment) Launch(ctx context.Context, spec LaunchSpec) (*Execution, error) {
	credArgs, initEntry, cleanup, err := de.prepareInitScript()
	if err != nil {
		return nil, err
	}
	secretArgs, secretCleanup, err := de.prepareSecretEnvFile()
	if err != nil {
		cleanup()
		return nil, err
	}
	origCleanup := cleanup
	cleanup = func() { origCleanup(); secretCleanup() }

	ttyFlag := "-i"
	if spec.Interactive {
		ttyFlag = "-it"
	}
	runArgs := de.dockerArgs("run", "--rm", ttyFlag,
		"--workdir", de.containerDir(),
		"-v", de.workspace.Path+":"+de.containerDir(),
	)
	runArgs = append(runArgs, credArgs...)
	for _, vol := range append(append([]string{}, de.extraVolumes...), spec.ExtraVolumes...) {
		runArgs = append(runArgs, "-v", vol)
	}
	runArgs = append(runArgs, de.dockerEnvArgs...)
	runArgs = append(runArgs, secretArgs...)
	runArgs = append(runArgs, envFlags(spec.Env)...)
	runArgs = append(runArgs, hostTimezoneArgs(de.baseEnv)...)
	runArgs = append(runArgs, de.dockerRunArgs...)
	runArgs = append(runArgs, de.image)
	if initEntry != "" {
		runArgs = append(runArgs, initEntry)
	}
	runArgs = append(runArgs, spec.Argv...)

	cmd := de.command(ctx, runArgs...)
	if spec.Detach {
		cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	}
	if !spec.Interactive {
		ownProcessGroup(cmd)
	}
	if spec.Interactive {
		cmd.Stdin = spec.Stdin
		cmd.Stdout = spec.Stdout
		cmd.Stderr = spec.Stderr
		if err := cmd.Start(); err != nil {
			cleanup()
			return nil, err
		}
		return &Execution{
			Resize:  func(cols, rows uint16) error { return nil },
			Kill:    func() error { return cmdKill(cmd) },
			Wait:    func() error { return cmd.Wait() },
			Cleanup: cleanup,
		}, nil
	}

	pr, pw, err := os.Pipe()
	if err != nil {
		cleanup()
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
			cleanup()
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
		cleanup()
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
		Cleanup:    cleanup,
	}, nil
}

// LaunchTerm starts argv inside a persistent detached container with PTY I/O.
// Used by terminal sessions. The container survives PTY disconnects and can be
// reattached; it is removed only by env.Cleanup(resourceID).
func (de *DockerEnvironment) LaunchTerm(_ context.Context, spec TermSpec) (*Execution, error) {
	containerName := "cham-" + spec.ID

	containerArgv := spec.Argv
	if de.innerTmux {
		if len(containerArgv) == 0 {
			containerArgv = de.imageCmd(de.image)
		}
		containerArgv = de.buildTmuxCmd(spec.ID, containerArgv, spec.Cols, spec.Rows)
	}

	cleanup, err := de.startContainer(containerName, containerArgv)
	if err != nil {
		return nil, err
	}

	exec_, err := de.attachContainer(containerName, spec.Cols, spec.Rows)
	if err != nil {
		de.removeContainer(containerName)
		cleanup()
		return nil, err
	}
	exec_.ResourceID = containerName
	exec_.Cleanup = cleanup
	return exec_, nil
}

// startContainer starts a detached container and returns a cleanup func that
// removes the temp init script (the container itself is removed by Cleanup).
func (de *DockerEnvironment) startContainer(containerName string, containerArgv []string) (func(), error) {
	credArgs, initEntry, cleanup, err := de.prepareInitScript()
	if err != nil {
		return func() {}, err
	}
	secretArgs, secretCleanup, err := de.prepareSecretEnvFile()
	if err != nil {
		cleanup()
		return func() {}, err
	}
	origCleanup := cleanup
	cleanup = func() { origCleanup(); secretCleanup() }

	runArgs := de.dockerArgs("run", "-d", "-it",
		"--name", containerName,
		"--rm",
		"--workdir", de.containerDir(),
		"-v", de.workspace.Path+":"+de.containerDir(),
	)
	runArgs = append(runArgs, credArgs...)
	for _, vol := range de.extraVolumes {
		runArgs = append(runArgs, "-v", vol)
	}
	runArgs = append(runArgs, de.dockerEnvArgs...)
	runArgs = append(runArgs, secretArgs...)
	runArgs = append(runArgs, currentHostTermEnvFlags(de.baseEnv)...)
	runArgs = append(runArgs, hostTimezoneArgs(de.baseEnv)...)
	runArgs = append(runArgs, de.dockerRunArgs...)

	if de.innerTmux {
		if hostCfg, ok := de.tmuxConfigMount(); ok {
			runArgs = append(runArgs, "-v", hostCfg+":"+dockerTmuxConfigPath+":ro")
		}
	}

	runArgs = append(runArgs, de.image)
	if initEntry != "" {
		if len(containerArgv) == 0 {
			containerArgv = de.imageCmd(de.image)
		}
		runArgs = append(runArgs, initEntry)
	}
	runArgs = append(runArgs, containerArgv...)

	if out, err := de.command(context.Background(), runArgs...).CombinedOutput(); err != nil {
		cleanup()
		return func() {}, fmt.Errorf("docker run: %w: %s", err, out)
	}
	return cleanup, nil
}

func (de *DockerEnvironment) removeContainer(name string) {
	_ = de.command(context.Background(), de.dockerArgs("rm", "-f", name)...).Run()
}

func (de *DockerEnvironment) Alive(resourceID string) bool {
	out, err := de.command(context.Background(), de.dockerArgs(
		"inspect", "-f", "{{.State.Running}}", resourceID,
	)...).Output()
	return err == nil && string(bytes.TrimSpace(out)) == "true"
}

func (de *DockerEnvironment) Cleanup(resourceID string) {
	de.removeContainer(resourceID)
}

func (de *DockerEnvironment) Reattach(resourceID string, cols, rows uint16) (*Execution, error) {
	exec_, err := de.attachContainer(resourceID, cols, rows)
	if err != nil {
		return nil, err
	}
	exec_.ResourceID = resourceID
	return exec_, nil
}

// ResizeWindow sends a tmux resize-window command through "docker exec".
// No-op when innerTmux is false (container does not run tmux).
func (de *DockerEnvironment) ResizeWindow(resourceID string, cols, rows uint16) error {
	if !de.innerTmux {
		return nil
	}
	args := de.dockerArgs("exec", resourceID,
		"tmux", "resize-window", "-t", resourceID,
		"-x", fmt.Sprint(cols), "-y", fmt.Sprint(rows))
	return de.command(context.Background(), args...).Run()
}

func (de *DockerEnvironment) attachContainer(containerName string, cols, rows uint16) (*Execution, error) {
	cmd := de.command(context.Background(), de.dockerArgs("attach", "--sig-proxy=false", containerName)...)
	cmd.Env = append(append([]string{}, de.baseEnv...), termEnv...)

	f, err := pty.StartWithSize(cmd, &pty.Winsize{Rows: rows, Cols: cols})
	if err != nil {
		return nil, err
	}
	return &Execution{
		IO:      f,
		Resize:  func(cols, rows uint16) error { return pty.Setsize(f, &pty.Winsize{Rows: rows, Cols: cols}) },
		Kill:    func() error { return cmdKill(cmd) },
		Wait:    func() error { return cmd.Wait() },
		Cleanup: func() {},
	}, nil
}

func envValue(base []string, key string) string {
	for i := len(base) - 1; i >= 0; i-- {
		k, v, _ := strings.Cut(base[i], "=")
		if k == key {
			return v
		}
	}
	return ""
}

func (de *DockerEnvironment) command(ctx context.Context, args ...string) *exec.Cmd {
	return commandContext(ctx, append([]string{}, de.baseEnv...), "docker", args...)
}
