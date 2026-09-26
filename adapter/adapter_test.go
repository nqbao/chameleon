package adapter

import (
	"encoding/json"
	"reflect"
	"testing"
)

func TestAgentRuntimeFor(t *testing.T) {
	for input, want := range map[string]string{
		"claude": "claude", " Claude ": "claude", "codex": "codex", "oc": "opencode",
		"open-code": "opencode", "gemini": "gemini", "pi": "pi", "shell": "shell",
	} {
		rt, err := AgentRuntimeFor(input)
		if err != nil {
			t.Fatalf("%q: %v", input, err)
		}
		if rt.Name() != want {
			t.Errorf("%q: got %q, want %q", input, rt.Name(), want)
		}
	}
	if _, err := AgentRuntimeFor("nope"); err == nil {
		t.Fatal("unknown runtime must fail")
	}
}

func TestToolDetail(t *testing.T) {
	for _, tc := range []struct{ name, input, want string }{
		{"Bash", `{"command":"ls -la"}`, "ls -la"},
		{"Read", `{"file_path":"/a/b/c.go"}`, "c.go"},
		{"Grep", `{"pattern":"foo"}`, "foo"},
		{"WebFetch", `{"url":"https://x.test"}`, "https://x.test"},
		{"LS", `{"path":"/tmp"}`, "/tmp"},
		{"task", `{"description":"do it","command":"x"}`, "do it"},
		{"Bash", `{"command":""}`, ""},
		{"Bash", `not json`, ""},
		{"Bash", ``, ""},
	} {
		if got := toolDetail(tc.name, json.RawMessage(tc.input)); got != tc.want {
			t.Errorf("%s %s: got %q, want %q", tc.name, tc.input, got, tc.want)
		}
	}
}

func TestResolveDetailsDoesNotMutateInput(t *testing.T) {
	in := []ToolCall{{Name: "Bash", Input: json.RawMessage(`{"command":"ls"}`)}}
	out := ResolveDetails(in)
	if out[0].Detail != "ls" || in[0].Detail != "" {
		t.Fatalf("in=%+v out=%+v", in, out)
	}
}

func TestHelpers(t *testing.T) {
	if got := NormalizeToolOutput("a\r\nb\rc"); got != "a\nbc" {
		t.Errorf("NormalizeToolOutput = %q", got)
	}
	if TruncateString("hello", 3) != "hel" || TruncateString("hi", 5) != "hi" {
		t.Error("TruncateString")
	}
}

func TestClaudeParseOutput(t *testing.T) {
	out := `{"type":"system","subtype":"init","session_id":"s1"}
garbage line
{"type":"assistant","message":{"content":[{"type":"text","text":"working"},{"type":"tool_use","name":"Bash","input":{"command":"ls"}}]}}
{"type":"result","is_error":false,"result":"done"}
`
	res, err := (&ClaudeRuntime{}).ParseOutput([]byte(out))
	if err != nil {
		t.Fatal(err)
	}
	if res.SessionID != "s1" || res.Content != "done" || res.IsError {
		t.Fatalf("%+v", res)
	}
	if len(res.ToolCalls) != 1 || res.ToolCalls[0].Name != "Bash" {
		t.Fatalf("tool calls: %+v", res.ToolCalls)
	}
}

func TestClaudeParseOutputFallbacks(t *testing.T) {
	rt := &ClaudeRuntime{}
	res, _ := rt.ParseOutput([]byte(`{"type":"assistant","message":{"content":[{"type":"text","text":"a"},{"type":"text","text":"b"}]}}`))
	if res.Content != "a\nb" {
		t.Errorf("assistant text fallback = %q", res.Content)
	}
	res, _ = rt.ParseOutput([]byte("plain\r\ntext"))
	if res.Content != "plain\ntext" {
		t.Errorf("raw fallback = %q", res.Content)
	}
	res, _ = rt.ParseOutput([]byte(`{"type":"result","is_error":true,"result":"boom"}`))
	if !res.IsError || res.Content != "boom" {
		t.Errorf("error result = %+v", res)
	}
}

func TestClaudeParseStreamLine(t *testing.T) {
	rt := &ClaudeRuntime{}
	line := `{"type":"assistant","message":{"content":[{"type":"thinking","thinking":"hm"},{"type":"text","text":"hi "},{"type":"tool_use","name":"Read","input":{"file_path":"/x/y.go"}},{"type":"text","text":"bye"}]}}`
	want := []StreamEvent{
		{Type: "thinking", Content: "hm"},
		{Type: "chunk", Content: "hi "},
		{Type: "message", Content: "hi "},
		{Type: "toolCall", Name: "Read", Detail: "y.go"},
		{Type: "chunk", Content: "bye"},
		{Type: "message", Content: "bye"},
	}
	if got := rt.ParseStreamLine([]byte(line)); !reflect.DeepEqual(got, want) {
		t.Fatalf("got %+v\nwant %+v", got, want)
	}
	for _, l := range []string{`{"type":"user"}`, `not json`, `{"type":"assistant"}`} {
		if got := rt.ParseStreamLine([]byte(l)); got != nil {
			t.Errorf("%q: got %+v, want nil", l, got)
		}
	}
}

func TestCodexParseOutput(t *testing.T) {
	out := `{"type":"thread.started","thread_id":"t1"}
{"type":"item.started","item":{"type":"agent_message","text":"ignored"}}
{"type":"item.completed","item":{"type":"command_execution","command":"ls"}}
{"type":"item.completed","item":{"type":"web_search","query":"go"}}
{"type":"item.completed","item":{"type":"mcp_tool_call","tool":"fetch"}}
{"type":"item.completed","item":{"type":"file_change","changes":[{"path":"a.go","kind":"update"}]}}
{"type":"item.completed","item":{"type":"agent_message","text":"one"}}
{"type":"item.completed","item":{"type":"agent_message","text":"two"}}
`
	res, err := (&CodexRuntime{}).ParseOutput([]byte(out))
	if err != nil {
		t.Fatal(err)
	}
	if res.SessionID != "t1" || res.Content != "one\ntwo" {
		t.Fatalf("%+v", res)
	}
	var names []string
	for _, tc := range res.ToolCalls {
		names = append(names, tc.Name)
	}
	if want := []string{"Bash", "WebSearch", "fetch", "FileChange"}; !reflect.DeepEqual(names, want) {
		t.Fatalf("tool names = %v, want %v", names, want)
	}
	if d := ResolveDetails(res.ToolCalls)[0].Detail; d != "ls" {
		t.Errorf("Bash detail = %q", d)
	}
}

func TestCodexParseStreamLine(t *testing.T) {
	rt := &CodexRuntime{}
	got := rt.ParseStreamLine([]byte(`{"type":"item.completed","item":{"type":"agent_message","text":"hi"}}`))
	if want := []StreamEvent{{Type: "chunk", Content: "hi"}, {Type: "message", Content: "hi"}}; !reflect.DeepEqual(got, want) {
		t.Errorf("agent_message: %+v", got)
	}
	got = rt.ParseStreamLine([]byte(`{"type":"item.started","item":{"type":"command_execution","command":"make"}}`))
	if want := []StreamEvent{{Type: "toolCall", Name: "Bash", Detail: "make"}}; !reflect.DeepEqual(got, want) {
		t.Errorf("command_execution: %+v", got)
	}
	if got := rt.ParseStreamLine([]byte(`{"type":"item.completed","item":{"type":"command_execution","command":"make"}}`)); got != nil {
		t.Errorf("completed command must not repeat the tool call: %+v", got)
	}
}

func TestShellRuntime(t *testing.T) {
	rt := &ShellRuntime{}
	cmd, err := rt.BuildCommand(AgentCommandContext{Prompt: "echo hi"})
	if err != nil || cmd.Name != "sh" || !reflect.DeepEqual(cmd.Args, []string{"-c", "echo hi"}) {
		t.Fatalf("%+v %v", cmd, err)
	}
	if cmd, _ = rt.BuildCommand(AgentCommandContext{Prompt: "x", Model: "bash"}); cmd.Name != "bash" {
		t.Errorf("model must select the shell, got %q", cmd.Name)
	}
	if _, err := rt.BuildCommand(AgentCommandContext{}); err == nil {
		t.Error("empty prompt must fail")
	}
	if _, err := rt.BuildCommand(AgentCommandContext{Prompt: "x", JSONSchema: "{}"}); err == nil {
		t.Error("schema must be rejected")
	}
	res, _ := rt.ParseOutput([]byte("  out\r\n"))
	if res.Content != "out" {
		t.Errorf("ParseOutput = %q", res.Content)
	}
}

func TestCredentialProvidersByName(t *testing.T) {
	got := CredentialProvidersByName([]string{" Claude ", "codex", "claude", "bogus"})
	if len(got) != 2 || got[0].Name() != "claude" || got[1].Name() != "codex" {
		t.Fatalf("got %v", got)
	}
	if all := CredentialProvidersByName([]string{"all"}); len(all) != len(AllCredentialProviders) {
		t.Fatalf("all = %d providers, want %d", len(all), len(AllCredentialProviders))
	}
	if len(CredentialProvidersByName(nil)) != 0 {
		t.Error("nil names must yield no providers")
	}
}
