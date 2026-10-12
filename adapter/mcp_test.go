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
		if len(cmd.TempFiles) != 2 || filepath.Dir(cmd.TempFiles[0]) != cmd.TempFiles[1] {
			t.Fatalf("interactive=%v: want the file then its directory, got %v", interactive, cmd.TempFiles)
		}
		defer func() {
			os.Remove(cmd.TempFiles[0])
			os.Remove(cmd.TempFiles[1])
		}()
		assertPrivateMCPFile(t, cmd.TempFiles[0], cmd.TempFiles[1])
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
	for _, rt := range []AgentRuntime{&ShellRuntime{}} {
		cmd, err := rt.BuildCommand(AgentCommandContext{Prompt: "true", MCPServers: servers})
		if err == nil || !strings.Contains(err.Error(), "MCP") {
			t.Errorf("%s: err = %v", rt.Name(), err)
		}
		for _, p := range cmd.TempFiles {
			os.Remove(p)
		}
	}
}

func TestPiMCPCommand(t *testing.T) {
	servers := mustParseMCP(t, mcpFixture)
	for _, interactive := range []bool{false, true} {
		cmd, err := (&PiRuntime{}).BuildCommand(AgentCommandContext{Prompt: "hi", Interactive: interactive, MCPServers: servers})
		if err != nil {
			t.Fatal(err)
		}
		defer func() {
			for _, p := range cmd.TempFiles {
				os.Remove(p)
			}
		}()
		if len(cmd.TempFiles) != 3 || len(cmd.TempMounts) != 2 {
			t.Fatalf("interactive=%v: temp files = %v mounts = %v", interactive, cmd.TempFiles, cmd.TempMounts)
		}
		mcpPath := cmd.TempFiles[1]
		assertPrivateMCPFile(t, mcpPath, cmd.TempFiles[2])
		var exts []string
		for i, a := range cmd.Args {
			if a == "--extension" {
				exts = append(exts, cmd.Args[i+1])
			}
		}
		if !reflect.DeepEqual(exts, cmd.TempFiles[:2]) {
			t.Errorf("--extension args = %v, want %v", exts, cmd.TempFiles[:2])
		}
		src, _ := os.ReadFile(mcpPath)
		for _, want := range []string{`"exposure":"direct"`, `"command":"npx"`, `"TOKEN":"${TOKEN}"`, `"url":"https://example.com/mcp"`, "registerMcpServer"} {
			if !strings.Contains(string(src), want) {
				t.Errorf("extension missing %s:\n%s", want, src)
			}
		}
	}
	cmd, err := (&PiRuntime{}).BuildCommand(AgentCommandContext{Prompt: "hi"})
	if err != nil || len(cmd.TempFiles) != 1 {
		t.Errorf("no servers: err=%v temp=%v", err, cmd.TempFiles)
	}
	for _, p := range cmd.TempFiles {
		os.Remove(p)
	}
	sse := mustParseMCP(t, `{"mcpServers": {"a": {"url": "http://x", "type": "sse"}}}`)
	if _, err := (&PiRuntime{}).BuildCommand(AgentCommandContext{Prompt: "hi", MCPServers: sse}); err == nil {
		t.Error("sse: expected error")
	}
}

// assertPrivateMCPFile checks the file is readable by any container user while
// the directory that holds it keeps other host users out.
func assertPrivateMCPFile(t *testing.T, file, dir string) {
	t.Helper()
	fi, err := os.Stat(file)
	if err != nil {
		t.Fatal(err)
	}
	di, err := os.Stat(dir)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != 0o644 {
		t.Errorf("file mode = %v, want 0644 so a different container UID can read it", fi.Mode().Perm())
	}
	if di.Mode().Perm() != 0o700 {
		t.Errorf("dir mode = %v, want 0700", di.Mode().Perm())
	}
}

func TestCodexMCPArgsResetStaleArrays(t *testing.T) {
	got, err := codexMCPArgs(mustParseMCP(t, `{"mcpServers": {"a": {"command": "x"}}}`))
	if err != nil {
		t.Fatal(err)
	}
	want := []string{
		"-c", `mcp_servers.a.command="x"`,
		"-c", `mcp_servers.a.args=[]`,
		"-c", `mcp_servers.a.env_vars=[]`,
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("args =\n%q\nwant\n%q", got, want)
	}
}

func TestMergeOpenCodeInline(t *testing.T) {
	gen := []byte(`{"mcp":{"fs":{"type":"local","command":["new"]}}}`)
	got, err := mergeOpenCodeInline(`{"model":"custom/model","permission":"deny","mcp":{"fs":{"type":"local","command":["old"]},"keep":{"type":"remote","url":"http://k"}}}`, gen)
	if err != nil {
		t.Fatal(err)
	}
	var doc map[string]json.RawMessage
	if err := json.Unmarshal(got, &doc); err != nil {
		t.Fatal(err)
	}
	if string(doc["model"]) != `"custom/model"` || string(doc["permission"]) != `"deny"` {
		t.Errorf("existing settings lost: %s", got)
	}
	var servers map[string]struct {
		Command []string `json:"command"`
		URL     string   `json:"url"`
	}
	if err := json.Unmarshal(doc["mcp"], &servers); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(servers["fs"].Command, []string{"new"}) || servers["keep"].URL != "http://k" {
		t.Errorf("mcp servers = %+v", servers)
	}
	if got, err := mergeOpenCodeInline("", gen); err != nil || string(got) != string(gen) {
		t.Errorf("empty existing: %s %v", got, err)
	}
	for name, bad := range map[string]string{"not json": `nope`, "array": `[]`, "null": `null`, " null ": ` null `, "mcp null": `{"mcp": null}`, "mcp not object": `{"mcp": 3}`} {
		if _, err := mergeOpenCodeInline(bad, gen); err == nil {
			t.Errorf("%s: expected error", name)
		}
	}
}

func TestOpenCodeMCPKeepsInlineConfigFromEnv(t *testing.T) {
	servers := mustParseMCP(t, `{"mcpServers": {"fs": {"command": "x"}}}`)
	cmd, err := (&OpenCodeRuntime{}).BuildCommand(AgentCommandContext{
		Prompt: "hi", MCPServers: servers,
		Env: []string{`OPENCODE_CONFIG_CONTENT={"model":"base"}`, `OPENCODE_CONFIG_CONTENT={"model":"override"}`},
	})
	if err != nil {
		t.Fatal(err)
	}
	var raw string
	for _, kv := range cmd.Env {
		if v, ok := strings.CutPrefix(kv, "OPENCODE_CONFIG_CONTENT="); ok {
			raw = v
		}
	}
	if !strings.Contains(raw, `"model":"override"`) || !strings.Contains(raw, `"fs"`) {
		t.Errorf("merged = %s", raw)
	}
	if !reflect.DeepEqual(cmd.MergedEnv, []string{"OPENCODE_CONFIG_CONTENT"}) {
		t.Errorf("MergedEnv = %v", cmd.MergedEnv)
	}
	for _, bad := range []string{"nope", "null"} {
		// Must return an error, not panic.
		if _, err := (&OpenCodeRuntime{}).BuildCommand(AgentCommandContext{Prompt: "hi", MCPServers: servers, Env: []string{"OPENCODE_CONFIG_CONTENT=" + bad}}); err == nil {
			t.Errorf("inline config %q: expected error", bad)
		}
	}
}

func TestCodexImagePlacement(t *testing.T) {
	for _, tc := range []struct{ session, want string }{
		{"", "exec --image /a.png --image /b.png"},
		{"s1", "resume --image /a.png --image /b.png s1 hi"},
	} {
		cmd, err := (&CodexRuntime{}).BuildCommand(AgentCommandContext{Prompt: "hi", SessionID: tc.session, WorkspaceDir: "/w", Images: []string{"/a.png", "/b.png"}})
		if err != nil {
			t.Fatal(err)
		}
		if joined := strings.Join(cmd.Args, " "); !strings.Contains(joined, tc.want) {
			t.Errorf("session %q: want %q in %q", tc.session, tc.want, joined)
		}
	}
}
