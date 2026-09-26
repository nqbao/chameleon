package chameleon_test

import (
	"context"
	"io"
	"strings"
	"testing"

	"github.com/nqbao/chameleon/environment"
	"os"
)

func TestHostEnvironmentLaunchEnvSpec(t *testing.T) {
	t.Setenv("CHAM_LAUNCH_KEEP", "kept")
	t.Setenv("CHAM_LAUNCH_DROP", "dropped")

	ws := environment.Workspace{Path: t.TempDir()}
	env := environment.NewHostEnvironment(environment.EnvironmentSpec{Workspace: ws, BaseEnv: os.Environ()})

	exec_, err := env.Launch(context.Background(), environment.LaunchSpec{
		Argv:     []string{"sh", "-c", "echo CHAM_LAUNCH_KEEP=$CHAM_LAUNCH_KEEP; echo CHAM_LAUNCH_DROP=$CHAM_LAUNCH_DROP; echo CHAM_LAUNCH_ADDED=$CHAM_LAUNCH_ADDED"},
		Env:      []string{"CHAM_LAUNCH_ADDED=added"},
		UnsetEnv: []string{"CHAM_LAUNCH_DROP"},
	})
	if err != nil {
		t.Fatalf("Launch: %v", err)
	}
	defer exec_.Cleanup()

	out, _ := io.ReadAll(exec_.IO)
	_ = exec_.Wait()
	s := string(out)

	if !strings.Contains(s, "CHAM_LAUNCH_KEEP=kept") {
		t.Errorf("want CHAM_LAUNCH_KEEP=kept in output, got %q", s)
	}
	if !strings.Contains(s, "CHAM_LAUNCH_DROP=\n") && !strings.Contains(s, "CHAM_LAUNCH_DROP=\r") {
		t.Errorf("want CHAM_LAUNCH_DROP unset (empty) in output, got %q", s)
	}
	if !strings.Contains(s, "CHAM_LAUNCH_ADDED=added") {
		t.Errorf("want CHAM_LAUNCH_ADDED=added in output, got %q", s)
	}
}

func TestHostEnvironmentLaunchHostEnv(t *testing.T) {
	ws := environment.Workspace{Path: t.TempDir()}
	env := environment.NewHostEnvironment(environment.EnvironmentSpec{
		Workspace: ws,
		HostEnv:   []string{"CHAM_HOST_ENV=from-config"},
		SecretEnv: []string{"CHAM_HOST_ENV=from-secret"}, // secrets must win over host.env
	})

	exec_, err := env.Launch(context.Background(), environment.LaunchSpec{
		Argv: []string{"sh", "-c", "echo CHAM_HOST_ENV=$CHAM_HOST_ENV"},
	})
	if err != nil {
		t.Fatalf("Launch: %v", err)
	}
	defer exec_.Cleanup()

	out, _ := io.ReadAll(exec_.IO)
	_ = exec_.Wait()
	if s := string(out); !strings.Contains(s, "CHAM_HOST_ENV=from-secret") {
		t.Errorf("want secrets to override host.env, got %q", s)
	}
}

func TestHostEnvironmentLaunchResourceID(t *testing.T) {
	t.Parallel()

	ws := environment.Workspace{Path: t.TempDir()}
	env := environment.NewHostEnvironment(environment.EnvironmentSpec{Workspace: ws, BaseEnv: os.Environ()})

	exec_, err := env.Launch(context.Background(), environment.LaunchSpec{Argv: []string{"sh", "-c", "exit 0"}})
	if err != nil {
		t.Fatalf("Launch: %v", err)
	}
	defer exec_.Cleanup()
	_ = exec_.Wait()

	if exec_.ResourceID == "" {
		t.Fatal("Launch: ResourceID should be non-empty (PID)")
	}
}
