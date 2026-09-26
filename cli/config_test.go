package cli

import (
	"bytes"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func writeConfig(t *testing.T, dir, body string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "config.yml"), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestResolveDockerArgsMergeOrder(t *testing.T) {
	home, ws := t.TempDir(), t.TempDir()
	writeConfig(t, home, "docker:\n  - --network=host\n  - -v \"/a b:/c\"\n")
	writeConfig(t, filepath.Join(ws, ".chameleon"), "docker:\n  - -e\n  - X=1\n")
	var stderr bytes.Buffer
	got, err := resolveDockerArgs(home, ws, []string{"--rm"}, &stderr)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"--network=host", "-v", "/a b:/c", "-e", "X=1", "--rm"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %q, want %q", got, want)
	}
	if !strings.Contains(stderr.String(), "applying docker flags from") {
		t.Fatalf("workspace flags must be announced, stderr=%q", stderr.String())
	}
}

func TestResolveDockerArgsMissingFilesAndErrors(t *testing.T) {
	var stderr bytes.Buffer
	got, err := resolveDockerArgs(t.TempDir(), t.TempDir(), nil, &stderr)
	if err != nil || len(got) != 0 || stderr.Len() != 0 {
		t.Fatalf("got=%q err=%v stderr=%q", got, err, stderr.String())
	}
	home := t.TempDir()
	writeConfig(t, home, "dockr: [--rm]\n")
	if _, err := resolveDockerArgs(home, t.TempDir(), nil, &stderr); err == nil {
		t.Fatal("unknown key must be an error")
	}
	writeConfig(t, home, "docker:\n  - '\"open'\n")
	if _, err := resolveDockerArgs(home, t.TempDir(), nil, &stderr); err == nil {
		t.Fatal("unterminated quote must be an error")
	}
}

func TestResolveDockerArgsSameFileNotAppliedTwice(t *testing.T) {
	home := t.TempDir()
	writeConfig(t, filepath.Join(home, ".chameleon"), "docker: [--rm]\n")
	// workspace dir == home/.chameleon's parent; user file is home/config.yml
	if err := os.Symlink(filepath.Join(home, ".chameleon", "config.yml"), filepath.Join(home, "config.yml")); err != nil {
		t.Fatal(err)
	}
	got, err := resolveDockerArgs(home, home, nil, &bytes.Buffer{})
	if err != nil || !reflect.DeepEqual(got, []string{"--rm"}) {
		t.Fatalf("got=%q err=%v", got, err)
	}
}
