package chameleon

import (
	"fmt"
	"strings"

	"github.com/nqbao/chameleon/adapter"
	envpkg "github.com/nqbao/chameleon/environment"
)

// ValidateEnvironment checks that the requested environment/environmentImage
// combination is valid for the given runtime.
func ValidateEnvironment(runtime, environment, environmentImage string) error {
	return validateEnvironment(runtime, environment, environmentImage, nil)
}

// validateEnvironment resolves sandbox-exec against baseEnv's PATH.
func validateEnvironment(runtime, environment, environmentImage string, baseEnv []string) error {
	if environment == "docker" && environmentImage == "" {
		return fmt.Errorf("docker environment requires an image")
	}
	if strings.HasPrefix(environmentImage, "-") {
		return fmt.Errorf("invalid docker image %q: must not start with '-'", environmentImage)
	}
	if environment == "sandbox" && environmentImage != "" {
		return fmt.Errorf("sandbox and docker environments cannot be used together")
	}
	switch environment {
	case "", "host", "docker":
		return nil
	case "sandbox":
		switch adapter.NormalizeChatRuntime(runtime) {
		case "pi", "opencode", "shell":
			if _, err := envpkg.LookPath("sandbox-exec", baseEnv); err != nil {
				return fmt.Errorf("sandbox-exec not found in PATH: %w", err)
			}
		case "codex", "gemini", "claude":
			// codex/gemini use native sandbox flags; claude uses --settings JSON
		default:
			return fmt.Errorf("%s is not supported with the sandbox environment", runtime)
		}
		return nil
	default:
		return fmt.Errorf("unsupported environment %q", environment)
	}
}
