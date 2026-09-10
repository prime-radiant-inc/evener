import { clientFromEnvironment } from "./connection.mjs";

const { hub, cwd } = clientFromEnvironment();
try {
  const hello = await hub.connect();
  const models = await hub.request("model/list", { cwd });
  const sessions = await hub.request("thread/list", { limit: 20 });
  const schema = await hub.request("evener/launch/schema", {});
  const resolved = await hub.request("evener/launch/resolve", { cwd });
  console.log(JSON.stringify({ protocolVersion: hello.protocolVersion, sourceId: hello.sourceId,
    models: models.data.length, sessionsOnFirstPage: sessions.data.length,
    hasMoreSessions: !!sessions.nextCursor, launchOptions: schema.options.length,
    resolvedLayers: Object.keys(resolved.layers), repositoryTrust: resolved.repo?.trust ?? "absent",
    diagnostics: resolved.diagnostics?.length ?? 0 }, null, 2));
} finally { hub.close(); }
