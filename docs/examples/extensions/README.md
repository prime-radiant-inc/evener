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

Contributor commands below run from the **Evener checkout root**, not from an
installed binary's directory. The Go checks need the checkout's Go toolchain.
The CLI/web tour also needs Python 3, Node 22, the frontend and `docs-site`
dependencies, and Playwright Chromium. Use
[the frontend build prerequisites](../../developing-evener/building.md) and
provision `docs-site/node_modules` from its lockfile before running the tour.
Install Chromium with `cd docs-site && npx playwright install chromium`, then
return to the checkout root. The tour builds the production frontend before
embedding it; run tour/build commands serially. Preserve existing dependency
installations and do not run `npm ci` through a `node_modules` symlink.

```bash
go test ./docs/examples/extensions/...
go test ./cmd/evener-hub -run '^TestTutorialExtension(Effects|MCPFailures)$' -count=1
node docs-site/scripts/tours/refresh.mjs check --surfaces cli,web --scenario extensions
go test ./agent/skill ./agent/plugin ./agent/mcpconfig ./agent/internal/hooks ./agent/internal/mcp ./internal/plugins
```

The example suite exercises the skill, command, plugin, and MCP configuration
loaders. It executes the startup command and parses its JSON output.
`TestEchoStdioProtocol` also compiles the server, initializes an MCP SDK client
over OS pipes, checks the `echo` schema, round-trips a random nonce, and waits for
process exit. The older in-memory protocol test remains a separate check.

The tours copy the skill, command, and MCP configuration into disposable
workspaces and load the plugin from this example directory. They launch real
CLI and web sessions. Only the model responses are scripted. They
invoke the local command, `use_skill`, the plugin command and startup hook, and
`example__echo` through the runtime. Structured records and workspace hashes,
not assistant prose, authorize checkpoints. Missing servers and malformed MCP
configuration cannot supply the required tool catalogue. CLI captures show
host-side copying and a filtered query of actual verbose event logs; web captures
show the real interface. No assets are published by `check`.

These checks make no hosted model requests or marketplace downloads. Scripted
tool choice proves integration, not model compliance, delegate selection, or
model quality. Native coverage is deferred.
