package cli

import (
	"bytes"
	"context"
	"os"
	"strings"
	"testing"
)

func isolatedHome(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("CHAMELEON_HOME", home)
	t.Setenv("CHAMELEON_SECRET_PASSPHRASE", "")
	return home
}

func TestMainRunAndExitCode(t *testing.T) {
	isolatedHome(t)
	for _, tc := range []struct {
		prompt      string
		code        int
		out, errOut string
	}{{"printf hello", 0, "hello\n", ""}, {"printf failure; exit 9", 9, "", "failure"}} {
		var out, stderr bytes.Buffer
		code := Main(context.Background(), []string{"run", "--agent", "shell", "-p", tc.prompt}, IO{Stdout: &out, Stderr: &stderr})
		if code != tc.code || out.String() != tc.out || !strings.Contains(stderr.String(), tc.errOut) {
			t.Fatalf("code=%d stdout=%q stderr=%q", code, out.String(), stderr.String())
		}
	}
}

func TestCLIRejectsRemoteAndConflictingEnvironments(t *testing.T) {
	isolatedHome(t)
	for _, args := range [][]string{{"run", "--agent", "shell", "--docker", "example", "--sandbox", "-p", "true"}} {
		if got := Main(context.Background(), args, IO{}); got != 2 {
			t.Fatalf("%v: status %d", args, got)
		}
	}
}

func TestOptionsFromEnv(t *testing.T) {
	t.Setenv("CHAMELEON_HOME", t.TempDir())
	t.Setenv("CHAMELEON_SECRET_PASSPHRASE", "pass")
	options, err := OptionsFromEnv()
	if err != nil {
		t.Fatal(err)
	}
	if options.Home != os.Getenv("CHAMELEON_HOME") || string(options.Passphrase) != "pass" {
		t.Fatalf("settings not selected: %+v", options)
	}
}

func TestRunnerOnlyCommands(t *testing.T) {
	for _, command := range []string{"flow", "run-flow", "secret"} {
		var stderr bytes.Buffer
		if code := Main(context.Background(), []string{command}, IO{Stderr: &stderr}); code != 2 || !strings.Contains(stderr.String(), "unknown command") {
			t.Fatalf("%s: code=%d stderr=%q", command, code, stderr.String())
		}
	}
}
