package cli

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func writeFile(t *testing.T, dir, name, content string) string {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, []byte(content), 0600); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestLoadEnvFile(t *testing.T) {
	p := writeFile(t, t.TempDir(), "env", "\xef\xbb\xbf# comment\n\nA=1\nexport B = two words \nC='sq # not comment' # trailing\nD=\"dq \\\"q\\\" \\n end\"\nE=plain # comment\nF=\nG=a=b\nH=\"\"\n")
	got, err := loadEnvFile(p, true)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"A=1", "B=two words", "C=sq # not comment", "D=dq \"q\" \n end", "E=plain", "F=", "G=a=b", "H="}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %q\nwant %q", got, want)
	}
}

func TestLoadEnvFileErrors(t *testing.T) {
	dir := t.TempDir()
	for content, msg := range map[string]string{
		"A=1\nnoequals\n":    ":2:",
		"1A=x\n":             "invalid variable name",
		"A='open\n":          "unterminated single",
		"A=\"open\n":         "unterminated double",
		"A='x' junk\n":       "unexpected text",
		"A=\"x\" junk # c\n": "unexpected text",
	} {
		_, err := loadEnvFile(writeFile(t, dir, "bad", content), true)
		if err == nil || !strings.Contains(err.Error(), msg) {
			t.Errorf("%q: err=%v, want %q", content, err, msg)
		}
	}
	if _, err := loadEnvFile(filepath.Join(dir, "missing"), true); err == nil {
		t.Error("required missing file must fail")
	}
	if got, err := loadEnvFile(filepath.Join(dir, "missing"), false); err != nil || got != nil {
		t.Errorf("optional missing file: %v %v", got, err)
	}
}

func TestResolveEnvVarsPrecedence(t *testing.T) {
	home, dir := t.TempDir(), t.TempDir()
	writeFile(t, home, "env", "A=home\nB=home\nC=home\n")
	f1 := writeFile(t, dir, "one.env", "B=file1\nC=file1\n")
	f2 := writeFile(t, dir, "two.env", "C=file2\n")
	lookup := func(k string) (string, bool) { v, ok := map[string]string{"FROMENV": "host"}[k]; return v, ok }
	got, err := resolveEnvVars(home, []string{f1, f2}, []string{"C=flag", "FROMENV"}, lookup)
	if err != nil {
		t.Fatal(err)
	}
	// Later entries win when the run merges them; order must be low to high precedence.
	want := []string{"A=home", "B=home", "C=home", "B=file1", "C=file1", "C=file2", "C=flag", "FROMENV=host"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %q\nwant %q", got, want)
	}
	if _, err := resolveEnvVars(home, nil, []string{"UNSET_VAR"}, lookup); err == nil {
		t.Error("bare --env NAME must fail when unset")
	}
	if _, err := resolveEnvVars(home, nil, []string{"BAD-NAME=x"}, lookup); err == nil {
		t.Error("invalid name must fail")
	}
	if _, err := resolveEnvVars(home, []string{filepath.Join(dir, "nope")}, nil, lookup); err == nil {
		t.Error("missing --env-file must fail")
	}
}

func TestParseRunArgsEnvFlags(t *testing.T) {
	opts, err := parseRunArgs([]string{"--agent", "shell", "--env", "A=1", "--env-file", "f1", "--env", "B", "--env-file", "f2", "-p", "x"})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(opts.envFlags, []string{"A=1", "B"}) || !reflect.DeepEqual(opts.envFiles, []string{"f1", "f2"}) {
		t.Fatalf("%+v", opts)
	}
}
