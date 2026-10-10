# Chameleon

Chameleon runs agent CLIs on the host, in Docker, or in a sandbox. Use the `cham`
command or import `github.com/nqbao/chameleon` into a Go application.

This project is purely an agent runner. It has no server, database, or frontend.
This checkout has not been published or versioned.

## Build and run

Requires Go 1.26.2 or newer. The runtime you select must be installed on the host
or in the selected Docker image. Supported runtimes are `claude`, `codex`,
`opencode`, `pi`, and `shell`.

```sh
make build
./bin/cham run --agent shell -p 'printf "hello\n"'
./bin/cham run --agent claude -p 'Explain this repository'
./bin/cham run --agent codex --sandbox -p 'Review this change'
./bin/cham run --agent claude --docker my-agent-image --setup
./bin/cham run examples/review.md topic=error-handling
```

Omit `-p` for interactive agent use. Run `cham run --help` for flags.
`--agent persona.md` uses the Markdown body as the system prompt; task Markdown
files use the body as the user prompt. CLI flags override frontmatter. `${name}`
is required, `${name:-default}` supplies a default, and `$$` escapes a dollar sign.
The `runfile` package also supports `@path` values for file content substitution.

`--sandbox` uses native agent settings for Claude and Codex. For Pi,
OpenCode, and shell it uses macOS `sandbox-exec`. `--sandbox-no-network` applies
only to the latter group. Docker and sandbox flags are mutually exclusive.

`--docker-args` passes extra flags to `docker run`, before the image name. The
string is split shell-style (quotes and backslashes; no variable expansion, no
shell):

```sh
cham run --agent claude --docker my-image \
  --docker-args '--network=host -v "/my dir:/data" -e FOO=bar' -p 'hi'
```

It requires `--docker` and is not read from run-file frontmatter (see also `config.yml` below). The flags are
trusted input: `--privileged` or a Docker socket mount removes container
isolation, and values in `ps` output are visible to other users, so do not put
credentials in it. Embedders set `Request.DockerArgs` or `DockerOpts.RunArgs` and
must not fill them from untrusted input.

## Argument safety

Agent CLIs are launched with an argument list, never through a shell (the `shell`
runtime runs the prompt as a command by design). Values that could be parsed as
flags are guarded in `Run`: a model, session, tool, file, image, docker image or
docker socket starting with `-` is rejected, and a prompt or system prompt starting
with `-` is prefixed with a space. `ExtraArgs` and `--docker-args` are
pass-through by design.

## Configuration

`cham` is driven by flags, environment variables and optional `config.yml` files.
`CHAMELEON_HOME` (default `~/.chameleon`) is the home directory. Docker
credential mounts live under `$CHAMELEON_DOCKER_HOME`, defaulting to
`<home>/docker`. `CHAMELEON_DOCKER_SOCKET` sets the Docker socket.

Default `docker run` flags can be set in `~/.chameleon/config.yml` and in
`<workspace>/.chameleon/config.yml`:

```yaml
docker:
  - --network=host
  - -v "/my dir:/data"
```

Each entry is split shell-style (quotes and backslashes, no expansion), so
`- -e` followed by `- FOO=bar` works too. Flags apply only to `--docker` runs and are
combined in order: user config, workspace config, then `--docker-args`. Missing files
are fine; unknown keys or bad quoting are errors. `docker` is the only supported key.

The workspace file belongs to the repository the agent works on, and the agent can
edit it. Flags such as `--privileged` or a Docker socket mount there remove
container isolation, so `cham` prints the workspace flags it applies to stderr.
Only run `--docker` in repositories you trust, or keep those flags in the user file.

## MCP servers

`--mcp-config servers.json` (repeatable; later files replace same-named servers)
exposes MCP servers to the agent. The file uses the common `mcpServers` format:

```json
{"mcpServers": {
  "fs":   {"command": "npx", "args": ["-y", "@modelcontextprotocol/server-filesystem", "/work"],
           "env": {"API_KEY": "${API_KEY}"}},
  "docs": {"url": "https://example.com/mcp",
           "headers": {"Authorization": "Bearer ${DOCS_KEY}"}}
}}
```

Each server has either `command` (stdio; optional `args`, `env`) or `url` (optional
`type` of `http` or `sse`, and `headers`). Unknown fields are an error. Chameleon
translates the file for the runtime:

| Runtime | How it is passed | `${NAME}` references |
|---|---|---|
| `claude` | temp file via `--mcp-config` | expanded by Claude |
| `codex` | `-c mcp_servers.<name>.*` overrides | whole values only (`env_vars`, `env_http_headers`, `bearer_token_env_var`); no `sse` |
| `opencode` | `OPENCODE_CONFIG_CONTENT`, merged over your config and over any inline config already set | converted to `{env:NAME}` |
| `pi` | generated extension calling `pi.registerMcpServer` (Pi 0.99 or newer; tools exposed directly, no `sse`) | expanded by Pi |
| `shell` | not supported; the run fails | |

Codex merges `-c` overrides into a same-named server from its own `config.toml`.
Chameleon always sets `args` and `env_vars`, which Codex replaces, but `env`, headers and
a `bearer_token_env_var` merge key by key and a stdio/http switch is rejected by Codex,
so give servers names that your Codex config does not use. For OpenCode, an
`OPENCODE_CONFIG_CONTENT` already in the environment (inherited, `--env` or
`--env-file`) is kept: its other settings survive and servers from `--mcp-config`
replace same-named ones under `mcp`. Malformed JSON there is an error.

Put the variables a server needs in the agent environment with `--env` / `--env-file`.
Literal secrets in the file reach Codex as command-line arguments, visible in `ps`;
use `${NAME}` references instead. Generated Claude and Pi files live in a private
(0700) temp directory but are mode 0644, so a container user with a different UID can
read the mounted copy. Under `--docker` the commands in stdio servers must
exist in the image, and a server on the host needs `host.docker.internal`, not
`localhost`. `--sandbox-no-network` blocks remote servers.

The file is trusted input: a stdio server runs its command as you. For that reason it
is a flag (or `Request.MCPConfig`), not run-file frontmatter. Embedders must not fill
it from untrusted input.

## Embed

```go
opts := chameleon.Options{BaseEnv: os.Environ()}
result, err := opts.Run(ctx, chameleon.Request{
    Runtime: "shell",
    Dir:     "/path/to/workspace",
    Prompt:  "printf hello",
})
```

See [the runnable embedding example](examples/embed/main.go). Library callers
supply home, inherited environment, and IO explicitly. Nil
`BaseEnv` means an empty inherited environment. `Run` returns errors and exit codes;
it does not call `os.Exit` or print results. Interactive IO and stream callbacks
are optional fields on `Request`.

Use `Options.HostSpec`, `DockerSpec`, and `SandboxSpec` with the exported
`environment` package for lower-level process and PTY control. `adapter` exposes
runtime command builders and output parsers. `cli.Main(ctx, args, cli.IO{...})`
embeds the run CLI.

## Environment variables for the agent

Extra variables reach the agent process in every environment (host, sandbox and
Docker). Sources, from lowest to highest precedence:

1. `<home>/env`, loaded automatically if present (`~/.chameleon/env` by default)
2. `--env-file path`, repeatable, in order
3. `--env NAME=value`, or `--env NAME` to copy `NAME` from the current environment
   (an error if it is unset). Repeatable.

They override workspace secrets from `secrets.yml` and the inherited environment.

```sh
cham run --agent claude --env-file .env.agent --env GH_TOKEN -p 'Open a PR'
```

Env files are a small dotenv subset: `NAME=value` lines, `#` comments, optional
`export `, `'literal'` and `"escaped\n"` quoted values. There is no variable
expansion and no multi-line values. Docker gets the values through a private
`--env-file`, so they do not appear in `docker run` arguments. Prefer `--env NAME`
or a file over `--env NAME=value` for secrets, since command-line values are
visible in `ps`. There is intentionally no workspace-level env file: the agent
can edit files in the repository, and a repository should not be able to inject
variables such as `PATH` into host runs. Embedders set `Request.EnvVars`.

## Development status

```sh
make check
go test -race ./...
```

Docker command construction is tested using a local fake executable. Real agent
logins, Docker runs, macOS sandbox enforcement, and persistent terminal recovery
still need manual validation.
