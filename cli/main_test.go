package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"

	chameleon "github.com/nqbao/chameleon"
	"github.com/nqbao/chameleon/adapter"
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

func TestStreamPrinter(t *testing.T) {
	var out, errw bytes.Buffer
	p := streamPrinter{stdout: &out, stderr: &errw}
	p.handle(adapter.StreamEvent{Type: "chunk", Content: "hel"})
	p.handle(adapter.StreamEvent{Type: "chunk", Content: "lo"})
	p.handle(adapter.StreamEvent{Type: "toolCall", Name: "Bash", Detail: "make"})
	p.handle(adapter.StreamEvent{Type: "message", Content: "hello"})
	p.handle(adapter.StreamEvent{Type: "chunk", Content: "done\n"})
	p.finish()
	if out.String() != "hello\ndone\n" || errw.String() != "  Bash: make\n" || !p.chunks || !p.tools {
		t.Fatalf("stdout=%q stderr=%q chunks=%v tools=%v", out.String(), errw.String(), p.chunks, p.tools)
	}
}

func TestStreamFlagValidation(t *testing.T) {
	if _, err := parseRunArgs([]string{"--agent", "claude", "--stream"}); err == nil {
		t.Fatal("--stream without -p should fail")
	}
	if _, err := parseRunArgs([]string{"--agent", "claude", "--stream", "-p", "x", "--schema", "{}"}); err == nil {
		t.Fatal("--stream with --schema should fail")
	}
	opts, err := parseRunArgs([]string{"--agent", "claude", "--stream", "-p", "x"})
	if err != nil || !opts.stream {
		t.Fatalf("opts=%+v err=%v", opts, err)
	}
}

func TestJSONLWriter(t *testing.T) {
	var out, errw bytes.Buffer
	w := &jsonlWriter{enc: json.NewEncoder(&out)}
	w.handle(adapter.StreamEvent{Type: "chunk", Content: "hi"})
	w.handle(adapter.StreamEvent{Type: "toolCall", Name: "Read", Detail: "a.go"})
	w.handle(adapter.StreamEvent{Type: "message", Content: "hi"})
	if code := w.finish(chameleon.Result{Content: "hi", SessionID: "s1"}, nil, &errw); code != 0 {
		t.Fatalf("code=%d", code)
	}
	want := `{"type":"text","content":"hi"}
{"type":"tool","name":"Read","detail":"a.go"}
{"type":"done","session_id":"s1"}
`
	if out.String() != want {
		t.Fatalf("got %q", out.String())
	}
}

func TestMessageBoundaries(t *testing.T) {
	events := []adapter.StreamEvent{
		{Type: "chunk", Content: "Checking now."}, {Type: "message", Content: "Checking now."},
		{Type: "chunk", Content: "Final "}, {Type: "chunk", Content: "answer."}, {Type: "message", Content: "Final answer."},
	}
	var out, errw bytes.Buffer
	p := streamPrinter{stdout: &out, stderr: &errw}
	for _, ev := range events {
		p.handle(ev)
	}
	p.finish()
	if out.String() != "Checking now.\nFinal answer.\n" {
		t.Fatalf("stream = %q", out.String())
	}
	out.Reset()
	w := &jsonlWriter{enc: json.NewEncoder(&out)}
	for _, ev := range events {
		w.handle(ev)
	}
	want := "{\"type\":\"text\",\"content\":\"Checking now.\"}\n{\"type\":\"text\",\"content\":\"\\nFinal \"}\n{\"type\":\"text\",\"content\":\"answer.\"}\n"
	if out.String() != want {
		t.Fatalf("jsonl = %q", out.String())
	}
}

type failWriter struct{}

func (failWriter) Write([]byte) (int, error) { return 0, errors.New("output unavailable") }

func TestJSONLWriteErrorFailsRun(t *testing.T) {
	var errw bytes.Buffer
	w := &jsonlWriter{enc: json.NewEncoder(failWriter{})}
	if code := w.finish(chameleon.Result{Content: "ok"}, nil, &errw); code != 1 || !strings.Contains(errw.String(), "output unavailable") {
		t.Fatalf("code=%d stderr=%q", code, errw.String())
	}
}

func TestJSONLErrorCarriesAgentOutput(t *testing.T) {
	var out, errw bytes.Buffer
	w := &jsonlWriter{enc: json.NewEncoder(&out)}
	w.finish(chameleon.Result{ExitCode: 9, Content: "specific failure"}, errors.New("exit status 9"), &errw)
	if want := "{\"type\":\"error\",\"content\":\"specific failure\",\"message\":\"exit status 9\",\"exit_code\":9}\n"; out.String() != want {
		t.Fatalf("got %q", out.String())
	}
	if !strings.Contains(errw.String(), "specific failure") {
		t.Fatalf("stderr=%q", errw.String())
	}
}

func TestFailureAfterProgressKeepsExplanation(t *testing.T) {
	var out, errw bytes.Buffer
	w := &jsonlWriter{enc: json.NewEncoder(&out)}
	w.handle(adapter.StreamEvent{Type: "chunk", Content: "Checking now."})
	w.handle(adapter.StreamEvent{Type: "message", Content: "Checking now."})
	w.finish(chameleon.Result{ExitCode: 9, Content: "specific failure after progress"}, errors.New("exit status 9"), &errw)
	if !strings.Contains(out.String(), `"content":"specific failure after progress"`) || !strings.Contains(errw.String(), "specific failure after progress") {
		t.Fatalf("stdout=%q stderr=%q", out.String(), errw.String())
	}
	// failure text that is exactly what was streamed is not repeated
	out.Reset()
	w = &jsonlWriter{enc: json.NewEncoder(&out)}
	w.handle(adapter.StreamEvent{Type: "chunk", Content: "boom"})
	w.finish(chameleon.Result{ExitCode: 1, Content: "boom"}, errors.New("exit status 1"), &errw)
	if n := strings.Count(out.String(), "boom"); n != 1 {
		t.Fatalf("duplicated: %q", out.String())
	}
}

func TestJSONLInitFailureEmitsErrorEvent(t *testing.T) {
	isolatedHome(t)
	var out, errw bytes.Buffer
	code := Main(context.Background(), []string{"run", "--agent", "shell", "-p", "true", "--events-jsonl", "--env-file", "/nonexistent/env"}, IO{Stdout: &out, Stderr: &errw})
	if code != 1 || !strings.HasPrefix(out.String(), `{"type":"error"`) {
		t.Fatalf("code=%d stdout=%q", code, out.String())
	}
}

func TestJSONLWriterFallbackAndError(t *testing.T) {
	var out, errw bytes.Buffer
	w := &jsonlWriter{enc: json.NewEncoder(&out)}
	w.finish(chameleon.Result{Content: "final"}, nil, &errw)
	if want := "{\"type\":\"text\",\"content\":\"final\"}\n{\"type\":\"done\"}\n"; out.String() != want {
		t.Fatalf("got %q", out.String())
	}
	out.Reset()
	if code := w.finish(chameleon.Result{ExitCode: 3}, errors.New("boom"), &errw); code != 3 {
		t.Fatalf("code=%d", code)
	}
	if want := "{\"type\":\"error\",\"message\":\"boom\",\"exit_code\":3}\n"; out.String() != want {
		t.Fatalf("got %q", out.String())
	}
}

func TestEventsJSONLFlagValidation(t *testing.T) {
	for _, args := range [][]string{
		{"--agent", "claude", "--events-jsonl"},
		{"--agent", "claude", "--events-jsonl", "--stream", "-p", "x"},
		{"--agent", "claude", "--events-jsonl", "-p", "x", "--schema", "{}"},
		{"--agent", "claude", "--events-jsonl", "-p", "x", "--setup"},
		{"--agent", "claude", "--stream", "-p", "x", "--shell", "/bin/sh"},
	} {
		if _, err := parseRunArgs(args); err == nil {
			t.Fatalf("%v should fail", args)
		}
	}
	if opts, err := parseRunArgs([]string{"--agent", "claude", "--events-jsonl", "-p", "x"}); err != nil || !opts.eventsJSONL {
		t.Fatalf("opts=%+v err=%v", opts, err)
	}
}
