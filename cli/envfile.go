package cli

import (
	"bufio"
	"errors"
	"fmt"
	"os"
	"strings"
)

// repeatedFlag collects every occurrence of a repeatable string flag.
type repeatedFlag []string

func (r *repeatedFlag) String() string     { return strings.Join(*r, ",") }
func (r *repeatedFlag) Set(v string) error { *r = append(*r, v); return nil }

// loadEnvFile reads NAME=value pairs from path. A missing file is an error only
// when required is true. Syntax is a small dotenv subset: blank lines and lines
// starting with '#' are ignored, an optional "export " prefix is accepted, and
// values may be 'single quoted' (literal) or "double quoted" (\n \t \r \" \\
// escapes). Unquoted values end at an unquoted " #" comment and are trimmed.
// There is no variable expansion and no multi-line values.
func loadEnvFile(path string, required bool) ([]string, error) {
	f, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) && !required {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	defer f.Close()
	var out []string
	scanner := bufio.NewScanner(f)
	for n := 1; scanner.Scan(); n++ {
		line := strings.TrimSpace(scanner.Text())
		if n == 1 {
			line = strings.TrimPrefix(line, "\xef\xbb\xbf")
		}
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		kv, err := parseEnvLine(line)
		if err != nil {
			return nil, fmt.Errorf("%s:%d: %w", path, n, err)
		}
		out = append(out, kv)
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return out, nil
}

func parseEnvLine(line string) (string, error) {
	line = strings.TrimPrefix(line, "export ")
	name, value, ok := strings.Cut(line, "=")
	name = strings.TrimSpace(name)
	if !ok {
		return "", fmt.Errorf("want NAME=value")
	}
	if !validEnvName(name) {
		return "", fmt.Errorf("invalid variable name %q", name)
	}
	value = strings.TrimSpace(value)
	switch {
	case strings.HasPrefix(value, "'"):
		end := strings.IndexByte(value[1:], '\'')
		if end < 0 {
			return "", fmt.Errorf("unterminated single quote for %s", name)
		}
		if err := onlyComment(value[end+2:], name); err != nil {
			return "", err
		}
		value = value[1 : end+1]
	case strings.HasPrefix(value, `"`):
		var b strings.Builder
		escaped, closed, i := false, false, 1
		for ; i < len(value) && !closed; i++ {
			c := value[i]
			switch {
			case escaped:
				escaped = false
				switch c {
				case 'n':
					b.WriteByte('\n')
				case 't':
					b.WriteByte('\t')
				case 'r':
					b.WriteByte('\r')
				case '"', '\\':
					b.WriteByte(c)
				default:
					b.WriteByte('\\')
					b.WriteByte(c)
				}
			case c == '\\':
				escaped = true
			case c == '"':
				closed = true
			default:
				b.WriteByte(c)
			}
		}
		if !closed {
			return "", fmt.Errorf("unterminated double quote for %s", name)
		}
		if err := onlyComment(value[i:], name); err != nil {
			return "", err
		}
		value = b.String()
	default:
		if i := strings.Index(value, " #"); i >= 0 {
			value = strings.TrimSpace(value[:i])
		}
	}
	return name + "=" + value, nil
}

// onlyComment accepts what follows a closing quote only if it is blank or a comment.
func onlyComment(rest, name string) error {
	rest = strings.TrimSpace(rest)
	if rest == "" || strings.HasPrefix(rest, "#") {
		return nil
	}
	return fmt.Errorf("unexpected text after quoted value for %s", name)
}

// parseEnvFlag resolves one --env value. NAME=value is used as given; a bare
// NAME copies the value from the calling environment and fails if it is unset,
// so the secret never has to appear on the command line.
func parseEnvFlag(arg string, lookup func(string) (string, bool)) (string, error) {
	if name, _, ok := strings.Cut(arg, "="); ok {
		if !validEnvName(name) {
			return "", fmt.Errorf("--env: invalid variable name %q", name)
		}
		return arg, nil
	}
	if !validEnvName(arg) {
		return "", fmt.Errorf("--env: invalid variable name %q", arg)
	}
	v, ok := lookup(arg)
	if !ok {
		return "", fmt.Errorf("--env %s: not set in the environment (use NAME=value)", arg)
	}
	return arg + "=" + v, nil
}

func validEnvName(s string) bool {
	for i, c := range s {
		if c == '_' || c >= 'A' && c <= 'Z' || c >= 'a' && c <= 'z' || i > 0 && c >= '0' && c <= '9' {
			continue
		}
		return false
	}
	return s != ""
}

// resolveEnvVars builds the extra environment in increasing precedence: the
// user env file ($home/env, optional), each --env-file in order, then --env.
func resolveEnvVars(home string, envFiles, envFlags []string, lookup func(string) (string, bool)) ([]string, error) {
	vars, err := loadEnvFile(home+string(os.PathSeparator)+"env", false)
	if err != nil {
		return nil, err
	}
	for _, path := range envFiles {
		more, err := loadEnvFile(path, true)
		if err != nil {
			return nil, fmt.Errorf("--env-file: %w", err)
		}
		vars = append(vars, more...)
	}
	for _, arg := range envFlags {
		kv, err := parseEnvFlag(arg, lookup)
		if err != nil {
			return nil, err
		}
		vars = append(vars, kv)
	}
	return vars, nil
}
