# Extension examples

Run these from the Evener checkout root. They are authoring fixtures, not
extensions automatically loaded by being in this directory.

| Files | Use |
| --- | --- |
| [skills/change-summary/SKILL.md](skills/change-summary/SKILL.md) | Copy to your project's `skills/change-summary/SKILL.md`. |
| [commands/summarize-change.md](commands/summarize-change.md) | Copy to `.evener/commands/summarize-change.md`. |
| [hello-plugin manifest](hello-plugin/.claude-plugin/plugin.json) | Pass `docs/examples/extensions/hello-plugin` to `--plugin-dir`. Includes a command, startup hook, and delegate role. |
| [mcp.json](mcp.json) and [mcp-server/main.go](mcp-server/main.go) | Build the echo server and set `EVENER_EXAMPLE_MCP_SERVER` as described in [MCP](../../mcp.md). |

Before copying, inspect existing destination files and merge deliberately rather
than overwriting another extension. See [skills](../../skills.md),
[plugins](../../plugins.md), and [hooks](../../hooks.md) for invocation and removal.

## Deterministic checks

```bash
go test ./docs/examples/extensions/...
go test ./agent/skill ./agent/plugin ./agent/mcpconfig ./agent/internal/hooks ./agent/internal/mcp ./internal/plugins
```

The example tests call the real skill parser and loader, command parser, plugin loader, and MCP
configuration loader. They execute the fixture's exec-form startup command and
parse its output as JSON. The echo-server test connects a real MCP SDK client
and server over an in-memory transport, lists tools, and calls `echo` with a
sentinel message. It does not test OS stdio transport or an Evener session launch.
The subsystem tests cover the engine paths separately; live MCP tests are opt-in.

These checks make no model requests. They do not prove that a model follows a
skill, chooses a delegate, or calls the requested tool. The guide's Evener launch
commands are separate, model-driven walkthroughs.
