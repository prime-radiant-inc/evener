// The hub MCP server entry point. Speaks MCP over stdio (the transport
// sessions launch it with), and drives the hub over AppWire with the SDK.
//
// Configuration problems (no token resolvable, say) do not kill the server:
// tools/list still answers, and every hub-touching call fails with the
// actionable configuration error — a supervising agent that can read the
// error can tell its human exactly what to fix, which a dead process cannot.

import type { ConnectionState } from "@evener/appwire-client";
import { McpServer } from "@modelcontextprotocol/sdk/server/mcp.js";
import { StdioServerTransport } from "@modelcontextprotocol/sdk/server/stdio.js";

import { type HubConfig, resolveConfig } from "./config.js";
import { ActivityWatcher } from "./events.js";
import { AppwireHub, type HubInfo, type HubPort } from "./hub.js";
import { TOOLS, type ToolContext, type ToolSpec } from "./tools.js";

const SERVER_VERSION = "0.1.0";

// UnconfiguredPort answers every request with the configuration error that
// stopped startup, so a session still sees tools/list plus an actionable
// failure the moment it calls one, instead of a dead process it cannot
// interrogate at all.
class UnconfiguredPort implements HubPort {
  constructor(private readonly reason: string) {}
  request(): Promise<never> {
    return Promise.reject(new Error(this.reason));
  }
  onNotification(): () => void {
    return () => {};
  }
  onReady(): () => void {
    return () => {};
  }
  connectionState(): ConnectionState {
    return "idle";
  }
  url(): string {
    return "unconfigured";
  }
  info(): HubInfo | undefined {
    return undefined;
  }

  close(): void {}
}

function diagnostic(message: string): void {
  process.stderr.write(`[evener-hub-mcp] ${message}\n`);
}

/** buildContext resolves configuration and wires the hub, watcher, context. */
export function buildContext(port?: HubPort, configOverride?: HubConfig): ToolContext {
  const config = configOverride ?? resolveConfig(process.env);
  const hub = port ?? new AppwireHub(config);
  const watcher = new ActivityWatcher(hub, { diagnostic });
  watcher.start();
  return { port: hub, watcher, config, shutdown: new AbortController() };
}

/** installTools registers every tool with honest, agent-facing errors. */
export function installTools(server: McpServer, ctx: ToolContext): void {
  for (const [name, spec] of Object.entries(TOOLS)) {
    server.registerTool(
      name,
      {
        description: spec.description,
        inputSchema: spec.schema,
      },
      toolHandler(spec, ctx),
    );
  }
}

function toolHandler(spec: ToolSpec, ctx: ToolContext) {
  return async (args: Record<string, unknown>, extra: { signal?: AbortSignal }) => {
    try {
      const text = await spec.run(ctx, args ?? {}, extra.signal);
      return { content: [{ type: "text" as const, text }] };
    } catch (err) {
      const message = err instanceof Error ? err.message : String(err);
      return { content: [{ type: "text" as const, text: message }], isError: true };
    }
  };
}

export async function main(): Promise<void> {
  let ctx: ToolContext;
  try {
    ctx = buildContext();
  } catch (err) {
    const message = err instanceof Error ? err.message : String(err);
    diagnostic(`configuration problem — tools will report this until fixed: ${message}`);
    const hub = new UnconfiguredPort(message);
    const watcher = new ActivityWatcher(hub, { diagnostic });
    watcher.start();
    ctx = {
      port: hub,
      watcher,
      config: { url: "unconfigured", token: "", tokenSource: "unresolved" },
      shutdown: new AbortController(),
    };
  }
  const server = new McpServer({ name: "evener-hub", version: SERVER_VERSION });
  installTools(server, ctx);
  const transport = new StdioServerTransport();
  // A stdio server's lifetime is its client's: when the session's daemon
  // closes the pipe (or dies), the server must let the process exit rather
  // than linger on heartbeat and socket timers — an orphan holding a dead
  // client's pipe is a zombie per session restart (found by the e2e run).
  transport.onclose = () => {
    ctx.shutdown.abort();
    ctx.port.close();
  };
  await server.connect(transport);
  process.stdin.once("close", () => {
    ctx.shutdown.abort();
    ctx.port.close();
  });
  diagnostic(`serving hub MCP on stdio for hub at ${ctx.config.url} (token from ${ctx.config.tokenSource})`);
}

// Run only when executed as the program entry (node dist/src/index.js), not
// when a test imports this module.
if (process.argv[1] && import.meta.url === new URL(`file://${process.argv[1]}`).href) {
  main().catch((err: unknown) => {
    diagnostic(`fatal: ${err instanceof Error ? err.message : String(err)}`);
    process.exitCode = 1;
  });
}
