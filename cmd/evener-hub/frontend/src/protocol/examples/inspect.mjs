import { readFileSync } from "node:fs";
import { AppwireClient } from "@evener/appwire-client";
import { WebSocket } from "ws";

const url = process.env.EVENER_RPC_URL;
const cwd = process.env.EVENER_CWD;
if (!url || !cwd) throw new Error("Set EVENER_RPC_URL and EVENER_CWD.");
const tokenFile = process.env.EVENER_TOKEN_FILE;
const token = tokenFile ? readFileSync(tokenFile, "utf8").trim() : "";
const hub = new AppwireClient({
  url,
  clientInfo: { name: "appwire-reference", version: "0.1.0" },
  socketFactory: (endpoint) =>
    new WebSocket(endpoint, {
      headers: token ? { Authorization: `Bearer ${token}` } : {},
    }),
});
try {
  const hello = await hub.connect();
  const models = await hub.request("model/list", { cwd });
  const sessions = await hub.request("thread/list", { limit: 20 });
  const schema = await hub.request("evener/launch/schema", {});
  const resolved = await hub.request("evener/launch/resolve", { cwd });
  console.log(
    JSON.stringify(
      {
        protocolVersion: hello.protocolVersion,
        sourceId: hello.sourceId,
        models: models.data.length,
        sessionsOnFirstPage: sessions.data.length,
        hasMoreSessions: !!sessions.nextCursor,
        launchOptions: schema.options.length,
        resolvedLayers: Object.keys(resolved.layers),
        repositoryTrust: resolved.repo?.trust ?? "absent",
        diagnostics: resolved.diagnostics?.length ?? 0,
      },
      null,
      2,
    ),
  );
} finally {
  hub.close();
}
