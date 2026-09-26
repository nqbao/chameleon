package adapter

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/nqbao/chameleon/environment"
)

type ClaudeRuntime struct{}

func (a *ClaudeRuntime) Name() string        { return "claude" }
func (a *ClaudeRuntime) DisplayName() string { return "Claude" }

func (a *ClaudeRuntime) StartSession() (string, error)               { return "", nil }
func (a *ClaudeRuntime) Credentials() environment.CredentialProvider { return ClaudeCredentials }
func (a *ClaudeRuntime) SetupCmd() []string                          { return []string{"claude", "login"} }

func (a *ClaudeRuntime) BuildCommand(ctx AgentCommandContext) (AgentCommand, error) {
	settingsArg, err := claudeSettingsArg(ctx.Sandbox, ctx.ProviderSettings)
	if err != nil {
		return AgentCommand{}, err
	}
	if ctx.Interactive {
		var args []string
		if ctx.SessionID != "" {
			args = append(args, "--resume", ctx.SessionID)
		}
		if ctx.Model != "" {
			args = append(args, "--model", ctx.Model)
		}
		if ctx.Yolo {
			args = append(args, "--permission-mode", "bypassPermissions")
		}
		if settingsArg != "" {
			args = append(args, "--settings", settingsArg)
		}
		if ctx.SystemPrompt != "" {
			args = append(args, "--system-prompt", ctx.SystemPrompt)
		}
		if len(ctx.Tools) > 0 {
			args = append(args, "--allowedTools", strings.Join(ctx.Tools, ","))
		}
		return AgentCommand{Name: "claude", Args: args}, nil
	}
	outFmt := ctx.OutputFormat
	if outFmt == "" {
		outFmt = "stream-json"
	}
	args := []string{"--output-format", outFmt}
	if outFmt == "stream-json" {
		args = append(args, "--verbose")
	}
	if ctx.SessionID != "" {
		args = append(args, "--resume", ctx.SessionID)
	}
	if ctx.Model != "" {
		args = append(args, "--model", ctx.Model)
	}
	if ctx.Yolo {
		args = append(args, "--permission-mode", "bypassPermissions")
	}
	if settingsArg != "" {
		args = append(args, "--settings", settingsArg)
	}
	if ctx.SystemPrompt != "" {
		args = append(args, "--system-prompt", ctx.SystemPrompt)
	}
	if ctx.JSONSchema != "" {
		args = append(args, "--json-schema", ctx.JSONSchema)
	}
	if len(ctx.Tools) > 0 {
		args = append(args, "--allowedTools", strings.Join(ctx.Tools, " "))
	}
	prompt := ctx.Prompt
	if len(ctx.Files) > 0 || len(ctx.Images) > 0 {
		var refs []string
		for _, p := range ctx.Images {
			refs = append(refs, "Image: "+p)
		}
		for _, p := range ctx.Files {
			refs = append(refs, "File: "+p)
		}
		if prompt != "" {
			prompt += "\n\n"
		}
		prompt += "Attached paths:\n" + strings.Join(refs, "\n")
	}
	args = append(args, "-p", prompt)
	return AgentCommand{Name: "claude", Args: args, Env: []string{"NO_COLOR=1", "TERM=dumb"}}, nil
}

// claudeSettingsArg builds the JSON string for --settings when sandbox or
// ProviderSettings are in use. Returns "" when neither applies.
func claudeSettingsArg(sandbox bool, extra map[string]interface{}) (string, error) {
	if !sandbox && len(extra) == 0 {
		return "", nil
	}
	settings := make(map[string]interface{}, len(extra)+1)
	for k, v := range extra {
		settings[k] = v
	}
	if sandbox {
		settings["sandbox"] = map[string]interface{}{"enabled": true}
	}
	b, err := json.Marshal(settings)
	if err != nil {
		return "", fmt.Errorf("marshal claude settings: %w", err)
	}
	return string(b), nil
}

func (a *ClaudeRuntime) ParseOutput(output []byte) (AgentResult, error) {
	var result AgentResult
	var text strings.Builder
	for _, line := range strings.Split(string(output), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		var event struct {
			Type      string `json:"type"`
			Subtype   string `json:"subtype"`
			SessionID string `json:"session_id"`
			IsError   bool   `json:"is_error"`
			Result    string `json:"result"`
			Message   *struct {
				Content []struct {
					Type  string          `json:"type"`
					Text  string          `json:"text"`
					Name  string          `json:"name"`
					Input json.RawMessage `json:"input"` // only used for detail extraction
				} `json:"content"`
				Error string `json:"error"`
			} `json:"message"`
		}
		if err := json.Unmarshal([]byte(line), &event); err != nil {
			continue
		}
		if event.Type == "system" && event.Subtype == "init" && event.SessionID != "" {
			result.SessionID = event.SessionID
		}
		if event.Type == "result" {
			if event.IsError {
				result.IsError = true
			}
			if result.Content == "" && event.Result != "" {
				result.Content = event.Result
			}
		}
		if event.Type == "assistant" && event.Message != nil {
			for _, part := range event.Message.Content {
				switch part.Type {
				case "text":
					if text.Len() > 0 {
						text.WriteString("\n")
					}
					text.WriteString(part.Text)
				case "tool_use":
					result.ToolCalls = append(result.ToolCalls, ToolCall{Name: part.Name, Input: part.Input})
				}
			}
		}
	}
	if result.Content == "" {
		result.Content = strings.TrimSpace(text.String())
	}
	if result.Content == "" {
		result.Content = normalizeToolOutput(string(output))
	}
	return result, nil
}

func (a *ClaudeRuntime) ParseStreamLine(line []byte) []StreamEvent {
	var event struct {
		Type    string `json:"type"`
		Message *struct {
			Content []struct {
				Type     string          `json:"type"`
				Text     string          `json:"text"`
				Thinking string          `json:"thinking"`
				Name     string          `json:"name"`
				Input    json.RawMessage `json:"input"`
			} `json:"content"`
		} `json:"message"`
	}
	if json.Unmarshal(line, &event) != nil || event.Type != "assistant" || event.Message == nil {
		return nil
	}
	var events []StreamEvent
	var textParts []string
	flushMessage := func() {
		if len(textParts) == 0 {
			return
		}
		events = append(events, StreamEvent{Type: "message", Content: strings.Join(textParts, "")})
		textParts = nil
	}
	for _, part := range event.Message.Content {
		switch part.Type {
		case "text":
			if part.Text != "" {
				textParts = append(textParts, part.Text)
				events = append(events, StreamEvent{Type: "chunk", Content: part.Text})
			}
		case "thinking":
			if part.Thinking != "" {
				events = append(events, StreamEvent{Type: "thinking", Content: part.Thinking})
			}
		case "tool_use":
			flushMessage()
			events = append(events, StreamEvent{Type: "toolCall", Name: part.Name, Detail: toolDetail(part.Name, part.Input)})
		}
	}
	flushMessage()
	return events
}
