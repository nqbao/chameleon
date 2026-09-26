package runfile

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func emptyFrontmatter(t *testing.T, fm MarkdownFrontmatter) bool {
	t.Helper()
	return reflect.DeepEqual(fm, MarkdownFrontmatter{})
}

func TestParseMarkdownFileNoFrontmatter(t *testing.T) {
	t.Parallel()
	f := writeTempMD(t, "Just a plain body.\n")
	fm, body, err := ParseMarkdownFile(f)
	if err != nil {
		t.Fatalf("ParseMarkdownFile() error = %v", err)
	}
	if !emptyFrontmatter(t, fm) {
		t.Fatalf("expected empty frontmatter, got %+v", fm)
	}
	if body != "Just a plain body.\n" {
		t.Fatalf("body = %q, want plain body", body)
	}
}

func TestParseMarkdownFileToolsFrontmatter(t *testing.T) {
	t.Parallel()
	f := writeTempMD(t, "---\nruntime: claude\ntools:\n  - Read\n  - Edit\n  - Bash(git *)\n---\nDo a review.\n")
	fm, _, err := ParseMarkdownFile(f)
	if err != nil {
		t.Fatalf("ParseMarkdownFile() error = %v", err)
	}
	want := []string{"Read", "Edit", "Bash(git *)"}
	if !reflect.DeepEqual(fm.Tools, want) {
		t.Fatalf("tools = %v, want %v", fm.Tools, want)
	}
}

func TestParseMarkdownFileFrontmatterAndBody(t *testing.T) {
	t.Parallel()
	f := writeTempMD(t, "---\nruntime: claude\nmodel: sonnet\nyolo: true\n---\n\nHello ${name}.\n")
	fm, body, err := ParseMarkdownFile(f)
	if err != nil {
		t.Fatalf("ParseMarkdownFile() error = %v", err)
	}
	if fm.Runtime != "claude" || fm.Model != "sonnet" || !fm.Yolo {
		t.Fatalf("frontmatter = %+v, want runtime=claude model=sonnet yolo=true", fm)
	}
	if body != "\nHello ${name}.\n" {
		t.Fatalf("body = %q", body)
	}
}

func TestParseMarkdownFileRuntimeKey(t *testing.T) {
	t.Parallel()
	f := writeTempMD(t, "---\nruntime: codex\nmodel: gpt-5\n---\n\nHello.\n")
	fm, _, err := ParseMarkdownFile(f)
	if err != nil {
		t.Fatalf("ParseMarkdownFile() error = %v", err)
	}
	if fm.Runtime != "codex" || fm.Model != "gpt-5" {
		t.Fatalf("frontmatter = %+v, want runtime=codex model=gpt-5", fm)
	}
}

func TestParseMarkdownFileClosingDelimiterAtEOF(t *testing.T) {
	t.Parallel()
	// no trailing newline after closing ---
	f := writeTempMD(t, "---\nruntime: claude\n---")
	fm, body, err := ParseMarkdownFile(f)
	if err != nil {
		t.Fatalf("ParseMarkdownFile() error = %v", err)
	}
	if fm.Runtime != "claude" {
		t.Fatalf("runtime = %q, want claude", fm.Runtime)
	}
	if body != "" {
		t.Fatalf("body = %q, want empty", body)
	}
}

func TestParseMarkdownFileFrontmatterOnlyWithNewline(t *testing.T) {
	t.Parallel()
	// "---\n---\n" — empty frontmatter, no body
	f := writeTempMD(t, "---\n---\n")
	fm, body, err := ParseMarkdownFile(f)
	if err != nil {
		t.Fatalf("ParseMarkdownFile() error = %v", err)
	}
	if !emptyFrontmatter(t, fm) {
		t.Fatalf("expected empty frontmatter, got %+v", fm)
	}
	if body != "" {
		t.Fatalf("body = %q, want empty", body)
	}
}

func TestParseMarkdownFileFrontmatterOnlyAtEOF(t *testing.T) {
	t.Parallel()
	// "---\n---" — empty frontmatter, no body, no trailing newline
	f := writeTempMD(t, "---\n---")
	fm, body, err := ParseMarkdownFile(f)
	if err != nil {
		t.Fatalf("ParseMarkdownFile() error = %v", err)
	}
	if !emptyFrontmatter(t, fm) {
		t.Fatalf("expected empty frontmatter, got %+v", fm)
	}
	if body != "" {
		t.Fatalf("body = %q, want empty", body)
	}
}

func TestParseMarkdownFileUnclosedFrontmatter(t *testing.T) {
	t.Parallel()
	content := "---\nruntime: claude\nThis line has no closing delimiter.\n"
	f := writeTempMD(t, content)
	fm, body, err := ParseMarkdownFile(f)
	if err != nil {
		t.Fatalf("ParseMarkdownFile() error = %v", err)
	}
	if !emptyFrontmatter(t, fm) {
		t.Fatalf("expected empty frontmatter for unclosed block, got %+v", fm)
	}
	if body != content {
		t.Fatalf("body = %q, want entire file content", body)
	}
}

func TestParseMarkdownFileCRLF(t *testing.T) {
	t.Parallel()
	f := writeTempMD(t, "---\r\nruntime: claude\r\nmodel: sonnet\r\n---\r\n\r\nHello ${name}.\r\n")
	fm, body, err := ParseMarkdownFile(f)
	if err != nil {
		t.Fatalf("ParseMarkdownFile() error = %v", err)
	}
	if fm.Runtime != "claude" || fm.Model != "sonnet" {
		t.Fatalf("frontmatter = %+v, want runtime=claude model=sonnet", fm)
	}
	if body != "\nHello ${name}.\n" {
		t.Fatalf("body = %q, want LF-normalized body", body)
	}
}

func TestParseMarkdownFileInvalidYAML(t *testing.T) {
	t.Parallel()
	f := writeTempMD(t, "---\n: bad: yaml: here\n---\nbody\n")
	_, _, err := ParseMarkdownFile(f)
	if err == nil {
		t.Fatal("expected error for invalid YAML, got nil")
	}
}

func TestParseMarkdownFileNotFound(t *testing.T) {
	t.Parallel()
	_, _, err := ParseMarkdownFile("/nonexistent/path.md")
	if err == nil {
		t.Fatal("expected error for missing file, got nil")
	}
}

func TestSubstitutePlaceholdersBasic(t *testing.T) {
	t.Parallel()
	out, err := SubstitutePlaceholders("Hello ${name}, you are ${age}.", "f.md", []string{"name=Alice", "age=30"})
	if err != nil {
		t.Fatalf("error = %v", err)
	}
	if out != "Hello Alice, you are 30." {
		t.Fatalf("output = %q", out)
	}
}

func TestSubstitutePlaceholdersMissingKey(t *testing.T) {
	t.Parallel()
	_, err := SubstitutePlaceholders("Hello ${name}.", "f.md", []string{})
	if err == nil || err.Error() == "" {
		t.Fatal("expected error for missing key")
	}
}

func TestSubstitutePlaceholdersExtraKey(t *testing.T) {
	t.Parallel()
	_, err := SubstitutePlaceholders("Hello ${name}.", "f.md", []string{"name=Alice", "extra=oops"})
	if err == nil {
		t.Fatal("expected error for extra key")
	}
}

func TestSubstitutePlaceholdersEscaping(t *testing.T) {
	t.Parallel()
	out, err := SubstitutePlaceholders("Cost is $${price}.", "f.md", []string{})
	if err != nil {
		t.Fatalf("error = %v", err)
	}
	if out != "Cost is ${price}." {
		t.Fatalf("output = %q", out)
	}
}

func TestSubstitutePlaceholdersBareDollar(t *testing.T) {
	t.Parallel()
	// bare $word (no braces) must not be substituted
	out, err := SubstitutePlaceholders("$HOME is not substituted.", "f.md", []string{})
	if err != nil {
		t.Fatalf("error = %v", err)
	}
	if out != "$HOME is not substituted." {
		t.Fatalf("output = %q", out)
	}
}

func TestSubstitutePlaceholdersUnclosed(t *testing.T) {
	t.Parallel()
	_, err := SubstitutePlaceholders("Hello ${name", "f.md", []string{})
	if err == nil {
		t.Fatal("expected error for unclosed placeholder")
	}
}

func TestParseMarkdownFileBodyLeadingBlankLinesPreserved(t *testing.T) {
	t.Parallel()
	f := writeTempMD(t, "---\nruntime: claude\n---\n\n\n# Heading\n")
	_, body, err := ParseMarkdownFile(f)
	if err != nil {
		t.Fatalf("ParseMarkdownFile() error = %v", err)
	}
	if body != "\n\n# Heading\n" {
		t.Fatalf("body = %q, want leading blank lines preserved", body)
	}
}

func TestParseMarkdownFileBodyNoBlankLine(t *testing.T) {
	t.Parallel()
	f := writeTempMD(t, "---\nruntime: claude\n---\nNo blank line.\n")
	_, body, err := ParseMarkdownFile(f)
	if err != nil {
		t.Fatalf("ParseMarkdownFile() error = %v", err)
	}
	if body != "No blank line.\n" {
		t.Fatalf("body = %q, want no leading newline when file has none", body)
	}
}

func TestSubstitutePlaceholdersInvalidPlaceholderKey(t *testing.T) {
	t.Parallel()
	_, err := SubstitutePlaceholders("Hello ${1bad}.", "f.md", []string{})
	if err == nil {
		t.Fatal("expected error for invalid placeholder key starting with digit")
	}
}

func TestSubstitutePlaceholdersInvalidPlaceholderColon(t *testing.T) {
	t.Parallel()
	_, err := SubstitutePlaceholders("Hello ${slugify:x}.", "f.md", []string{})
	if err == nil {
		t.Fatal("expected error for invalid placeholder key containing colon")
	}
}

func TestSubstitutePlaceholdersInvalidPlaceholderEmpty(t *testing.T) {
	t.Parallel()
	_, err := SubstitutePlaceholders("Hello ${}.", "f.md", []string{})
	if err == nil {
		t.Fatal("expected error for empty placeholder key")
	}
}

func TestSubstitutePlaceholdersEscapedInvalidNotFlagged(t *testing.T) {
	t.Parallel()
	// $${1bad} is an escaped $ followed by literal {1bad} — not a placeholder, no error
	out, err := SubstitutePlaceholders("$${1bad}", "f.md", []string{})
	if err != nil {
		t.Fatalf("error = %v, want no error for escaped sequence", err)
	}
	if out != "${1bad}" {
		t.Fatalf("output = %q, want ${1bad}", out)
	}
}

func TestSubstitutePlaceholdersDuplicateKey(t *testing.T) {
	t.Parallel()
	_, err := SubstitutePlaceholders("${name}", "f.md", []string{"name=Alice", "name=Bob"})
	if err == nil || !strings.Contains(err.Error(), "name=Bob") {
		t.Fatalf("error = %v, want duplicate error containing full arg", err)
	}
}

func TestSubstitutePlaceholdersInvalidKV(t *testing.T) {
	t.Parallel()
	_, err := SubstitutePlaceholders("${x}", "f.md", []string{"notakeyvalue"})
	if err == nil {
		t.Fatal("expected error for invalid key=value arg")
	}
}

func TestSubstitutePlaceholdersFileValue(t *testing.T) {
	t.Parallel()
	tmp := filepath.Join(t.TempDir(), "payload.txt")
	if err := os.WriteFile(tmp, []byte("line 1\nline 2\n"), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	out, err := SubstitutePlaceholders("Content:\n${payload}", "f.md", []string{"payload=@" + tmp})
	if err != nil {
		t.Fatalf("error = %v", err)
	}
	if out != "Content:\nline 1\nline 2\n" {
		t.Fatalf("output = %q", out)
	}
}

func TestSubstitutePlaceholdersFileValueMissingFile(t *testing.T) {
	t.Parallel()
	_, err := SubstitutePlaceholders("${payload}", "f.md", []string{"payload=@/nonexistent/file.txt"})
	if err == nil || !strings.Contains(err.Error(), `read @file "/nonexistent/file.txt"`) {
		t.Fatalf("error = %v, want missing @file error", err)
	}
}

func TestSubstitutePlaceholdersFileValueEscapedAt(t *testing.T) {
	t.Parallel()
	out, err := SubstitutePlaceholders("${payload}", "f.md", []string{"payload=@@literal"})
	if err != nil {
		t.Fatalf("error = %v", err)
	}
	if out != "@literal" {
		t.Fatalf("output = %q, want @literal", out)
	}
}

func TestSubstitutePlaceholdersOptionalWithDefault(t *testing.T) {
	t.Parallel()
	out, err := SubstitutePlaceholders("Hello ${name:-World}.", "f.md", []string{})
	if err != nil {
		t.Fatalf("error = %v", err)
	}
	if out != "Hello World." {
		t.Fatalf("output = %q, want default value", out)
	}
}

func TestSubstitutePlaceholdersOptionalOverridden(t *testing.T) {
	t.Parallel()
	out, err := SubstitutePlaceholders("Hello ${name:-World}.", "f.md", []string{"name=Alice"})
	if err != nil {
		t.Fatalf("error = %v", err)
	}
	if out != "Hello Alice." {
		t.Fatalf("output = %q, want supplied value", out)
	}
}

func TestSubstitutePlaceholdersOptionalEmptyDefault(t *testing.T) {
	t.Parallel()
	out, err := SubstitutePlaceholders("Hello ${name:-}.", "f.md", []string{})
	if err != nil {
		t.Fatalf("error = %v", err)
	}
	if out != "Hello ." {
		t.Fatalf("output = %q, want empty default", out)
	}
}

func TestSubstitutePlaceholdersOptionalMixedRequired(t *testing.T) {
	t.Parallel()
	out, err := SubstitutePlaceholders("${greeting:-Hi} ${name}.", "f.md", []string{"name=Alice"})
	if err != nil {
		t.Fatalf("error = %v", err)
	}
	if out != "Hi Alice." {
		t.Fatalf("output = %q", out)
	}
}

func TestSubstitutePlaceholdersOptionalMissingRequired(t *testing.T) {
	t.Parallel()
	_, err := SubstitutePlaceholders("${greeting:-Hi} ${name}.", "f.md", []string{})
	if err == nil {
		t.Fatal("expected error: ${name} is required but not supplied")
	}
}

func writeTempMD(t *testing.T, content string) string {
	t.Helper()
	f := filepath.Join(t.TempDir(), "test.md")
	if err := os.WriteFile(f, []byte(content), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	return f
}
