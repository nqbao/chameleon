package adapter

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"github.com/nqbao/chameleon/environment"
)

type CodexRuntime struct{}

func (a *CodexRuntime) Name() string        { return "codex" }
func (a *CodexRuntime) DisplayName() string { return "Codex" }

func (a *CodexRuntime) StartSession() (string, error)               { return "", nil }
func (a *CodexRuntime) Credentials() environment.CredentialProvider { return CodexCredentials }
func (a *CodexRuntime) SetupCmd() []string                          { return []string{"codex", "login"} }

func (a *CodexRuntime) BuildCommand(ctx AgentCommandContext) (AgentCommand, error) {
	// ctx.Tools is intentionally not wired: codex exec has no tool filtering flag
	// as of the time of writing.
	if ctx.Interactive {
		var args []string
		if ctx.SessionID != "" {
			args = append(args, "--session", ctx.SessionID)
		}
		if ctx.Model != "" {
			args = append(args, "--model", ctx.Model)
		}
		if ctx.Sandbox {
			args = append(args, "--sandbox", "workspace-write")
		}
		if ctx.Yolo {
			args = append(args, "--dangerously-bypass-approvals-and-sandbox")
		}
		// Codex has no system-prompt flag; pass it as the initial user message instead.
		if ctx.SystemPrompt != "" {
			args = append(args, ctx.SystemPrompt)
		}
		return AgentCommand{Name: "codex", Args: args}, nil
	}
	args := []string{"exec"}
	if ctx.Model != "" {
		args = append(args, "--model", ctx.Model)
	}
	for _, p := range ctx.Images {
		args = append(args, "--image", p)
	}
	if ctx.Sandbox {
		args = append(args, "--sandbox", "workspace-write")
	}
	if ctx.SessionID == "" {
		// Codex has no system-prompt flag; prepend to the user prompt on new sessions only.
		prompt := ctx.Prompt
		if ctx.SystemPrompt != "" {
			prompt = ctx.SystemPrompt + "\n\n" + prompt
		}
		if len(ctx.Files) > 0 {
			refs := make([]string, 0, len(ctx.Files))
			for _, p := range ctx.Files {
				refs = append(refs, "File: "+p)
			}
			if prompt != "" {
				prompt += "\n\n"
			}
			prompt += "Attached file paths:\n" + strings.Join(refs, "\n")
		}
		if ctx.Yolo {
			args = append(args, "--dangerously-bypass-approvals-and-sandbox")
		}
		args = append(args, "--cd", ctx.WorkspaceDir, "--color", "never", "--json", prompt)
	} else {
		prompt := ctx.Prompt
		if len(ctx.Files) > 0 {
			refs := make([]string, 0, len(ctx.Files))
			for _, p := range ctx.Files {
				refs = append(refs, "File: "+p)
			}
			if prompt != "" {
				prompt += "\n\n"
			}
			prompt += "Attached file paths:\n" + strings.Join(refs, "\n")
		}
		args = append(args, "--color", "never", "--json", "resume")
		if ctx.Yolo {
			args = append(args, "--dangerously-bypass-approvals-and-sandbox")
		}
		args = append(args, ctx.SessionID, prompt)
	}
	cmd := AgentCommand{
		Name: "codex",
		Args: args,
		Env:  []string{"NO_COLOR=1", "TERM=dumb"},
	}
	if ctx.JSONSchema != "" {
		schemaFile, err := os.CreateTemp("", "cham-schema-*.json")
		if err != nil {
			return AgentCommand{}, fmt.Errorf("create schema file: %w", err)
		}
		if _, err := schemaFile.WriteString(ctx.JSONSchema); err != nil {
			os.Remove(schemaFile.Name())
			return AgentCommand{}, fmt.Errorf("write schema file: %w", err)
		}
		schemaFile.Close()

		outputFile, err := os.CreateTemp("", "cham-output-*.json")
		if err != nil {
			os.Remove(schemaFile.Name())
			return AgentCommand{}, fmt.Errorf("create output file: %w", err)
		}
		outputFile.Close()

		cmd.Args = append(cmd.Args, "--output-schema", schemaFile.Name(), "--output-file", outputFile.Name())
		cmd.OutputFile = outputFile.Name()
		cmd.TempFiles = []string{schemaFile.Name(), outputFile.Name()}
		cmd.TempMounts = []string{
			schemaFile.Name() + ":" + schemaFile.Name() + ":ro",
			outputFile.Name() + ":" + outputFile.Name(),
		}
	}
	return cmd, nil
}

func (a *CodexRuntime) ParseOutput(output []byte) (AgentResult, error) {
	var result AgentResult
	var content strings.Builder
	for _, line := range strings.Split(string(output), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		var event struct {
			Type     string `json:"type"`
			ThreadID string `json:"thread_id"`
			Item     *struct {
				Type    string `json:"type"`
				Text    string `json:"text"`
				Command string `json:"command"`
				Tool    string `json:"tool"`
				Query   string `json:"query"`
				Changes []struct {
					Path string `json:"path"`
					Kind string `json:"kind"`
				} `json:"changes"`
			} `json:"item"`
		}
		if err := json.Unmarshal([]byte(line), &event); err != nil {
			continue
		}
		if event.Type == "thread.started" && event.ThreadID != "" {
			result.SessionID = event.ThreadID
		}
		if (event.Type == "item.completed" || event.Type == "item.started") && event.Item != nil {
			switch event.Item.Type {
			case "agent_message":
				if event.Type == "item.completed" {
					if content.Len() > 0 {
						content.WriteString("\n")
					}
					content.WriteString(event.Item.Text)
				}
			case "command_execution":
				if event.Type == "item.completed" {
					inp, _ := json.Marshal(map[string]string{"command": event.Item.Command})
					result.ToolCalls = append(result.ToolCalls, ToolCall{Name: "Bash", Input: inp})
				}
			case "file_change":
				if event.Type == "item.completed" {
					inp, _ := json.Marshal(event.Item.Changes)
					result.ToolCalls = append(result.ToolCalls, ToolCall{Name: "FileChange", Input: inp})
				}
			case "mcp_tool_call":
				if event.Type == "item.completed" {
					result.ToolCalls = append(result.ToolCalls, ToolCall{Name: event.Item.Tool})
				}
			case "web_search":
				if event.Type == "item.completed" {
					inp, _ := json.Marshal(map[string]string{"query": event.Item.Query})
					result.ToolCalls = append(result.ToolCalls, ToolCall{Name: "WebSearch", Input: inp})
				}
			}
		}
	}
	result.Content = content.String()
	if result.Content == "" {
		result.Content = normalizeToolOutput(string(output))
	}
	return result, nil
}

func (a *CodexRuntime) ParseStreamLine(line []byte) []StreamEvent {
	var event struct {
		Type string `json:"type"`
		Item *struct {
			Type    string `json:"type"`
			Text    string `json:"text"`
			Command string `json:"command"`
			Tool    string `json:"tool"`
			Query   string `json:"query"`
		} `json:"item"`
	}
	if json.Unmarshal(line, &event) != nil || event.Item == nil {
		return nil
	}
	switch event.Type {
	case "item.completed":
		switch event.Item.Type {
		case "agent_message":
			if event.Item.Text != "" {
				return []StreamEvent{{Type: "chunk", Content: event.Item.Text}, {Type: "message", Content: event.Item.Text}}
			}
		case "mcp_tool_call":
			return []StreamEvent{{Type: "toolCall", Name: event.Item.Tool}}
		case "web_search":
			inp, _ := json.Marshal(map[string]string{"query": event.Item.Query})
			return []StreamEvent{{Type: "toolCall", Name: "WebSearch", Detail: toolDetail("WebSearch", inp)}}
		}
	case "item.started":
		if event.Item.Type == "command_execution" {
			inp, _ := json.Marshal(map[string]string{"command": event.Item.Command})
			return []StreamEvent{{Type: "toolCall", Name: "Bash", Detail: toolDetail("Bash", inp)}}
		}
	}
	return nil
}
