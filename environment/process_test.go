package environment

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func writeExe(t *testing.T, dir, name, body string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte("#!/bin/sh\n"+body+"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
}

func TestHostCancelKillsDescendants(t *testing.T) {
	env := NewHostEnvironment(EnvironmentSpec{Workspace: Workspace{Path: t.TempDir()}, BaseEnv: os.Environ()})
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	start := time.Now()
	ex, err := env.Launch(ctx, LaunchSpec{Argv: []string{"sh", "-c", "sleep 5; echo done"}, MergeStderr: true})
	if err != nil {
		t.Fatal(err)
	}
	out, _ := io.ReadAll(ex.IO)
	_ = ex.Wait()
	if d := time.Since(start); d > 2*time.Second {
		t.Fatalf("cancellation took %v", d)
	}
	if strings.Contains(string(out), "done") {
		t.Fatalf("descendant kept running: %q", out)
	}
}

func TestHostResolvesExecutableFromSuppliedEnv(t *testing.T) {
	first, second := t.TempDir(), t.TempDir()
	writeExe(t, first, "cham-tool", "echo first")
	writeExe(t, second, "cham-tool", "echo second")
	env := NewHostEnvironment(EnvironmentSpec{
		Workspace: Workspace{Path: t.TempDir()},
		BaseEnv:   []string{"PATH=" + first + ":" + second + ":/bin:/usr/bin"},
	})
	ex, err := env.Launch(context.Background(), LaunchSpec{Argv: []string{"cham-tool"}, MergeStderr: true})
	if err != nil {
		t.Fatal(err)
	}
	out, _ := io.ReadAll(ex.IO)
	_ = ex.Wait()
	if got := strings.TrimSpace(string(out)); got != "first" {
		t.Fatalf("got %q, want first", got)
	}
	// A binary present only in the supplied PATH is found.
	only := t.TempDir()
	writeExe(t, only, "cham-only", "echo only")
	env = NewHostEnvironment(EnvironmentSpec{Workspace: Workspace{Path: t.TempDir()}, BaseEnv: []string{"PATH=" + only}})
	if _, err := env.Launch(context.Background(), LaunchSpec{Argv: []string{"cham-only"}, MergeStderr: true}); err != nil {
		t.Fatal(err)
	}
}

func TestDockerResolvesExecutableFromBaseEnv(t *testing.T) {
	dir := t.TempDir()
	writeExe(t, dir, "docker", "echo fake")
	de := NewDockerEnvironment(EnvironmentSpec{BaseEnv: []string{"PATH=" + dir}})
	out, err := de.command(context.Background()).Output()
	if err != nil || strings.TrimSpace(string(out)) != "fake" {
		t.Fatalf("out=%q err=%v", out, err)
	}
}

func TestDockerRejectsNewlineSecrets(t *testing.T) {
	for _, v := range []string{"TOKEN=first\nEXTRA=second", "TOKEN=a\rb"} {
		de := NewDockerEnvironment(EnvironmentSpec{Workspace: Workspace{Path: t.TempDir()}, Image: "img", SecretEnv: []string{v}})
		if _, err := de.Launch(context.Background(), LaunchSpec{Argv: []string{"true"}}); err == nil || !strings.Contains(err.Error(), "newline") {
			t.Fatalf("%q: err = %v", v, err)
		}
	}
}
