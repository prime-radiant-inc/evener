# Connect MCP tools

MCP (Model Context Protocol) connects external tools to a session. Evener supports
local stdio processes, HTTP streamable endpoints (`http`), and SSE endpoints
(`sse`). The server supplies tool descriptions and schemas; Evener registers the
tools after connecting. Connecting does not invoke a tool.

## Connect a local server

This example uses the complete [echo server](examples/extensions/mcp-server/main.go).
It returns its input without changing files or contacting a service. You need Go,
an Evener source checkout, and an Evener installation with a working provider
for the final agent invocation. Get the checkout with the `git clone` command in
[Optional disposable exercise](getting-started.md#optional-disposable-exercise). You will build only
the example server, not Evener. The final `evener` command makes a real provider
call and may incur charges. Run on the session host, from the checkout root:

```bash
example_dir=$(mktemp -d)
go build -o "$example_dir/echo-server" ./docs/examples/extensions/mcp-server
export EVENER_EXAMPLE_MCP_SERVER="$example_dir/echo-server"
evener --mcp-config ./docs/examples/extensions/mcp.json \
  'Call example__echo with message "extension check", then report the result.'
```

The complete configuration is:

```json
{
  "mcpServers": {
    "example": {"command": "${EVENER_EXAMPLE_MCP_SERVER}"}
  }
}
```

Verify a connected `example` server, a tool call to `example__echo`, and the tool
result `extension check`. The model repeating those words without a tool call is
not verification. The deterministic [fixture checks](examples/extensions/README.md)
exercise discovery and a real MCP client/server call without a model.

If connection fails, check the expanded executable path and execute permission,
then inspect MCP diagnostics. Keep protocol messages on stdout and diagnostics
on stderr. Use an executable allowed by the session sandbox; do not disable the
sandbox to work around an unexplained failure.

To remove the integration, end the session and omit `--mcp-config` next time.
Delete only the example binary you created and unset `EVENER_EXAMPLE_MCP_SERVER`
when no sessions need it:

```bash
rm "$EVENER_EXAMPLE_MCP_SERVER"
rmdir "$example_dir"
unset EVENER_EXAMPLE_MCP_SERVER example_dir
```

The session manager closes its MCP connections at
teardown. Closing a remote connection does not shut down the remote service.
There is no configuration-file watcher promised here: start a new session after
changing server definitions.

## Configuration locations and precedence

Later layers replace an entire server entry with the same name; they do not
merge individual fields:

1. `$XDG_CONFIG_HOME/evener/mcp.json` (`~/.config/evener/mcp.json` fallback).
2. `<git root>/.evener/mcp.json`, only when a Git root exists.
3. Files passed through repeatable `--mcp-config` options, in order.
4. Repeatable `--mcp` inline specifications.

Inline syntax is `--mcp 'name:command args...'`. It splits arguments on whitespace,
not shell quoting; use a JSON file for paths or arguments containing spaces.
A missing global or project file is normal. Malformed global or project layers
are skipped with warnings; an invalid explicit CLI file or inline specification
is fatal. `EVENER_NO_USER_SKILLS=1` also suppresses the global MCP layer.

A plugin separately contributes `.mcp.json` and an inline manifest `mcpServers`
map. Inline plugin entries override its file entries. Plugin server names become
`plugin_<plugin-name>_<server>`, avoiding ordinary bare-name collisions. Bad
plugin MCP layers warn and are skipped without discarding the plugin's other
components. See [plugin loading](plugins.md).

## Server fields

Each `mcpServers` key is a nonblank server name. Its value supports:

| Field | Meaning |
| --- | --- |
| `type` | `stdio` by default, `sse`, or `http`. Other types fail transport construction. |
| `command` | Required executable for stdio. It is not a shell command string. |
| `args` | Array of arguments passed directly to the executable. |
| `env` | String map added to the stdio process environment. |
| `url` | Required endpoint for `http` or `sse`. |
| `headers` | String map of HTTP headers for `http` or `sse`. |

For a remote endpoint, replace the stdio entry with this schematic entry and set
the variables on the session host before launch:

```json
{
  "mcpServers": {
    "service": {
      "type": "http",
      "url": "${MY_MCP_URL}",
      "headers": {"Authorization": "Bearer ${MY_MCP_TOKEN}"}
    }
  }
}
```

This is not a public test service. Supply an endpoint and credentials you control.
Do not commit tokens. Header values and tool arguments leave the session host for
that service; its permissions are not constrained by the local filesystem sandbox.

## Expansion and trust

String fields expand `$NAME`, `${NAME}`, `${NAME:-default}`, and `$$` for a literal
dollar. Defaults apply to unset or empty variables. Unresolved expressions fail
the affected layer. Global and explicit `--mcp-config` files also allow
`$(command)`, using trimmed stdout. **That command runs on the host when the
configuration loads.** Do not put command expressions in files you have not
reviewed. Project and plugin configurations reject command expressions instead
of executing them. Plugins expand their root placeholders before this parsing.

Stdio servers run as child processes on the session host, under the session
sandbox when configured. The sandbox scrubs secret environment variables and
applies its environment floor; configured `env` values are explicit additions.
Project and plugin MCP configurations do not grant new sandbox filesystem roots.
A server's connection does not grant its tools permission to bypass session
policy. See [sandboxing](sandboxing.md).

## Tool names, failures, and teardown

Tools are registered as `<server>__<tool>`, sanitized for provider-compatible
names. For this example the name is `example__echo`, not
`mcp__example__echo`. Hook matchers see the registered name unchanged.

Each server's startup transport setup, connection, and tool listing share one
10-second deadline. Servers start independently, so a bad server does not prevent
other servers from connecting. Diagnostics distinguish
`connected`, `degraded`, and `failed`. A server can be connected yet report an
application error. Transport reconnects and failure handling are not a guarantee
that a failed call had no external effect; check the service before repeating a
side-effecting operation.

Evener owns the stdio transport lifetime and closes connections at session
teardown. Removing a configuration entry does not cancel a call already in
progress or undo work the remote service performed.
