package runfile

import (
	"fmt"
	"os"
	"regexp"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

// MarkdownFrontmatter holds the YAML frontmatter fields from a .md run file.
// Field names match the corresponding CLI flags (dash-separated).
type MarkdownFrontmatter struct {
	Agent            string `yaml:"agent"`
	Runtime          string `yaml:"runtime"`
	Model            string `yaml:"model"`
	Yolo             bool   `yaml:"yolo"`
	Docker           string `yaml:"docker"`
	DockerSocket     string `yaml:"docker-socket"`
	Sandbox          bool   `yaml:"sandbox"`
	SandboxNoNetwork bool   `yaml:"sandbox-no-network"`
	Session          string `yaml:"session"`
	Dir              string `yaml:"dir"`
	Shell            string `yaml:"shell"`
	Setup            bool   `yaml:"setup"`
	// Tools is an allowlist of tool names for the agent. Maps to --tools on the CLI.
	// Empty means all tools are available. Supports glob patterns where the agent CLI allows them.
	Tools []string `yaml:"tools"`
}

// ParseMarkdownFile reads a markdown file, extracts YAML frontmatter and body.
// If the file does not start with "---", the entire content is returned as the body.
// If the opening "---" has no closing "---", the entire content is treated as the body.
func ParseMarkdownFile(path string) (MarkdownFrontmatter, string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return MarkdownFrontmatter{}, "", err
	}
	content := strings.ReplaceAll(string(data), "\r\n", "\n")

	var fm MarkdownFrontmatter
	body := content

	if strings.HasPrefix(content, "---\n") {
		rest := content[4:]
		end := -1
		bodyStart := 0
		if idx := strings.Index(rest, "\n---\n"); idx != -1 {
			end, bodyStart = idx, idx+5
		} else if strings.HasSuffix(rest, "\n---") {
			// closing delimiter at EOF with no trailing newline
			end, bodyStart = len(rest)-4, len(rest)
		} else if rest == "---\n" || rest == "---" {
			// frontmatter-only file: opening --- immediately followed by closing ---
			end, bodyStart = 0, len(rest)
		}
		if end != -1 {
			yamlSrc := rest[:end]
			if err := yaml.Unmarshal([]byte(yamlSrc), &fm); err != nil {
				return MarkdownFrontmatter{}, "", fmt.Errorf("%s: invalid frontmatter: %w", path, err)
			}
			body = rest[bodyStart:]
		}
		// no closing --- → treat entire file as body, no frontmatter
	}

	return fm, body, nil
}

// placeholderRe matches $$ (escape), ${key} (required), or ${key:-default} (optional).
// Group 1 = key, group 2 = default value (present only when :- appears).
var placeholderRe = regexp.MustCompile(`\$\$|\$\{([A-Za-z_][A-Za-z0-9_]*)(?::-([^}]*))?\}`)

// anyBracketRe matches $$ or any ${...} regardless of key validity, used to
// detect malformed placeholders before substitution.
var anyBracketRe = regexp.MustCompile(`\$\$|\$\{([^}]*)\}`)

var validKeyRe = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

// SubstitutePlaceholders replaces ${key} placeholders in body using key=value pairs.
// Missing keys and extra (unused) keys both return an error.
// $$ in the body is unescaped to a literal $.
func SubstitutePlaceholders(body, filename string, kvArgs []string) (string, error) {
	kvMap := map[string]string{}
	for _, arg := range kvArgs {
		eq := strings.IndexByte(arg, '=')
		if eq <= 0 {
			return "", fmt.Errorf("invalid key=value argument: %q", arg)
		}
		key := arg[:eq]
		if _, dup := kvMap[key]; dup {
			return "", fmt.Errorf("duplicate key=value argument: %q", arg)
		}
		value, err := resolvePlaceholderValue(arg[eq+1:])
		if err != nil {
			return "", fmt.Errorf("%s: %w", filename, err)
		}
		kvMap[key] = value
	}

	// validate: reject any ${...} whose content is not a valid key or key:-default
	for _, m := range anyBracketRe.FindAllStringSubmatch(body, -1) {
		if m[0] == "$$" {
			continue
		}
		content := m[1]
		key := content
		if idx := strings.Index(content, ":-"); idx >= 0 {
			key = content[:idx]
		}
		if !validKeyRe.MatchString(key) {
			return "", fmt.Errorf("%s: invalid placeholder ${%s}: key must match [A-Za-z_][A-Za-z0-9_]*", filename, content)
		}
	}
	// validate: reject unclosed ${ sequences (anyBracketRe only matches closed forms,
	// so if any ${ remains in the stripped remainder it was never closed)
	if remainder := anyBracketRe.ReplaceAllString(body, ""); strings.Contains(remainder, "${") {
		return "", fmt.Errorf("%s: unclosed placeholder (missing closing '}')", filename)
	}

	// collect placeholder keys; track whether each key has at least one required
	// (no default) occurrence and record the first default value seen.
	type keyUsage struct {
		required   bool
		hasDefault bool
		defaultVal string
	}
	keys := map[string]keyUsage{}
	for _, m := range placeholderRe.FindAllStringSubmatch(body, -1) {
		if m[1] == "" {
			continue
		}
		key := m[1]
		u := keys[key]
		if strings.Contains(m[0], ":-") {
			if !u.hasDefault {
				u.hasDefault = true
				u.defaultVal = m[2]
			}
		} else {
			u.required = true
		}
		keys[key] = u
	}

	var missing []string
	for key, u := range keys {
		if u.required {
			if _, ok := kvMap[key]; !ok {
				missing = append(missing, key)
			}
		}
	}
	if len(missing) > 0 {
		sort.Strings(missing)
		return "", fmt.Errorf("unresolved placeholder(s) in %s: %s", filename, strings.Join(missing, ", "))
	}

	var extra []string
	for key := range kvMap {
		if _, ok := keys[key]; !ok {
			extra = append(extra, key)
		}
	}
	if len(extra) > 0 {
		sort.Strings(extra)
		return "", fmt.Errorf("unused key=value argument(s): %s", strings.Join(extra, ", "))
	}

	return placeholderRe.ReplaceAllStringFunc(body, func(match string) string {
		if match == "$$" {
			return "$"
		}
		m := placeholderRe.FindStringSubmatch(match)
		key := m[1]
		if val, ok := kvMap[key]; ok {
			return val
		}
		return m[2] // default value
	}), nil
}

func resolvePlaceholderValue(raw string) (string, error) {
	switch {
	case strings.HasPrefix(raw, "@@"):
		return raw[1:], nil
	case strings.HasPrefix(raw, "@"):
		path := raw[1:]
		if path == "" {
			return "", fmt.Errorf("empty @file placeholder value")
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return "", fmt.Errorf("read @file %q: %w", path, err)
		}
		return string(data), nil
	default:
		return raw, nil
	}
}
