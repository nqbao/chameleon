package chameleon_test

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	cham "github.com/nqbao/chameleon"
	"github.com/nqbao/chameleon/secrets"
)

func TestRunExplicitEnvironmentAndSecrets(t *testing.T) {
	dir := t.TempDir()
	home := t.TempDir()
	if err := os.Mkdir(filepath.Join(dir, ".chameleon"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".chameleon", "secrets.yml"), []byte("secrets:\n  - app/value?as=VALUE\n"), 0600); err != nil {
		t.Fatal(err)
	}
	backend := &secrets.LocalBackend{Dir: filepath.Join(home, "secrets"), KeyFile: filepath.Join(home, "key")}
	if err := backend.Set("app/value", []byte("secret")); err != nil {
		t.Fatal(err)
	}
	t.Setenv("UNPASSED", "must-not-inherit")
	options := cham.Options{Home: home, BaseEnv: []string{"VALUE=base", "KEEP=kept", "CHAMELEON_SECRET_PASSPHRASE=hidden"}}
	result, err := options.Run(context.Background(), cham.Request{Runtime: "shell", Dir: dir, Prompt: `printf '%s|%s|%s|%s' "$VALUE" "$KEEP" "$UNPASSED" "$CHAMELEON_SECRET_PASSPHRASE"`})
	if err != nil {
		t.Fatal(err)
	}
	if result.Content != "secret|kept||" {
		t.Fatalf("content=%q", result.Content)
	}
}

func TestRunFailureAndCancellation(t *testing.T) {
	result, err := (cham.Options{}).Run(context.Background(), cham.Request{Runtime: "shell", Dir: t.TempDir(), Prompt: "printf failed; exit 7"})
	if err == nil || result.ExitCode != 7 || result.Content != "failed" {
		t.Fatalf("result=%+v err=%v", result, err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	start := time.Now()
	_, err = (cham.Options{}).Run(ctx, cham.Request{Runtime: "shell", Dir: t.TempDir(), Prompt: "exec sleep 10"})
	if err == nil || time.Since(start) > 2*time.Second {
		t.Fatalf("cancellation failed: %v", err)
	}
}

func TestRunInteractiveUsesCallerIO(t *testing.T) {
	var out bytes.Buffer
	_, err := (cham.Options{}).Run(context.Background(), cham.Request{Runtime: "shell", Dir: t.TempDir(), Prompt: "read answer; printf '%s' \"$answer\"", Interactive: true, Stdin: strings.NewReader("from-input\n"), Stdout: &out})
	if err != nil || out.String() != "from-input" {
		t.Fatalf("output=%q err=%v", out.String(), err)
	}
}

func TestMissingSecretFailsClosed(t *testing.T) {
	dir := t.TempDir()
	if err := os.Mkdir(filepath.Join(dir, ".chameleon"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".chameleon", "secrets.yml"), []byte("secrets: [missing?as=VALUE]\n"), 0600); err != nil {
		t.Fatal(err)
	}
	_, err := (cham.Options{Home: t.TempDir()}).Run(context.Background(), cham.Request{Runtime: "shell", Dir: dir, Prompt: "echo should-not-run"})
	if err == nil {
		t.Fatal("expected missing-secret error")
	}
}
