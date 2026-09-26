package adapter

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/nqbao/chameleon/environment"
)

type OpenCodeRuntime struct{}

const openCodeParseFallback = "[Opencode output is incomplete]"

func (a *OpenCodeRuntime) Name() string        { return "opencode" }
func (a *OpenCodeRuntime) DisplayName() string { return "opencode" }

func (a *OpenCodeRuntime) StartSession() (string, error)               { return "", nil }
func (a *OpenCodeRuntime) Credentials() environment.CredentialProvider { return OpenCodeCredentials }
func (a *OpenCodeRuntime) SetupCmd() []string                          { return []string{"opencode", "auth", "login"} }

func (a *OpenCodeRuntime) BuildCommand(ctx AgentCommandContext) (AgentCommand, error) {
	if ctx.JSONSchema != "" && !ctx.Interactive {
		return AgentCommand{}, fmt.Errorf("opencode does not support --schema")
	}
	// ctx.Tools is intentionally not wired: opencode run has no tool filtering flag
	// as of the time of writing.
	if ctx.Interactive {
		var args []string
		if ctx.SystemPrompt != "" {
			args = append(args, "--prompt", ctx.SystemPrompt)
		}
		if ctx.Model != "" {
			args = append(args, "--model", ctx.Model)
		}
		if ctx.SessionID != "" {
			args = append(args, "--session", ctx.SessionID)
		}
		if ctx.Yolo {
			args = append(args, "--dangerously-skip-permissions")
		}
		return AgentCommand{
			Name:     "opencode",
			Args:     args,
			UnsetEnv: []string{"OPENCODE_SERVER_PASSWORD", "OPENCODE_SERVER_USERNAME"},
		}, nil
	}
	var args []string
	if ctx.SystemPrompt != "" {
		args = append(args, "--prompt", ctx.SystemPrompt)
	}
	args = append(args, "run", "--dir", ctx.WorkspaceDir, "--format", "json")
	for _, p := range ctx.Images {
		args = append(args, "--file", p)
	}
	for _, p := range ctx.Files {
		args = append(args, "--file", p)
	}
	if ctx.Yolo {
		args = append(args, "--dangerously-skip-permissions")
	}
	if ctx.Model != "" {
		args = append(args, "--model", ctx.Model)
	}
	if ctx.SessionID != "" {
		args = append(args, "--session", ctx.SessionID)
	}
	args = append(args, ctx.Prompt)
	return AgentCommand{
		Name:     "opencode",
		Args:     args,
		Env:      []string{"NO_COLOR=1", "TERM=dumb"},
		UnsetEnv: []string{"OPENCODE_SERVER_PASSWORD", "OPENCODE_SERVER_USERNAME"},
	}, nil
}

func (a *OpenCodeRuntime) ParseOutput(output []byte) (AgentResult, error) {
	var result AgentResult
	var text strings.Builder
	var errMsg strings.Builder
	for _, line := range strings.Split(string(output), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		var event struct {
			Type      string `json:"type"`
			SessionID string `json:"sessionID"`
			Error     *struct {
				Name string `json:"name"`
				Data *struct {
					Message string `json:"message"`
				} `json:"data"`
			} `json:"error"`
			Part *struct {
				Type  string `json:"type"`
				Text  string `json:"text"`
				Tool  string `json:"tool"`
				State *struct {
					Input json.RawMessage `json:"input"`
				} `json:"state"`
			} `json:"part"`
		}
		if err := json.Unmarshal([]byte(line), &event); err != nil {
			continue
		}
		if result.SessionID == "" && event.SessionID != "" {
			result.SessionID = event.SessionID
		}
		if event.Type == "error" {
			result.IsError = true
			if event.Error != nil {
				msg := event.Error.Name
				if event.Error.Data != nil && event.Error.Data.Message != "" {
					msg = event.Error.Data.Message
				}
				if errMsg.Len() > 0 {
					errMsg.WriteString("\n")
				}
				errMsg.WriteString(msg)
			}
			continue
		}
		if event.Type == "text" && event.Part != nil && event.Part.Type == "text" {
			if text.Len() > 0 {
				text.WriteString("\n")
			}
			text.WriteString(event.Part.Text)
		}
		if event.Type == "tool_use" && event.Part != nil {
			tc := ToolCall{Name: event.Part.Tool}
			if event.Part.State != nil {
				tc.Input = event.Part.State.Input
			}
			result.ToolCalls = append(result.ToolCalls, tc)
		}
	}
	result.Content = strings.TrimSpace(text.String())
	if result.IsError && result.Content == "" {
		result.Content = strings.TrimSpace(errMsg.String())
	}
	if result.Content == "" {
		if result.IsError {
			result.Content = normalizeToolOutput(string(output))
		} else {
			// Known issue: opencode can exit 0 while stdout never includes the final
			// assistant text, leaving only intermediate protocol events/tool steps.
			result.Content = openCodeParseFallback
		}
	}
	return result, nil
}

func (a *OpenCodeRuntime) ParseStreamLine(line []byte) []StreamEvent {
	var event struct {
		Type string `json:"type"`
		Part *struct {
			Type  string `json:"type"`
			Text  string `json:"text"`
			Tool  string `json:"tool"`
			State *struct {
				Input json.RawMessage `json:"input"`
			} `json:"state"`
		} `json:"part"`
	}
	if json.Unmarshal(line, &event) != nil || event.Part == nil {
		return nil
	}
	switch event.Type {
	case "text":
		if event.Part.Type == "text" && event.Part.Text != "" {
			return []StreamEvent{{Type: "chunk", Content: event.Part.Text}}
		}
	case "tool_use":
		ev := StreamEvent{Type: "toolCall", Name: event.Part.Tool}
		if event.Part.State != nil {
			ev.Detail = toolDetail(event.Part.Tool, event.Part.State.Input)
		}
		return []StreamEvent{ev}
	}
	return nil
}
