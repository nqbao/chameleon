package chameleon

import "testing"

func TestDockerMCPURL(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{"http://localhost:8000/mcp", "http://host.docker.internal:8000/mcp"},
		{"http://localhost:${MCP_PORT}/mcp", "http://host.docker.internal:${MCP_PORT}/mcp"},
		{"http://127.0.0.1:${MCP_PORT}/${MCP_PATH}", "http://host.docker.internal:${MCP_PORT}/${MCP_PATH}"},
		{"https://user:p%40ss@[::1]:${MCP_PORT}/mcp?q=${QUERY}", "https://user:p%40ss@host.docker.internal:${MCP_PORT}/mcp?q=${QUERY}"},
		{"https://user:p%40ss@LOCALHOST.:443/a%2Fb?q=localhost%2F#frag", "https://user:p%40ss@host.docker.internal:443/a%2Fb?q=localhost%2F#frag"},
		{"http://0.0.0.0/mcp", "http://host.docker.internal/mcp"},
		{"http://0.0.0.0:${MCP_PORT}/${MCP_PATH}", "http://host.docker.internal:${MCP_PORT}/${MCP_PATH}"},
		{"http://[::]:8000/mcp", "http://host.docker.internal:8000/mcp"},
		{"http://[::]:${MCP_PORT}/mcp", "http://host.docker.internal:${MCP_PORT}/mcp"},
		{"http://127.0.0.1/mcp", "http://host.docker.internal/mcp"},
		{"http://127.2.3.4:9000/sse", "http://host.docker.internal:9000/sse"},
		{"http://[::1]:8000/mcp", "http://host.docker.internal:8000/mcp"},
		{"http://[::ffff:127.0.0.1]/mcp", "http://host.docker.internal/mcp"},
		{"http://localhost:8000/${MCP_PATH}/mcp", "http://host.docker.internal:8000/${MCP_PATH}/mcp"},
		{"http://localhost:8000/a b|c/日本語?q=${QUERY}#${FRAGMENT}", "http://host.docker.internal:8000/a b|c/日本語?q=${QUERY}#${FRAGMENT}"},
		{"http://localhost:/mcp", "http://host.docker.internal:/mcp"},
		{"HTTP://user:p%40ss@[::1]:08000/${PATH}/a%2fb?x=localhost#with space", "HTTP://user:p%40ss@host.docker.internal:08000/${PATH}/a%2fb?x=localhost#with space"},
	} {
		t.Run(tc.in, func(t *testing.T) {
			if got := dockerMCPURL(tc.in); got != tc.want {
				t.Fatalf("got %q, want %q", got, tc.want)
			}
		})
	}
	for _, raw := range []string{"", "https://example.com/localhost", "http://localhost.example.com/mcp", "http://host.docker.internal:8000/mcp", "http://192.168.1.2/mcp", "http://[::2]/mcp", "file://localhost/mcp", "http://localhost:bad/mcp", "http://localhost/%zz", "${MCP_URL}", "http://${HOST}:8000/mcp", "http://${HOST}:${PORT}/mcp", "http://example.com:${PORT}/mcp", "http://localhost:${BAD-NAME}/mcp", "http://[::1]:${PORT/mcp"} {
		if got := dockerMCPURL(raw); got != raw {
			t.Errorf("changed %q to %q", raw, got)
		}
	}
}
