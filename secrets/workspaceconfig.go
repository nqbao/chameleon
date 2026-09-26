package secrets

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"
)

// WorkspaceSecrets holds the secret refs declared in <workspace>/.chameleon/secrets.yml.
type WorkspaceSecrets struct {
	Secrets []string `yaml:"secrets"`
}

// LoadWorkspaceSecrets reads <workspacePath>/.chameleon/secrets.yml.
// Returns an empty config (no error) if the file does not exist.
func LoadWorkspaceSecrets(workspacePath string, directories ...string) (*WorkspaceSecrets, error) {
	dir := ".chameleon"
	if len(directories) > 0 && directories[0] != "" {
		dir = directories[0]
	}
	path := filepath.Join(workspacePath, dir, "secrets.yml")
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return &WorkspaceSecrets{}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
	var ws WorkspaceSecrets
	if err := yaml.Unmarshal(data, &ws); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	return &ws, nil
}

// isValidEnvName reports whether s is a valid POSIX environment variable name:
// [A-Za-z_][A-Za-z0-9_]*, no whitespace or special characters.
func isValidEnvName(s string) bool {
	if s == "" {
		return false
	}
	for i, c := range s {
		switch {
		case c >= 'A' && c <= 'Z', c >= 'a' && c <= 'z', c == '_':
			// always valid
		case c >= '0' && c <= '9':
			if i == 0 {
				return false // must not start with digit
			}
		default:
			return false
		}
	}
	return true
}

// ParseSecretRef parses a secret ref of the form "name" or "name?as=ENV_NAME".
// Without ?as=, the env var name is derived from name: uppercased with / and - replaced by _.
func ParseSecretRef(raw string) (name, envName string, err error) {
	name, alias, hasAlias := strings.Cut(raw, "?as=")
	if name == "" {
		return "", "", fmt.Errorf("empty secret name in ref %q", raw)
	}
	if hasAlias {
		if !isValidEnvName(alias) {
			return "", "", fmt.Errorf("invalid env var name %q in ref %q: only A-Z, a-z, 0-9, _ allowed, must not start with a digit", alias, raw)
		}
		return name, alias, nil
	}
	derived := strings.ToUpper(name)
	derived = strings.ReplaceAll(derived, "/", "_")
	derived = strings.ReplaceAll(derived, "-", "_")
	derived = strings.ReplaceAll(derived, ".", "_")
	return name, derived, nil
}

// Resolve calls backend.Get for each ref in refs and returns NAME=value pairs.
// Fails immediately if any secret is missing (fail-closed).
// Returns an error if duplicate env var names are declared.
func Resolve(backend Backend, refs []string) ([]string, error) {
	seen := make(map[string]bool, len(refs))
	result := make([]string, 0, len(refs))
	for _, ref := range refs {
		name, envName, err := ParseSecretRef(ref)
		if err != nil {
			return nil, err
		}
		if seen[envName] {
			return nil, fmt.Errorf("duplicate env var name %q in secrets config", envName)
		}
		seen[envName] = true
		value, err := backend.Get(name)
		if err != nil {
			return nil, fmt.Errorf("resolve secret %q: %w", name, err)
		}
		result = append(result, envName+"="+value)
	}
	return result, nil
}
