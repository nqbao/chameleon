package adapter

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

const mcpFixture = `{"mcpServers": {
  "fs": {"command": "npx", "args": ["-y", "server-fs", "/work"], "env": {"TOKEN": "${TOKEN}", "MODE": "ro"}},
  "docs": {"url": "https://example.com/mcp", "headers": {"Authorization": "Bearer ${DOCS_KEY}", "X-Region": "us"}}
}}`

func mustParseMCP(t *testing.T, doc string) map[string]MCPServer {
	t.Helper()
	servers, err := ParseMCPConfig([]byte(doc))
	if err != nil {
		t.Fatal(err)
	}
	return servers
}

func TestParseMCPConfig(t *testing.T) {
	servers := mustParseMCP(t, mcpFixture)
	if got := servers["fs"].Transport(); got != "stdio" {
		t.Errorf("fs transport = %q", got)
	}
	if got := servers["docs"].Transport(); got != "http" {
		t.Errorf("docs transport = %q", got)
	}
	for name, doc := range map[string]string{
		"not json":      `nope`,
		"no servers":    `{"mcpServers": {}}`,
		"unknown field": `{"mcpServers": {"a": {"command": "x", "cwd": "/"}}}`,
		"both":          `{"mcpServers": {"a": {"command": "x", "url": "http://x"}}}`,
		"neither":       `{"mcpServers": {"a": {}}}`,
		"bad name":      `{"mcpServers": {"a.b": {"command": "x"}}}`,
		"bad type":      `{"mcpServers": {"a": {"command": "x", "type": "ws"}}}`,
		"env on url":    `{"mcpServers": {"a": {"url": "http://x", "env": {"A": "b"}}}}`,
	} {
		if _, err := ParseMCPConfig([]byte(doc)); err == nil {
			t.Errorf("%s: expected error", name)
		}
	}
}

func TestLoadMCPConfigsMergesLaterWins(t *testing.T) {
	dir := t.TempDir()
	a, b := filepath.Join(dir, "a.json"), filepath.Join(dir, "b.json")
	os.WriteFile(a, []byte(`{"mcpServers": {"x": {"command": "one"}, "y": {"command": "keep"}}}`), 0o600)
	os.WriteFile(b, []byte(`{"mcpServers": {"x": {"command": "two"}}}`), 0o600)
	got, err := LoadMCPConfigs([]string{a, b})
	if err != nil {
		t.Fatal(err)
	}
	if got["x"].Command != "two" || got["y"].Command != "keep" {
		t.Errorf("merged = %+v", got)
	}
	if _, err := LoadMCPConfigs([]string{filepath.Join(dir, "missing.json")}); err == nil {
		t.Error("expected error for missing file")
	}
}

func TestClaudeMCPCommand(t *testing.T) {
	servers := mustParseMCP(t, mcpFixture)
	for _, interactive := range []bool{false, true} {
		cmd, err := (&ClaudeRuntime{}).BuildCommand(AgentCommandContext{Prompt: "hi", Interactive: interactive, MCPServers: servers})
		if err != nil {
			t.Fatal(err)
		}
		if len(cmd.TempFiles) != 1 {
			t.Fatalf("interactive=%v: temp files = %v", interactive, cmd.TempFiles)
		}
		defer os.Remove(cmd.TempFiles[0])
		if cmd.Args[0] != "--mcp-config="+cmd.TempFiles[0] {
			t.Errorf("interactive=%v: args = %v", interactive, cmd.Args)
		}
		if want := cmd.TempFiles[0] + ":" + cmd.TempFiles[0] + ":ro"; !reflect.DeepEqual(cmd.TempMounts, []string{want}) {
			t.Errorf("mounts = %v", cmd.TempMounts)
		}
		data, err := os.ReadFile(cmd.TempFiles[0])
		if err != nil {
			t.Fatal(err)
		}
		var doc struct {
			MCPServers map[string]map[string]any `json:"mcpServers"`
		}
		if err := json.Unmarshal(data, &doc); err != nil {
			t.Fatal(err)
		}
		if doc.MCPServers["docs"]["type"] != "http" {
			t.Errorf("remote server type not defaulted: %v", doc.MCPServers["docs"])
		}
		if !strings.Contains(string(data), "${DOCS_KEY}") {
			t.Errorf("env references must pass through untouched: %s", data)
		}
	}
	cmd, err := (&ClaudeRuntime{}).BuildCommand(AgentCommandContext{Prompt: "hi"})
	if err != nil || len(cmd.TempFiles) != 0 || strings.Contains(strings.Join(cmd.Args, " "), "mcp-config") {
		t.Errorf("no servers: err=%v cmd=%+v", err, cmd)
	}
}

func TestCodexMCPArgs(t *testing.T) {
	got, err := codexMCPArgs(mustParseMCP(t, mcpFixture))
	if err != nil {
		t.Fatal(err)
	}
	want := []string{
		"-c", `mcp_servers.docs.url="https://example.com/mcp"`,
		"-c", `mcp_servers.docs.bearer_token_env_var="DOCS_KEY"`,
		"-c", `mcp_servers.docs.http_headers={"X-Region" = "us"}`,
		"-c", `mcp_servers.fs.command="npx"`,
		"-c", `mcp_servers.fs.args=["-y", "server-fs", "/work"]`,
		"-c", `mcp_servers.fs.env={"MODE" = "ro"}`,
		"-c", `mcp_servers.fs.env_vars=["TOKEN"]`,
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("args =\n%q\nwant\n%q", got, want)
	}
	for name, doc := range map[string]string{
		"sse":             `{"mcpServers": {"a": {"url": "http://x", "type": "sse"}}}`,
		"partial env ref": `{"mcpServers": {"a": {"command": "x", "env": {"A": "pre-${B}"}}}}`,
		"renamed env":     `{"mcpServers": {"a": {"command": "x", "env": {"A": "${B}"}}}}`,
		"partial header":  `{"mcpServers": {"a": {"url": "http://x", "headers": {"X": "k-${B}"}}}}`,
	} {
		if _, err := codexMCPArgs(mustParseMCP(t, doc)); err == nil {
			t.Errorf("%s: expected error", name)
		}
	}
}

func TestTomlString(t *testing.T) {
	if got, want := tomlString("a\"b\\c\n\x01"), `"a\"b\\c\n\u0001"`; got != want {
		t.Errorf("got %s want %s", got, want)
	}
}

func TestCodexMCPCommandPlacement(t *testing.T) {
	servers := mustParseMCP(t, `{"mcpServers": {"a": {"command": "x"}}}`)
	cmd, err := (&CodexRuntime{}).BuildCommand(AgentCommandContext{Prompt: "hi", SessionID: "s1", WorkspaceDir: "/w", MCPServers: servers})
	if err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(cmd.Args, "\x00")
	mcp, resume := strings.Index(joined, "mcp_servers.a.command"), strings.Index(joined, "resume")
	if mcp < 0 || resume < 0 || mcp > resume {
		t.Errorf("overrides must precede the resume subcommand: %q", cmd.Args)
	}
}

func TestOpenCodeMCPEnv(t *testing.T) {
	servers := mustParseMCP(t, mcpFixture)
	for _, interactive := range []bool{false, true} {
		cmd, err := (&OpenCodeRuntime{}).BuildCommand(AgentCommandContext{Prompt: "hi", Interactive: interactive, MCPServers: servers})
		if err != nil {
			t.Fatal(err)
		}
		var raw string
		for _, kv := range cmd.Env {
			if v, ok := strings.CutPrefix(kv, "OPENCODE_CONFIG_CONTENT="); ok {
				raw = v
			}
		}
		if raw == "" {
			t.Fatalf("interactive=%v: OPENCODE_CONFIG_CONTENT missing from %v", interactive, cmd.Env)
		}
		var doc struct {
			MCP map[string]struct {
				Type        string            `json:"type"`
				Command     []string          `json:"command"`
				URL         string            `json:"url"`
				Headers     map[string]string `json:"headers"`
				Environment map[string]string `json:"environment"`
			} `json:"mcp"`
		}
		if err := json.Unmarshal([]byte(raw), &doc); err != nil {
			t.Fatal(err)
		}
		fs, docs := doc.MCP["fs"], doc.MCP["docs"]
		if fs.Type != "local" || !reflect.DeepEqual(fs.Command, []string{"npx", "-y", "server-fs", "/work"}) || fs.Environment["TOKEN"] != "{env:TOKEN}" {
			t.Errorf("fs = %+v", fs)
		}
		if docs.Type != "remote" || docs.URL != "https://example.com/mcp" || docs.Headers["Authorization"] != "Bearer {env:DOCS_KEY}" {
			t.Errorf("docs = %+v", docs)
		}
	}
}

func TestMCPUnsupportedRuntimes(t *testing.T) {
	servers := mustParseMCP(t, `{"mcpServers": {"a": {"command": "x"}}}`)
	for _, rt := range []AgentRuntime{&PiRuntime{}, &ShellRuntime{}} {
		cmd, err := rt.BuildCommand(AgentCommandContext{Prompt: "true", MCPServers: servers})
		if err == nil || !strings.Contains(err.Error(), "MCP") {
			t.Errorf("%s: err = %v", rt.Name(), err)
		}
		for _, p := range cmd.TempFiles {
			os.Remove(p)
		}
	}
}
