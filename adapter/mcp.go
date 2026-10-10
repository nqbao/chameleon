package adapter

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// MCPServer is a runtime-neutral MCP server definition, using the field names of
// the common {"mcpServers": {...}} JSON format. Exactly one of Command (stdio)
// or URL (remote) is set. Values in Env and Headers may reference environment
// variables as ${NAME}.
type MCPServer struct {
	Type    string            `json:"type,omitempty"` // "stdio", "http" or "sse"; inferred when empty
	Command string            `json:"command,omitempty"`
	Args    []string          `json:"args,omitempty"`
	Env     map[string]string `json:"env,omitempty"`
	URL     string            `json:"url,omitempty"`
	Headers map[string]string `json:"headers,omitempty"`
}

var mcpNameRe = regexp.MustCompile(`^[A-Za-z0-9_-]+$`)

// IsRemote reports whether the server is reached over the network.
func (s MCPServer) IsRemote() bool { return s.URL != "" }

// Transport returns the normalized transport: "stdio", "http" or "sse".
func (s MCPServer) Transport() string {
	switch {
	case s.Type != "":
		return s.Type
	case s.URL != "":
		return "http"
	default:
		return "stdio"
	}
}

// ParseMCPConfig decodes one {"mcpServers": {...}} document and validates it.
// Unknown fields are rejected so settings are never silently dropped.
func ParseMCPConfig(data []byte) (map[string]MCPServer, error) {
	var doc struct {
		Servers map[string]json.RawMessage `json:"mcpServers"`
	}
	if err := json.Unmarshal(data, &doc); err != nil {
		return nil, fmt.Errorf("invalid MCP config: %w", err)
	}
	if len(doc.Servers) == 0 {
		return nil, fmt.Errorf(`invalid MCP config: no servers under "mcpServers"`)
	}
	out := make(map[string]MCPServer, len(doc.Servers))
	for name, raw := range doc.Servers {
		if !mcpNameRe.MatchString(name) {
			return nil, fmt.Errorf("invalid MCP server name %q: want letters, digits, '-' or '_'", name)
		}
		var s MCPServer
		dec := json.NewDecoder(bytes.NewReader(raw))
		dec.DisallowUnknownFields()
		if err := dec.Decode(&s); err != nil {
			return nil, fmt.Errorf("MCP server %q: %w", name, err)
		}
		if err := s.validate(); err != nil {
			return nil, fmt.Errorf("MCP server %q: %w", name, err)
		}
		out[name] = s
	}
	return out, nil
}

func (s MCPServer) validate() error {
	if (s.Command == "") == (s.URL == "") {
		return fmt.Errorf(`exactly one of "command" or "url" is required`)
	}
	switch s.Type {
	case "", "stdio", "http", "sse":
	default:
		return fmt.Errorf(`unsupported type %q: want "stdio", "http" or "sse"`, s.Type)
	}
	if s.Type == "stdio" && s.Command == "" || s.IsRemote() && s.Type == "stdio" {
		return fmt.Errorf(`type "stdio" requires "command" and not "url"`)
	}
	if s.Command != "" && (s.Type == "http" || s.Type == "sse") {
		return fmt.Errorf(`type %q requires "url"`, s.Type)
	}
	if s.IsRemote() && (len(s.Args) > 0 || len(s.Env) > 0) {
		return fmt.Errorf(`"args" and "env" apply to command servers only`)
	}
	if s.Command != "" && len(s.Headers) > 0 {
		return fmt.Errorf(`"headers" applies to url servers only`)
	}
	return nil
}

// LoadMCPConfigs reads each file and merges the servers; a later file replaces a
// server of the same name from an earlier one.
func LoadMCPConfigs(paths []string) (map[string]MCPServer, error) {
	merged := map[string]MCPServer{}
	for _, p := range paths {
		data, err := os.ReadFile(p)
		if err != nil {
			return nil, fmt.Errorf("MCP config: %w", err)
		}
		servers, err := ParseMCPConfig(data)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", p, err)
		}
		for name, s := range servers {
			merged[name] = s
		}
	}
	return merged, nil
}

func sortedMCPNames(servers map[string]MCPServer) []string {
	names := make([]string, 0, len(servers))
	for n := range servers {
		names = append(names, n)
	}
	sort.Strings(names)
	return names
}

var envRefRe = regexp.MustCompile(`\$\{([A-Za-z_][A-Za-z0-9_]*)\}`)

// wholeEnvRef returns NAME when v is exactly "${NAME}".
func wholeEnvRef(v string) (string, bool) {
	m := envRefRe.FindStringSubmatch(v)
	if m != nil && m[0] == v {
		return m[1], true
	}
	return "", false
}

func mcpUnsupported(runtime string, servers map[string]MCPServer) error {
	if len(servers) == 0 {
		return nil
	}
	return fmt.Errorf("%s does not support MCP servers", runtime)
}

// --- claude ---

// claudeMCPJSON renders servers as Claude's --mcp-config document. Claude expands
// ${NAME} references itself, so values pass through unchanged.
func claudeMCPJSON(servers map[string]MCPServer) ([]byte, error) {
	out := make(map[string]MCPServer, len(servers))
	for name, s := range servers {
		if s.IsRemote() && s.Type == "" {
			s.Type = "http"
		}
		out[name] = s
	}
	return json.Marshal(map[string]any{"mcpServers": out})
}

// writeClaudeMCPConfig writes the config and returns the file plus every path to
// remove afterwards (the file first, then its directory).
func writeClaudeMCPConfig(servers map[string]MCPServer) (file string, cleanup []string, err error) {
	data, err := claudeMCPJSON(servers)
	if err != nil {
		return "", nil, fmt.Errorf("marshal MCP config: %w", err)
	}
	return writeMCPFile("cham-mcp.json", data)
}

// writeMCPFile stores data, which may hold literal credentials, in a new 0700
// directory. The file itself is 0644 so a container user whose UID differs from
// the host owner can still read the bind mount; other host users are kept out by
// the directory. cleanup lists the file then its directory, ready for removal in
// order.
func writeMCPFile(name string, data []byte) (file string, cleanup []string, err error) {
	dir, err := os.MkdirTemp("", "cham-mcp-*")
	if err != nil {
		return "", nil, fmt.Errorf("create MCP config directory: %w", err)
	}
	file = filepath.Join(dir, name)
	if err := os.WriteFile(file, data, 0o644); err != nil {
		os.RemoveAll(dir)
		return "", nil, fmt.Errorf("write MCP config file: %w", err)
	}
	// WriteFile is subject to the umask; make the mode explicit.
	if err := os.Chmod(file, 0o644); err != nil {
		os.RemoveAll(dir)
		return "", nil, fmt.Errorf("write MCP config file: %w", err)
	}
	return file, []string{file, dir}, nil
}

// --- codex ---

// tomlString renders s as a TOML basic string.
func tomlString(s string) string {
	var b strings.Builder
	b.WriteByte('"')
	for _, r := range s {
		switch {
		case r == '"':
			b.WriteString(`\"`)
		case r == '\\':
			b.WriteString(`\\`)
		case r == '\n':
			b.WriteString(`\n`)
		case r == '\r':
			b.WriteString(`\r`)
		case r == '\t':
			b.WriteString(`\t`)
		case r < 0x20 || r == 0x7f:
			fmt.Fprintf(&b, `\u%04X`, r)
		default:
			b.WriteRune(r)
		}
	}
	b.WriteByte('"')
	return b.String()
}

func tomlStringArray(vals []string) string {
	parts := make([]string, len(vals))
	for i, v := range vals {
		parts[i] = tomlString(v)
	}
	return "[" + strings.Join(parts, ", ") + "]"
}

func tomlInlineTable(m map[string]string) string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	parts := make([]string, len(keys))
	for i, k := range keys {
		parts[i] = tomlString(k) + " = " + tomlString(m[k])
	}
	return "{" + strings.Join(parts, ", ") + "}"
}

// codexMCPArgs translates servers to `-c mcp_servers.<name>.<key>=<toml>`
// overrides. A whole-value ${NAME} is forwarded by reference (env_vars,
// env_http_headers, bearer_token_env_var) so the secret stays out of argv; any
// other ${...} use is rejected because codex does not expand it.
func codexMCPArgs(servers map[string]MCPServer) ([]string, error) {
	var args []string
	set := func(name, key, val string) {
		args = append(args, "-c", "mcp_servers."+name+"."+key+"="+val)
	}
	for _, name := range sortedMCPNames(servers) {
		s := servers[name]
		if s.Transport() == "sse" {
			return nil, fmt.Errorf("MCP server %q: codex supports stdio and streamable http servers, not sse", name)
		}
		if !s.IsRemote() {
			// Codex merges -c overrides into a same-named entry from config.toml:
			// arrays replace but maps merge key by key, so always set the arrays to
			// keep stale args and forwarded variables out.
			set(name, "command", tomlString(s.Command))
			set(name, "args", tomlStringArray(s.Args))
			literal := map[string]string{}
			var forward []string
			for k, v := range s.Env {
				if ref, ok := wholeEnvRef(v); ok && ref == k {
					forward = append(forward, k)
				} else if strings.Contains(v, "${") {
					return nil, fmt.Errorf("MCP server %q: env %s: codex only supports ${%s} as the whole value", name, k, k)
				} else {
					literal[k] = v
				}
			}
			sort.Strings(forward)
			if len(literal) > 0 {
				set(name, "env", tomlInlineTable(literal))
			}
			set(name, "env_vars", tomlStringArray(forward))
			continue
		}
		set(name, "url", tomlString(s.URL))
		static := map[string]string{}
		fromEnv := map[string]string{}
		for h, v := range s.Headers {
			ref, whole := wholeEnvRef(v)
			switch {
			case strings.EqualFold(h, "Authorization") && strings.HasPrefix(v, "Bearer ") && isWholeRef(strings.TrimPrefix(v, "Bearer ")):
				set(name, "bearer_token_env_var", tomlString(envRefRe.FindStringSubmatch(v)[1]))
			case whole:
				fromEnv[h] = ref
			case strings.Contains(v, "${"):
				return nil, fmt.Errorf("MCP server %q: header %s: codex only supports a whole-value ${NAME} or \"Bearer ${NAME}\"", name, h)
			default:
				static[h] = v
			}
		}
		if len(static) > 0 {
			set(name, "http_headers", tomlInlineTable(static))
		}
		if len(fromEnv) > 0 {
			set(name, "env_http_headers", tomlInlineTable(fromEnv))
		}
	}
	return args, nil
}

func isWholeRef(v string) bool { _, ok := wholeEnvRef(v); return ok }

// --- opencode ---

// opencodeMCPConfig renders servers as an opencode config fragment
// ({"mcp": {...}}). ${NAME} becomes opencode's {env:NAME} substitution.
func opencodeMCPConfig(servers map[string]MCPServer) ([]byte, error) {
	conv := func(v string) string { return envRefRe.ReplaceAllString(v, "{env:$1}") }
	convMap := func(m map[string]string) map[string]string {
		if len(m) == 0 {
			return nil
		}
		out := make(map[string]string, len(m))
		for k, v := range m {
			out[k] = conv(v)
		}
		return out
	}
	mcp := map[string]any{}
	for name, s := range servers {
		if s.IsRemote() {
			e := map[string]any{"type": "remote", "url": conv(s.URL), "enabled": true}
			if h := convMap(s.Headers); h != nil {
				e["headers"] = h
			}
			mcp[name] = e
			continue
		}
		e := map[string]any{"type": "local", "command": append([]string{s.Command}, s.Args...), "enabled": true}
		if env := convMap(s.Env); env != nil {
			e["environment"] = env
		}
		mcp[name] = e
	}
	return json.Marshal(map[string]any{"mcp": mcp})
}

// --- pi ---

// piMCPExtension renders a Pi extension that registers servers with
// pi.registerMcpServer (Pi >= 0.99). Pi expands ${NAME} in env and headers
// itself. Servers use "direct" exposure so their tools are declared to the model;
// Pi's default, "codemode", hides them behind a script sandbox.
func piMCPExtension(servers map[string]MCPServer) ([]byte, error) {
	cfg := make(map[string]map[string]any, len(servers))
	for _, name := range sortedMCPNames(servers) {
		s := servers[name]
		if s.Transport() == "sse" {
			return nil, fmt.Errorf("MCP server %q: pi supports stdio and streamable http servers, not sse", name)
		}
		e := map[string]any{"exposure": "direct"}
		if s.IsRemote() {
			e["url"] = s.URL
			if len(s.Headers) > 0 {
				e["headers"] = s.Headers
			}
		} else {
			e["command"] = s.Command
			if len(s.Args) > 0 {
				e["args"] = s.Args
			}
			if len(s.Env) > 0 {
				e["env"] = s.Env
			}
		}
		cfg[name] = e
	}
	data, err := json.Marshal(cfg)
	if err != nil {
		return nil, err
	}
	var b bytes.Buffer
	b.WriteString("// Generated by chameleon: registers the MCP servers passed with --mcp-config.\n")
	b.WriteString("const servers: Record<string, unknown> = ")
	b.Write(data)
	b.WriteString("\n\nexport default function (pi: any) {\n")
	b.WriteString("  if (typeof pi.registerMcpServer !== \"function\") {\n")
	b.WriteString("    throw new Error(\"chameleon: MCP servers need pi >= 0.99 (pi.registerMcpServer is missing)\")\n")
	b.WriteString("  }\n")
	b.WriteString("  for (const [name, config] of Object.entries(servers)) pi.registerMcpServer(name, config)\n")
	b.WriteString("}\n")
	return b.Bytes(), nil
}

// mergeOpenCodeInline combines an existing OPENCODE_CONFIG_CONTENT document with
// the generated MCP fragment. Every top-level setting of the existing document is
// kept; its "mcp" servers are kept too, except that a server the caller passed
// with --mcp-config replaces one of the same name.
func mergeOpenCodeInline(existing string, generated []byte) ([]byte, error) {
	if strings.TrimSpace(existing) == "" {
		return generated, nil
	}
	var base map[string]json.RawMessage
	if err := json.Unmarshal([]byte(existing), &base); err != nil {
		return nil, fmt.Errorf("OPENCODE_CONFIG_CONTENT is not a valid JSON object: %w", err)
	}
	if base == nil { // the JSON literal null unmarshals without error
		return nil, fmt.Errorf("OPENCODE_CONFIG_CONTENT is not a valid JSON object: got null")
	}
	var gen struct {
		MCP map[string]json.RawMessage `json:"mcp"`
	}
	if err := json.Unmarshal(generated, &gen); err != nil {
		return nil, err
	}
	servers := map[string]json.RawMessage{}
	if raw, ok := base["mcp"]; ok {
		if err := json.Unmarshal(raw, &servers); err != nil || servers == nil {
			return nil, fmt.Errorf(`OPENCODE_CONFIG_CONTENT: "mcp" must be a JSON object`)
		}
	}
	for name, raw := range gen.MCP {
		servers[name] = raw
	}
	merged, err := json.Marshal(servers)
	if err != nil {
		return nil, err
	}
	base["mcp"] = merged
	return json.Marshal(base)
}

// lastEnvValue returns the value of the last NAME=value entry for name.
func lastEnvValue(env []string, name string) (string, bool) {
	val, found := "", false
	for _, kv := range env {
		if v, ok := strings.CutPrefix(kv, name+"="); ok {
			val, found = v, true
		}
	}
	return val, found
}
