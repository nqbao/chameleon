package adapter

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"github.com/nqbao/chameleon/environment"
)

//go:embed permissiongate.ts
var permissionGateTS []byte

type PiRuntime struct{}

func (a *PiRuntime) Name() string                                { return "pi" }
func (a *PiRuntime) DisplayName() string                         { return "Pi" }
func (a *PiRuntime) StartSession() (string, error)               { return "", nil }
func (a *PiRuntime) Credentials() environment.CredentialProvider { return PiCredentials }
func (a *PiRuntime) SetupCmd() []string                          { return []string{"pi"} }

func (a *PiRuntime) BuildCommand(ctx AgentCommandContext) (AgentCommand, error) {
	if ctx.JSONSchema != "" && !ctx.Interactive {
		return AgentCommand{}, fmt.Errorf("pi does not support --schema")
	}
	gate, err := os.CreateTemp("", "cham-pi-gate-*.ts")
	if err != nil {
		return AgentCommand{}, fmt.Errorf("pi permission gate: %w", err)
	}
	gatePath := gate.Name()
	if _, err := gate.Write(permissionGateTS); err != nil {
		_ = gate.Close()
		_ = os.Remove(gatePath)
		return AgentCommand{}, err
	}
	// The extension is not secret; it must be readable by a container user whose
	// UID differs from the host owner.
	if err := gate.Chmod(0o644); err != nil {
		_ = gate.Close()
		_ = os.Remove(gatePath)
		return AgentCommand{}, err
	}
	if err := gate.Close(); err != nil {
		_ = os.Remove(gatePath)
		return AgentCommand{}, err
	}
	tempFiles := []string{gatePath}
	tempMounts := []string{gatePath + ":" + gatePath + ":ro"}
	if ctx.Interactive {
		args := []string{"--extension", gatePath}
		if ctx.Model != "" {
			args = append(args, "--model", ctx.Model)
		}
		if ctx.SessionID != "" {
			args = append(args, "--resume", ctx.SessionID)
		}
		if ctx.SystemPrompt != "" {
			args = append(args, "--system-prompt", ctx.SystemPrompt)
		}
		if len(ctx.Tools) > 0 {
			args = append(args, "--tools", strings.Join(ctx.Tools, ","))
		}
		return AgentCommand{Name: "pi", Args: args, TempFiles: tempFiles, TempMounts: tempMounts}, nil
	}
	args := []string{"--mode", "json", "--extension", gatePath}
	if ctx.Model != "" {
		args = append(args, "--model", ctx.Model)
	}
	if ctx.SessionID != "" {
		args = append(args, "--session", ctx.SessionID)
	}
	if ctx.SystemPrompt != "" {
		args = append(args, "--system-prompt", ctx.SystemPrompt)
	}
	if len(ctx.Tools) > 0 {
		args = append(args, "--tools", strings.Join(ctx.Tools, ","))
	}
	for _, p := range ctx.Images {
		args = append(args, "@"+p)
	}
	for _, p := range ctx.Files {
		args = append(args, "@"+p)
	}
	args = append(args, ctx.Prompt)
	return AgentCommand{
		Name:      "pi",
		TempFiles: tempFiles, TempMounts: tempMounts,
		Args: args,
		Env:  []string{"NO_COLOR=1", "TERM=dumb", "PI_OFFLINE=1"},
	}, nil
}

func (a *PiRuntime) ParseOutput(output []byte) (AgentResult, error) {
	var result AgentResult
	var text strings.Builder
	var lastAgentEnd struct {
		Type     string `json:"type"`
		Messages []struct {
			Role         string `json:"role"`
			StopReason   string `json:"stopReason"`
			ErrorMessage string `json:"errorMessage"`
			Content      []struct {
				Type      string          `json:"type"`
				Text      string          `json:"text"`
				Name      string          `json:"name"`
				Arguments json.RawMessage `json:"arguments"`
			} `json:"content"`
		} `json:"messages"`
	}
	for _, line := range strings.Split(string(output), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		var event struct {
			Type string `json:"type"`
			ID   string `json:"id"`
		}
		if err := json.Unmarshal([]byte(line), &event); err != nil {
			continue
		}
		if event.Type == "session" && event.ID != "" && result.SessionID == "" {
			result.SessionID = event.ID
		}
		if event.Type == "agent_end" {
			lastAgentEnd.Type = event.Type
			if err := json.Unmarshal([]byte(line), &lastAgentEnd); err != nil {
				continue
			}
		}
	}
	for _, msg := range lastAgentEnd.Messages {
		if msg.Role == "assistant" {
			if msg.StopReason == "error" || msg.StopReason == "aborted" {
				result.IsError = true
			}
			for _, part := range msg.Content {
				if part.Type == "text" && part.Text != "" {
					if text.Len() > 0 {
						text.WriteString("\n")
					}
					text.WriteString(part.Text)
				}
				if part.Type == "toolCall" {
					result.ToolCalls = append(result.ToolCalls, ToolCall{Name: part.Name, Input: part.Arguments})
				}
			}
			if result.IsError && text.Len() == 0 && msg.ErrorMessage != "" {
				text.WriteString(msg.ErrorMessage)
			}
		}
	}
	result.Content = strings.TrimSpace(text.String())
	return result, nil
}

func (a *PiRuntime) ParseStreamLine(line []byte) []StreamEvent {
	var event struct {
		Type              string `json:"type"`
		AssistantMsgEvent *struct {
			Type  string `json:"type"`
			Delta string `json:"delta"`
		} `json:"assistantMessageEvent"`
		ToolName string          `json:"toolName"`
		Args     json.RawMessage `json:"args"`
	}
	if json.Unmarshal(line, &event) != nil {
		return nil
	}
	switch {
	case event.Type == "message_update" && event.AssistantMsgEvent != nil:
		switch event.AssistantMsgEvent.Type {
		case "text_delta":
			if event.AssistantMsgEvent.Delta != "" {
				return []StreamEvent{{Type: "chunk", Content: event.AssistantMsgEvent.Delta}}
			}
		case "thinking_delta":
			if event.AssistantMsgEvent.Delta != "" {
				return []StreamEvent{{Type: "thinking", Content: event.AssistantMsgEvent.Delta}}
			}
		}
	case event.Type == "tool_execution_start":
		return []StreamEvent{{Type: "toolCall", Name: event.ToolName, Detail: toolDetail(event.ToolName, event.Args)}}
	}
	return nil
}
