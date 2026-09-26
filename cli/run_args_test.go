package cli

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestParseRunArgsRequiresAgent(t *testing.T) {
	t.Parallel()

	_, err := parseRunArgs(nil)
	if err == nil || !strings.Contains(err.Error(), "--agent (or --runtime) is required") {
		t.Fatalf("parseRunArgs() error = %v, want missing runtime error", err)
	}
}

func TestParseRunArgsParsesValues(t *testing.T) {
	t.Setenv("CHAMELEON_DOCKER_SOCKET", "unix:///tmp/docker.sock")

	opts, err := parseRunArgs([]string{
		"--runtime", "codex",
		"--model", "gpt-5",
		"--session", "sess-1",
		"--yolo",
		"--dir", "/tmp/work",
		"--docker", "alpine:latest",
		"--sandbox",
		"--sandbox-no-network",
		"-p", "hello",
		"--setup",
		"--shell", "/bin/bash",
		"--",
		"--foo", "bar",
	})
	if err != nil {
		t.Fatalf("parseRunArgs() error = %v", err)
	}

	want := runOptions{
		runtime:          "codex",
		model:            "gpt-5",
		sessionID:        "sess-1",
		yolo:             true,
		dir:              "/tmp/work",
		dockerImage:      "alpine:latest",
		dockerSocket:     "unix:///tmp/docker.sock",
		sandboxMode:      true,
		sandboxNoNetwork: true,
		prompt:           "hello",
		setup:            true,
		shell:            "/bin/bash",
		extraArgs:        []string{"--foo", "bar"},
	}

	if !reflect.DeepEqual(opts, want) {
		t.Fatalf("parseRunArgs() = %#v, want %#v", opts, want)
	}
}

func TestParseRunArgsEmptyAgentRejected(t *testing.T) {
	t.Parallel()

	_, err := parseRunArgs([]string{"--runtime", "   "})
	if err == nil || !strings.Contains(err.Error(), "--agent (or --runtime) is required") {
		t.Fatalf("parseRunArgs() error = %v, want missing runtime error", err)
	}
}

func TestParseRunArgsUnknownFlag(t *testing.T) {
	t.Parallel()

	_, err := parseRunArgs([]string{"--runtime", "claude", "--wat"})
	if err == nil || !strings.Contains(err.Error(), "flag provided but not defined") {
		t.Fatalf("parseRunArgs() error = %v, want unknown flag error", err)
	}
}

func TestParseRunArgsMarkdownFile(t *testing.T) {
	f := writeTempMD(t, "---\nruntime: claude\nmodel: sonnet\n---\n\nSummarise ${repo}.\n")
	opts, err := parseRunArgs([]string{f, "repo=cham"})
	if err != nil {
		t.Fatalf("parseRunArgs() error = %v", err)
	}
	if opts.runtime != "claude" {
		t.Fatalf("runtime = %q, want claude", opts.runtime)
	}
	if opts.model != "sonnet" {
		t.Fatalf("model = %q, want sonnet", opts.model)
	}
	if opts.prompt != "\nSummarise cham.\n" {
		t.Fatalf("prompt = %q, want substituted body", opts.prompt)
	}
}

func TestParseRunArgsMarkdownRuntimeKey(t *testing.T) {
	f := writeTempMD(t, "---\nruntime: codex\nmodel: gpt-5\n---\n\nHello.\n")
	opts, err := parseRunArgs([]string{f})
	if err != nil {
		t.Fatalf("parseRunArgs() error = %v", err)
	}
	if opts.runtime != "codex" {
		t.Fatalf("runtime = %q, want codex (new runtime: key)", opts.runtime)
	}
	if opts.model != "gpt-5" {
		t.Fatalf("model = %q, want gpt-5", opts.model)
	}
}

func TestParseRunArgsMarkdownCLIOverridesFrontmatter(t *testing.T) {
	f := writeTempMD(t, "---\nruntime: claude\nmodel: sonnet\n---\n\nHello.\n")
	opts, err := parseRunArgs([]string{"--runtime", "codex", "--model", "gpt-5", f})
	if err != nil {
		t.Fatalf("parseRunArgs() error = %v", err)
	}
	if opts.runtime != "codex" {
		t.Fatalf("runtime = %q, want codex (CLI overrides frontmatter)", opts.runtime)
	}
	if opts.model != "gpt-5" {
		t.Fatalf("model = %q, want gpt-5 (CLI overrides frontmatter)", opts.model)
	}
}

func TestParseRunArgsMarkdownPromptOverridesBody(t *testing.T) {
	f := writeTempMD(t, "---\nruntime: claude\n---\n\nBody text.\n")
	opts, err := parseRunArgs([]string{"-p", "override", f})
	if err != nil {
		t.Fatalf("parseRunArgs() error = %v", err)
	}
	if opts.prompt != "override" {
		t.Fatalf("prompt = %q, want override", opts.prompt)
	}
}

func TestParseRunArgsMarkdownExtraKeyErrors(t *testing.T) {
	f := writeTempMD(t, "---\nruntime: claude\n---\n\nHello ${name}.\n")
	_, err := parseRunArgs([]string{f, "name=Alice", "typo=oops"})
	if err == nil {
		t.Fatal("expected error for unused key=value arg")
	}
}

func TestParseRunArgsMarkdownFlagsAfterFilename(t *testing.T) {
	f := writeTempMD(t, "---\nruntime: claude\nmodel: sonnet\n---\n\nHello ${name}.\n")
	opts, err := parseRunArgs([]string{f, "--runtime", "codex", "--model", "gpt-5", "name=alice"})
	if err != nil {
		t.Fatalf("parseRunArgs() error = %v", err)
	}
	if opts.runtime != "codex" {
		t.Fatalf("runtime = %q, want codex (CLI flag after filename overrides frontmatter)", opts.runtime)
	}
	if opts.model != "gpt-5" {
		t.Fatalf("model = %q, want gpt-5 (CLI flag after filename overrides frontmatter)", opts.model)
	}
	if opts.prompt != "\nHello alice.\n" {
		t.Fatalf("prompt = %q, want substituted body", opts.prompt)
	}
}

func TestParseRunArgsMarkdownPromptFlagValueContainsEquals(t *testing.T) {
	// -p "x=y" after filename: "x=y" is the value of -p, not a key=value pair
	f := writeTempMD(t, "---\nruntime: claude\n---\n\nBody.\n")
	opts, err := parseRunArgs([]string{f, "-p", "x=y"})
	if err != nil {
		t.Fatalf("parseRunArgs() error = %v", err)
	}
	if opts.prompt != "x=y" {
		t.Fatalf("prompt = %q, want x=y", opts.prompt)
	}
}

func TestParseRunArgsMarkdownFlagValueContainsEquals(t *testing.T) {
	// --model abc=def after filename: "abc=def" is the value of --model
	f := writeTempMD(t, "---\nruntime: claude\n---\n\nHello.\n")
	opts, err := parseRunArgs([]string{f, "--model", "abc=def"})
	if err != nil {
		t.Fatalf("parseRunArgs() error = %v", err)
	}
	if opts.model != "abc=def" {
		t.Fatalf("model = %q, want abc=def", opts.model)
	}
}

func TestParseRunArgsMarkdownFlagsInterleavedWithKV(t *testing.T) {
	// cham run task.md name=alice --model m
	// flags after the first key=value arg must still be parsed
	f := writeTempMD(t, "---\nruntime: claude\n---\n\nHello ${name}.\n")
	opts, err := parseRunArgs([]string{f, "name=alice", "--model", "m"})
	if err != nil {
		t.Fatalf("parseRunArgs() error = %v", err)
	}
	if opts.prompt != "\nHello alice.\n" {
		t.Fatalf("prompt = %q, want substituted body", opts.prompt)
	}
	if opts.model != "m" {
		t.Fatalf("model = %q, want m", opts.model)
	}
}

func TestParseRunArgsMarkdownEscaping(t *testing.T) {
	f := writeTempMD(t, "---\nruntime: claude\n---\n\nCost is $$5.\n")
	opts, err := parseRunArgs([]string{f})
	if err != nil {
		t.Fatalf("parseRunArgs() error = %v", err)
	}
	if opts.prompt != "\nCost is $5.\n" {
		t.Fatalf("prompt = %q, want $$ unescaped to $", opts.prompt)
	}
}

func TestParseRunArgsMarkdownWhitespaceOnlyBodyIsInteractive(t *testing.T) {
	// trailing blank line after frontmatter must not flip to headless mode
	f := writeTempMD(t, "---\nruntime: claude\n---\n\n")
	opts, err := parseRunArgs([]string{f})
	if err != nil {
		t.Fatalf("parseRunArgs() error = %v", err)
	}
	if opts.prompt != "" {
		t.Fatalf("prompt = %q, want empty (whitespace-only body should be interactive)", opts.prompt)
	}
}

func TestParseRunArgsMarkdownSetupSkipsBodySubstitution(t *testing.T) {
	// setup: true means no prompt is needed; body with unresolved placeholders must not error
	f := writeTempMD(t, "---\nruntime: claude\nsetup: true\n---\n\nLogin for ${account}.\n")
	opts, err := parseRunArgs([]string{f})
	if err != nil {
		t.Fatalf("parseRunArgs() error = %v", err)
	}
	if !opts.setup {
		t.Fatal("setup = false, want true")
	}
	if opts.prompt != "" {
		t.Fatalf("prompt = %q, want empty (setup mode ignores body)", opts.prompt)
	}
}

func TestParseRunArgsMarkdownShellSkipsBodySubstitution(t *testing.T) {
	f := writeTempMD(t, "---\nruntime: claude\nshell: /bin/bash\n---\n\nLogin for ${account}.\n")
	opts, err := parseRunArgs([]string{f})
	if err != nil {
		t.Fatalf("parseRunArgs() error = %v", err)
	}
	if opts.shell != "/bin/bash" {
		t.Fatalf("shell = %q, want /bin/bash", opts.shell)
	}
	if opts.prompt != "" {
		t.Fatalf("prompt = %q, want empty (shell mode ignores body)", opts.prompt)
	}
}

func TestParseRunArgsMarkdownMissingAgent(t *testing.T) {
	f := writeTempMD(t, "Just a body, no frontmatter.\n")
	_, err := parseRunArgs([]string{f})
	if err == nil || !strings.Contains(err.Error(), "--agent (or --runtime) is required") {
		t.Fatalf("error = %v, want missing runtime error", err)
	}
}

func writeTempMD(t *testing.T, content string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "task.md")
	if err := os.WriteFile(p, []byte(content), 0600); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestParseRunArgsDockerArgs(t *testing.T) {
	opts, err := parseRunArgs([]string{"--runtime", "claude", "--docker", "img", "--docker-args", `--network=host -v "/a b:/c" -e 'X=1 2' --label a\ b`})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"--network=host", "-v", "/a b:/c", "-e", "X=1 2", "--label", "a b"}
	if !reflect.DeepEqual(opts.dockerArgs, want) {
		t.Fatalf("dockerArgs = %q, want %q", opts.dockerArgs, want)
	}
	if _, err := parseRunArgs([]string{"--runtime", "claude", "--docker-args", `"open`}); err == nil {
		t.Fatal("unterminated quote must fail")
	}
}
