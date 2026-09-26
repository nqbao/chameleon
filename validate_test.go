package chameleon

import (
	"context"
	"strings"
	"testing"

	"github.com/nqbao/chameleon/environment"
)

func TestValidateChatEnvironment(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name             string
		runtime          string
		environment      string
		environmentImage string
		wantErr          string
	}{
		{
			name:        "docker requires image",
			runtime:     "claude",
			environment: "docker",
			wantErr:     "docker environment requires an image",
		},
		{
			name:             "sandbox and docker are exclusive",
			runtime:          "claude",
			environment:      "sandbox",
			environmentImage: "alpine",
			wantErr:          "sandbox and docker environments cannot be used together",
		},
		{
			name:        "unsupported environment",
			runtime:     "claude",
			environment: "podman",
			wantErr:     `unsupported environment "podman"`,
		},
		{
			name:    "plain host exec allowed",
			runtime: "claude",
		},
		{
			name:             "docker with image allowed",
			runtime:          "claude",
			environment:      "docker",
			environmentImage: "alpine:latest",
		},
		{
			name:        "explicit host allowed",
			runtime:     "claude",
			environment: "host",
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			err := ValidateEnvironment(tt.runtime, tt.environment, tt.environmentImage)
			if tt.wantErr == "" {
				if err != nil {
					t.Fatalf("ValidateEnvironment() error = %v, want nil", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("ValidateEnvironment() error = %v, want substring %q", err, tt.wantErr)
			}
		})
	}
}

func TestApplyEnvOverrides(t *testing.T) {
	t.Parallel()

	base := []string{"KEEP=1", "DROP=1", "OVERRIDE=old"}

	got := strings.Join(environment.ApplyEnvOverrides(base, []string{"OVERRIDE=new", "ADDED=1"}, []string{"DROP"}), "\n")

	if !strings.Contains(got, "KEEP=1") {
		t.Fatalf("applyEnvOverrides: want KEEP=1, got %q", got)
	}
	if strings.Contains(got, "DROP=1") {
		t.Fatalf("applyEnvOverrides: want DROP removed, got %q", got)
	}
	if !strings.Contains(got, "OVERRIDE=new") {
		t.Fatalf("applyEnvOverrides: want OVERRIDE=new, got %q", got)
	}
	if !strings.Contains(got, "ADDED=1") {
		t.Fatalf("applyEnvOverrides: want ADDED=1, got %q", got)
	}
	if strings.Contains(got, "OVERRIDE=old") {
		t.Fatalf("applyEnvOverrides: want old OVERRIDE value gone, got %q", got)
	}
}

func TestApplyEnvOverridesNoop(t *testing.T) {
	t.Parallel()

	base := []string{"A=1", "B=2"}
	got := environment.ApplyEnvOverrides(base, nil, nil)
	if &got[0] != &base[0] {
		t.Fatal("applyEnvOverrides with no overrides should return base slice unchanged")
	}
}

func TestDockerArgsRequireDocker(t *testing.T) {
	_, err := (Options{}).Run(context.Background(), Request{Runtime: "shell", Dir: t.TempDir(), Prompt: "true", DockerArgs: []string{"--rm"}})
	if err == nil || !strings.Contains(err.Error(), "docker args require docker") {
		t.Fatalf("err = %v", err)
	}
}

func TestDockerImageCannotBeAFlag(t *testing.T) {
	if err := ValidateEnvironment("shell", "docker", "--privileged"); err == nil {
		t.Fatal("image starting with '-' must be rejected")
	}
}

func TestGuardArgs(t *testing.T) {
	for _, req := range []Request{{Model: "-m"}, {SessionID: "--x"}, {Tools: []string{"Read", "-Bash"}}, {Files: []string{"-f"}}, {Images: []string{"--i"}}, {DockerSocket: "--privileged"}} {
		if err := guardArgs(&req); err == nil {
			t.Fatalf("%+v: expected rejection", req)
		}
	}
	req := Request{Prompt: "--yolo do it", SystemPrompt: "- bullet", Model: "gpt-5"}
	if err := guardArgs(&req); err != nil || req.Prompt != " --yolo do it" || req.SystemPrompt != " - bullet" {
		t.Fatalf("req=%+v err=%v", req, err)
	}
}
