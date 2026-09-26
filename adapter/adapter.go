package adapter

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/nqbao/chameleon/environment"
)

// AgentRuntime is the interface each agent implementation must satisfy.
type AgentRuntime interface {
	Name() string
	DisplayName() string
	// StartSession initializes a session with the underlying agent and returns
	// its session ID. For runtimes that need no external initialization (e.g.
	// Claude), this generates a local ID. The returned ID is passed to
	// subsequent BuildCommand calls. Runtimes with no session concept return "".
	StartSession() (string, error)
	BuildCommand(ctx AgentCommandContext) (AgentCommand, error)
	ParseOutput(output []byte) (AgentResult, error)
	ParseStreamLine(line []byte) []StreamEvent
	Credentials() environment.CredentialProvider
	// SetupCmd returns the command to run inside a docker container to set up
	// credentials (e.g. login). Returns nil if no dedicated setup command exists.
	SetupCmd() []string
}

// DockerMountProvider is an optional interface that runtimes can implement to
// add extra host→container file mounts during Docker runs.
type DockerMountProvider interface {
	DockerMountFiles() []string
}

type AgentCommandContext struct {
	Prompt       string
	SystemPrompt string
	WorkspaceDir string
	Files        []string
	Images       []string
	SessionID    string
	Model        string
	Yolo         bool
	Interactive  bool
	Sandbox      bool
	ExtraArgs    []string
	// Tools is an allowlist of tool names available to the agent. Empty means all tools.
	// Supported: Claude (--allowedTools), Pi (--tools), Gemini (--allowed-tools, deprecated flag).
	// Not supported by Codex or OpenCode (no per-invocation tool filtering available).
	Tools            []string
	ProviderSettings map[string]interface{} // merged into --settings JSON for runtimes that support it (claude)
	OutputFormat     string                 // desired output format ("text", "json", ...); empty means runtime default (stream-json for claude)
	JSONSchema       string                 // raw JSON schema string; if set, requests structured output matching this schema
}

type AgentCommand struct {
	Name       string
	Args       []string
	Env        []string
	UnsetEnv   []string // env var names to strip from the inherited environment
	OutputFile string   // if non-empty, read structured output from this file after exec (Codex)
	TempFiles  []string // temporary files to remove after exec
	TempMounts []string // extra Docker volume mounts for temp files; "hostPath:containerPath" format
}

type ToolCall struct {
	Name   string          `json:"name"`
	Input  json.RawMessage `json:"input,omitempty"`  // raw input, persisted to DB
	Detail string          `json:"detail,omitempty"` // computed at load time, not stored
}

type AgentResult struct {
	Content   string
	SessionID string
	IsError   bool
	ToolCalls []ToolCall
}

type StreamEvent struct {
	Type    string // "chunk", "message", "toolCall", "thinking"
	Content string
	Name    string
	Detail  string
}

func AgentRuntimeFor(runtime string) (AgentRuntime, error) {
	switch normalizeChatRuntime(runtime) {
	case "claude":
		return &ClaudeRuntime{}, nil
	case "codex":
		return &CodexRuntime{}, nil
	case "opencode":
		return &OpenCodeRuntime{}, nil
	case "gemini":
		return &GeminiRuntime{}, nil
	case "pi":
		return &PiRuntime{}, nil
	case "shell":
		return &ShellRuntime{}, nil
	default:
		return nil, fmt.Errorf("unsupported runtime %q", runtime)
	}
}

// toolDetail extracts a human-readable detail string from a tool's raw JSON input.
// It tries common field names in priority order so each runtime doesn't need its own logic.
func toolDetail(name string, raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var inp map[string]json.RawMessage
	if json.Unmarshal(raw, &inp) != nil {
		return ""
	}
	str := func(keys ...string) string {
		for _, k := range keys {
			if v, ok := inp[k]; ok {
				var s string
				if json.Unmarshal(v, &s) == nil && s != "" {
					return s
				}
			}
		}
		return ""
	}
	// Task (subagent delegation)
	if name == "task" {
		if s := str("description"); s != "" {
			return s
		}
	}
	// Command (Bash, shell)
	if s := str("command"); s != "" {
		return s
	}
	// File paths → basename only
	if p := str("file_path", "filePath", "notebook_path"); p != "" {
		return filepath.Base(p)
	}
	// Search query / pattern
	if s := str("query", "pattern"); s != "" {
		return s
	}
	// URL
	if s := str("url"); s != "" {
		return s
	}
	// Directory path (LS, etc.)
	if p := str("path"); p != "" {
		return p
	}
	return ""
}

// ResolveDetails populates the Detail field for each ToolCall from its raw Input.
// Called at serve time so Detail is never persisted to the database.
func ResolveDetails(tcs []ToolCall) []ToolCall {
	out := make([]ToolCall, len(tcs))
	for i, tc := range tcs {
		tc.Detail = toolDetail(tc.Name, tc.Input)
		out[i] = tc
	}
	return out
}

func normalizeChatRuntime(r string) string {
	switch strings.ToLower(strings.TrimSpace(r)) {
	case "claude":
		return "claude"
	case "codex", "codrx":
		return "codex"
	case "opencode", "open-code", "oc":
		return "opencode"
	case "gemini":
		return "gemini"
	case "pi":
		return "pi"
	case "shell":
		return "shell"
	default:
		return strings.ToLower(strings.TrimSpace(r))
	}
}

// NormalizeChatRuntime is the exported version for use in the main package.
func NormalizeChatRuntime(r string) string {
	return normalizeChatRuntime(r)
}

func normalizeToolOutput(s string) string {
	s = strings.ReplaceAll(s, "\r\n", "\n")
	s = strings.ReplaceAll(s, "\r", "")
	return s
}

// NormalizeToolOutput is the exported version for use in the main package.
func NormalizeToolOutput(s string) string {
	return normalizeToolOutput(s)
}

func truncateString(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}

// TruncateString is the exported version for use in the main package.
func TruncateString(s string, n int) string {
	return truncateString(s, n)
}
