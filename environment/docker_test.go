package environment

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type testProvider struct{}

func (testProvider) Name() string      { return "test" }
func (testProvider) Dirs() []CredDir   { return []CredDir{{HomeRelPath: ".agent", IsDir: true}} }
func (testProvider) EnvVars() []string { return nil }

func TestDockerExplicitCredentialRoot(t *testing.T) {
	home := t.TempDir()
	setup := DockerContainerSetup{DockerHome: home, Providers: []CredentialProvider{testProvider{}}}
	if err := setup.EnsureDirs(); err != nil {
		t.Fatal(err)
	}
	args := strings.Join(setup.MountArgs(""), " ")
	if !strings.Contains(args, filepath.Join(home, "test", ".agent")) {
		t.Fatal(args)
	}
	setup.DockerHome = ""
	if err := setup.EnsureDirs(); err == nil {
		t.Fatal("must not create credentials under cwd")
	}
}

func TestDockerLaunchArgumentsAndCleanup(t *testing.T) {
	bin := t.TempDir()
	script := filepath.Join(bin, "docker")
	// A local stand-in records argv. No daemon or image is involved.
	if err := os.WriteFile(script, []byte("#!/bin/sh\nprintf '%s\\n' \"$@\"\n"), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	env := NewDockerEnvironment(EnvironmentSpec{Workspace: Workspace{Path: "/tmp/project"}, Image: "test-image", DockerHome: t.TempDir(), ExtraInit: "echo configured", BaseEnv: []string{"PATH=" + os.Getenv("PATH")}, SecretEnv: []string{"TOKEN=private-value"}})
	execution, err := env.Launch(context.Background(), LaunchSpec{Argv: []string{"sh", "-c", "echo hello"}, MergeStderr: true})
	if err != nil {
		t.Fatal(err)
	}
	defer execution.Cleanup()
	data, err := io.ReadAll(execution.IO)
	_ = execution.IO.Close()
	if err != nil {
		t.Fatal(err)
	}
	if err := execution.Wait(); err != nil {
		t.Fatal(err)
	}
	argv := string(data)
	if strings.Contains(argv, "private-value") || !strings.Contains(argv, "--env-file\n") || !strings.Contains(argv, "/workspace/project") || !strings.Contains(argv, "/cham-init.sh") {
		t.Fatal(argv)
	}
	lines := strings.Split(argv, "\n")
	var secretPath string
	for i, line := range lines {
		if line == "--env-file" {
			secretPath = lines[i+1]
		}
	}
	info, err := os.Stat(secretPath)
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatalf("secret file permissions: %v %v", info, err)
	}
	execution.Cleanup()
	if _, err := os.Stat(secretPath); !os.IsNotExist(err) {
		t.Fatalf("secret file not removed: %v", err)
	}
}

func TestDockerRunArgsPassedBeforeImage(t *testing.T) {
	dir := t.TempDir()
	out := filepath.Join(dir, "argv")
	writeExe(t, dir, "docker", `printf '%s\n' "$@" > `+out)
	de := NewDockerEnvironment(EnvironmentSpec{
		Workspace: Workspace{Path: t.TempDir()}, Image: "img", BaseEnv: []string{"PATH=" + dir + ":/bin:/usr/bin"},
		DockerRunArgs: []string{"--network=host", "-v", "/a:/b"},
	})
	ex, err := de.Launch(context.Background(), LaunchSpec{Argv: []string{"true"}, MergeStderr: true})
	if err != nil {
		t.Fatal(err)
	}
	_, _ = io.ReadAll(ex.IO)
	_ = ex.Wait()
	ex.Cleanup()
	data, _ := os.ReadFile(out)
	argv := string(data)
	if i, j := strings.Index(argv, "--network=host\n-v\n/a:/b\n"), strings.Index(argv, "img\n"); i < 0 || j < i {
		t.Fatalf("argv = %q", argv)
	}
}
