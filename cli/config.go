package cli

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"
)

// fileConfig is the content of a config.yml. Only docker run flags are supported.
type fileConfig struct {
	// Docker lists extra `docker run` flags. Each entry is split shell-style, so
	// "--network=host", "-v /a:/b" and separate "-v" / "/a:/b" entries all work.
	Docker []string `yaml:"docker"`
}

// loadConfigFile reads path. A missing file is not an error. Unknown keys are.
func loadConfigFile(path string) (fileConfig, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return fileConfig{}, nil
	}
	if err != nil {
		return fileConfig{}, err
	}
	var cfg fileConfig
	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true)
	if err := dec.Decode(&cfg); err != nil && !errors.Is(err, io.EOF) {
		return fileConfig{}, fmt.Errorf("%s: %w", path, err)
	}
	return cfg, nil
}

// dockerArgsFromFile returns the docker run flags configured in path.
func dockerArgsFromFile(path string) ([]string, error) {
	cfg, err := loadConfigFile(path)
	if err != nil {
		return nil, err
	}
	var args []string
	for _, entry := range cfg.Docker {
		words, err := splitArgs(entry)
		if err != nil {
			return nil, fmt.Errorf("%s: docker: %w", path, err)
		}
		args = append(args, words...)
	}
	return args, nil
}

// resolveDockerArgs merges docker run flags in increasing precedence: user config
// ($home/config.yml), workspace config (<dir>/.chameleon/config.yml), then the
// command line. Workspace flags come from the repository being worked on, so
// applying them is announced on stderr.
func resolveDockerArgs(home, dir string, cli []string, stderr io.Writer) ([]string, error) {
	userPath := filepath.Join(home, "config.yml")
	wsPath := filepath.Join(dir, ".chameleon", "config.yml")
	user, err := dockerArgsFromFile(userPath)
	if err != nil {
		return nil, err
	}
	var ws []string
	if !sameFile(userPath, wsPath) {
		if ws, err = dockerArgsFromFile(wsPath); err != nil {
			return nil, err
		}
	}
	if len(ws) > 0 {
		fmt.Fprintf(stderr, "cham: applying docker flags from %s: %s\n", wsPath, strings.Join(ws, " "))
	}
	args := append(append(append([]string{}, user...), ws...), cli...)
	return args, nil
}

func sameFile(a, b string) bool {
	ai, err1 := os.Stat(a)
	bi, err2 := os.Stat(b)
	return err1 == nil && err2 == nil && os.SameFile(ai, bi)
}
