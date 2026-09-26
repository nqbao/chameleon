package adapter

import (
	"strings"

	"github.com/nqbao/chameleon/environment"
)

// Built-in credential providers.
var (
	ClaudeCredentials   environment.CredentialProvider = &claudeCredProvider{}
	CodexCredentials    environment.CredentialProvider = &codexCredProvider{}
	OpenCodeCredentials environment.CredentialProvider = &opencodeCredProvider{}
	GeminiCredentials   environment.CredentialProvider = &geminiCredProvider{}
	PiCredentials       environment.CredentialProvider = &piCredProvider{}
)

// AllCredentialProviders is the full registry of built-in credential providers.
var AllCredentialProviders = []environment.CredentialProvider{
	ClaudeCredentials,
	CodexCredentials,
	OpenCodeCredentials,
	GeminiCredentials,
	PiCredentials,
}

// CredentialProvidersByName resolves a list of credential names to providers.
// "all" expands to AllCredentialProviders; unknown names are silently skipped.
func CredentialProvidersByName(names []string) []environment.CredentialProvider {
	var out []environment.CredentialProvider
	seen := map[string]bool{}
	add := func(p environment.CredentialProvider) {
		if !seen[p.Name()] {
			seen[p.Name()] = true
			out = append(out, p)
		}
	}
	for _, name := range names {
		switch strings.ToLower(strings.TrimSpace(name)) {
		case "all":
			for _, p := range AllCredentialProviders {
				add(p)
			}
		case "claude":
			add(ClaudeCredentials)
		case "codex":
			add(CodexCredentials)
		case "opencode":
			add(OpenCodeCredentials)
		case "pi":
			add(PiCredentials)
		case "gemini":
			add(GeminiCredentials)
		}
	}
	return out
}

type claudeCredProvider struct{}

func (p *claudeCredProvider) Name() string { return "claude" }
func (p *claudeCredProvider) Dirs() []environment.CredDir {
	return []environment.CredDir{
		{HomeRelPath: ".claude", DockerRelPath: "claude", IsDir: true},
		{HomeRelPath: ".claude.json", DockerRelPath: "claude.json", IsDir: false},
	}
}
func (p *claudeCredProvider) EnvVars() []string { return []string{"ANTHROPIC_API_KEY"} }

type codexCredProvider struct{}

func (p *codexCredProvider) Name() string { return "codex" }
func (p *codexCredProvider) Dirs() []environment.CredDir {
	return []environment.CredDir{{HomeRelPath: ".codex", DockerRelPath: "codex", IsDir: true}}
}
func (p *codexCredProvider) EnvVars() []string { return []string{"OPENAI_API_KEY"} }

type opencodeCredProvider struct{}

func (p *opencodeCredProvider) Name() string { return "opencode" }
func (p *opencodeCredProvider) Dirs() []environment.CredDir {
	return []environment.CredDir{
		{HomeRelPath: ".config/opencode", DockerRelPath: "config", IsDir: true},
		{HomeRelPath: ".local/share/opencode", DockerRelPath: "share", IsDir: true},
	}
}
func (p *opencodeCredProvider) EnvVars() []string {
	return []string{"OPENAI_API_KEY", "ANTHROPIC_API_KEY"}
}

type geminiCredProvider struct{}

func (p *geminiCredProvider) Name() string { return "gemini" }
func (p *geminiCredProvider) Dirs() []environment.CredDir {
	return []environment.CredDir{{HomeRelPath: ".gemini", DockerRelPath: "gemini", IsDir: true}}
}
func (p *geminiCredProvider) EnvVars() []string { return []string{"GEMINI_API_KEY", "GOOGLE_API_KEY"} }

type piCredProvider struct{}

func (p *piCredProvider) Name() string { return "pi" }
func (p *piCredProvider) Dirs() []environment.CredDir {
	return []environment.CredDir{{HomeRelPath: ".pi/agent", DockerRelPath: ".", IsDir: true}}
}
func (p *piCredProvider) EnvVars() []string {
	return []string{
		"ANTHROPIC_API_KEY", "OPENAI_API_KEY", "GEMINI_API_KEY",
		"DEEPSEEK_API_KEY", "GROQ_API_KEY", "XAI_API_KEY",
		"OPENROUTER_API_KEY", "MISTRAL_API_KEY", "TOGETHER_API_KEY",
		"FIREWORKS_API_KEY", "CEREBRAS_API_KEY", "HF_TOKEN",
		"AI_GATEWAY_API_KEY",
		"AWS_PROFILE", "AWS_ACCESS_KEY_ID", "AWS_SECRET_ACCESS_KEY",
		"AZURE_OPENAI_API_KEY", "CLOUDFLARE_API_KEY",
		"GOOGLE_CLOUD_PROJECT", "GOOGLE_CLOUD_LOCATION",
		"GOOGLE_APPLICATION_CREDENTIALS",
		"PI_CODING_AGENT_DIR",
	}
}
