package adapter

import (
	"fmt"
	"strings"

	"github.com/nqbao/chameleon/environment"
)

// ShellRuntime is hidden from the frontend UI; intended for scheduled bash commands.
// The prompt is executed verbatim via "sh -c <prompt>" and raw stdout is
// returned as the assistant message content.
type ShellRuntime struct{}

func (a *ShellRuntime) Name() string                                { return "shell" }
func (a *ShellRuntime) DisplayName() string                         { return "Shell" }
func (a *ShellRuntime) StartSession() (string, error)               { return "", nil }
func (a *ShellRuntime) Credentials() environment.CredentialProvider { return nil }
func (a *ShellRuntime) SetupCmd() []string                          { return nil }
func (a *ShellRuntime) ParseStreamLine(_ []byte) []StreamEvent      { return nil }
func (a *ShellRuntime) MergeStderr() bool                           { return true }
func (a *ShellRuntime) AlwaysEmitOutput() bool                      { return true }

func (a *ShellRuntime) BuildCommand(ctx AgentCommandContext) (AgentCommand, error) {
	if ctx.JSONSchema != "" {
		return AgentCommand{}, fmt.Errorf("shell does not support --schema")
	}
	if ctx.Prompt == "" {
		return AgentCommand{}, fmt.Errorf("shell runtime: prompt (command) must not be empty")
	}
	shell := "sh"
	if ctx.Model != "" {
		shell = ctx.Model
	}
	return AgentCommand{Name: shell, Args: []string{"-c", ctx.Prompt}}, nil
}

func (a *ShellRuntime) ParseOutput(output []byte) (AgentResult, error) {
	return AgentResult{Content: strings.TrimSpace(normalizeToolOutput(string(output)))}, nil
}
