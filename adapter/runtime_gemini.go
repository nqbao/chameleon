package adapter

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/nqbao/chameleon/environment"
)

type GeminiRuntime struct{}

func (a *GeminiRuntime) Name() string        { return "gemini" }
func (a *GeminiRuntime) DisplayName() string { return "Gemini" }

func (a *GeminiRuntime) StartSession() (string, error)               { return "", nil }
func (a *GeminiRuntime) Credentials() environment.CredentialProvider { return GeminiCredentials }
func (a *GeminiRuntime) SetupCmd() []string                          { return []string{"gemini"} }

func (a *GeminiRuntime) BuildCommand(ctx AgentCommandContext) (AgentCommand, error) {
	if ctx.JSONSchema != "" && !ctx.Interactive {
		return AgentCommand{}, fmt.Errorf("gemini does not support --schema")
	}
	if ctx.Interactive {
		var args []string
		if ctx.SessionID != "" {
			args = append(args, "--resume", ctx.SessionID)
		}
		if ctx.Model != "" {
			args = append(args, "--model", ctx.Model)
		}
		if ctx.Sandbox {
			args = append(args, "--sandbox")
		}
		if ctx.Yolo {
			args = append(args, "--yolo")
		}
		if ctx.SystemPrompt != "" {
			args = append(args, "--system-prompt", ctx.SystemPrompt)
		}
		if len(ctx.Tools) > 0 {
			args = append(args, "--allowed-tools")
			args = append(args, ctx.Tools...)
		}
		return AgentCommand{Name: "gemini", Args: args}, nil
	}
	args := []string{"--output-format", "stream-json", "--prompt", ctx.Prompt}
	for _, p := range ctx.Images {
		args = append(args, "--image", p)
	}
	for _, p := range ctx.Files {
		args = append(args, "--file", p)
	}
	if ctx.SessionID != "" {
		args = append(args, "--resume", ctx.SessionID)
	}
	if ctx.Model != "" {
		args = append(args, "--model", ctx.Model)
	}
	if ctx.Yolo {
		args = append(args, "--yolo")
	}
	if ctx.Sandbox {
		args = append(args, "--sandbox")
	}
	if ctx.SystemPrompt != "" {
		args = append(args, "--system-prompt", ctx.SystemPrompt)
	}
	if len(ctx.Tools) > 0 {
		args = append(args, "--allowed-tools")
		args = append(args, ctx.Tools...)
	}
	return AgentCommand{
		Name: "gemini",
		Args: args,
		Env:  []string{"NO_COLOR=1", "TERM=dumb"},
	}, nil
}

// geminiNonJSONLines returns only the non-JSON lines from raw Gemini output,
// used as a fallback when no assistant message was parsed (e.g. the process
// was killed mid-run). This surfaces retry warnings and error messages while
// discarding the raw JSON event lines that would be noise to the user.
func geminiNonJSONLines(output string) string {
	var b strings.Builder
	for _, line := range strings.Split(output, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "{") || strings.HasPrefix(line, "Warning:") ||
			strings.HasPrefix(line, "YOLO mode") || strings.HasPrefix(line, "Ripgrep is not available") {
			continue
		}
		if b.Len() > 0 {
			b.WriteString("\n")
		}
		b.WriteString(line)
	}
	return strings.TrimSpace(b.String())
}

// geminiSplitThought detects Gemini CLI thought events. toPart2() in the CLI
// serializes thought API parts as "text\n[Thought: true]" (non-empty thought)
// or "[Thought: true]" (empty thought text). Returns the text and whether it
// is a thinking event.
func geminiSplitThought(content string) (text string, isThought bool) {
	if strings.HasSuffix(content, "\n[Thought: true]") {
		return strings.TrimSuffix(content, "\n[Thought: true]"), true
	}
	if content == "[Thought: true]" {
		return "", true
	}
	return content, false
}

func (a *GeminiRuntime) ParseOutput(output []byte) (AgentResult, error) {
	var result AgentResult
	var response strings.Builder
	var lastThought string
	for _, line := range strings.Split(string(output), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		var event struct {
			Type       string          `json:"type"`
			Role       string          `json:"role"`
			Content    string          `json:"content"`
			SessionID  string          `json:"session_id"`
			ToolName   string          `json:"tool_name"`
			Parameters json.RawMessage `json:"parameters"`
		}
		if err := json.Unmarshal([]byte(line), &event); err != nil {
			continue
		}
		if event.Type == "init" && event.SessionID != "" {
			result.SessionID = event.SessionID
		}
		if event.Type == "message" && event.Role == "assistant" && event.Content != "" {
			text, isThought := geminiSplitThought(event.Content)
			if isThought {
				lastThought = text
			} else {
				response.WriteString(text)
			}
		}
		if event.Type == "tool_use" && event.ToolName != "" {
			result.ToolCalls = append(result.ToolCalls, ToolCall{Name: event.ToolName, Input: event.Parameters})
		}
	}
	result.Content = strings.TrimSpace(response.String())
	if result.Content == "" && lastThought != "" {
		// All events were thought events; the last one is closest to a
		// user-facing reply, so surface it as the response.
		result.Content = strings.TrimSpace(lastThought)
	}
	if result.Content == "" {
		result.Content = geminiNonJSONLines(string(output))
		if result.Content != "" {
			result.IsError = true
		}
	}
	return result, nil
}

func (a *GeminiRuntime) ParseStreamLine(line []byte) []StreamEvent {
	var event struct {
		Type       string          `json:"type"`
		Role       string          `json:"role"`
		Content    string          `json:"content"`
		ToolName   string          `json:"tool_name"`
		Parameters json.RawMessage `json:"parameters"`
	}
	if json.Unmarshal(line, &event) != nil {
		return nil
	}
	if event.Type == "message" && event.Role == "assistant" && event.Content != "" {
		text, isThought := geminiSplitThought(event.Content)
		if isThought {
			return []StreamEvent{{Type: "thinking", Content: text}}
		}
		return []StreamEvent{{Type: "chunk", Content: text}}
	}
	if event.Type == "tool_use" && event.ToolName != "" {
		return []StreamEvent{{Type: "toolCall", Name: event.ToolName, Detail: toolDetail(event.ToolName, event.Parameters)}}
	}
	return nil
}
