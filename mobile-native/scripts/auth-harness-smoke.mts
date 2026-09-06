import WebSocket from "ws";
import type { WebSocketLike } from "../../cmd/evener-hub/frontend/src/protocol/transport";
import { createHubClient } from "../src/connection";

const c = createHubClient(
  "http://127.0.0.1:9201",
  "",
  (url, options) => new WebSocket(url, options) as unknown as WebSocketLike,
);
try {
  await c.connect();
  const list = await c.request("evener/instance/list", {});
  console.log({
    instances: list.instances.map((x) => ({
      name: x.name,
      authModes: x.authModes,
    })),
  });
  const start = await c.request("evener/auth/device/start", {
    provider: "work",
  });
  const pending = await c.request("evener/auth/device/poll", {
    provider: "work",
    flowId: start.flowId,
  });
  if (pending.state !== "pending") throw Error("expected pending");
  await fetch("http://127.0.0.1:9201/approve", { method: "POST" });
  const result = await c.request("evener/auth/device/poll", {
    provider: "work",
    flowId: start.flowId,
  });
  if (result.state !== "authorized" || result.status?.activeSource !== "oauth")
    throw Error("not authorized");
  console.log({
    device: result.state,
    storedSource: result.status.activeSource,
  });
  await c.request("evener/auth/logout", { provider: "work" });
  await fetch("http://127.0.0.1:9201/mode/browser", { method: "POST" });
  const fallback = await c.request("evener/auth/device/start", {
    provider: "work",
  });
  if (!fallback.fallback) throw Error("no fallback");
  const browser = await c.request("evener/auth/login/start", {
    provider: "work",
  });
  const url = new URL(browser.url);
  const redirectUrl =
    "http://localhost:1455/auth/callback?" +
    new URLSearchParams({
      code: "fixture-code",
      state: url.searchParams.get("state") ?? "",
    });
  const complete = await c.request("evener/auth/login/complete", {
    provider: "work",
    flowId: browser.flowId,
    redirectUrl,
  });
  if (complete.status.activeSource !== "oauth")
    throw Error("browser not stored");
  console.log({ browser: complete.status.activeSource });
  await c.request("evener/auth/logout", { provider: "work" });
  await fetch("http://127.0.0.1:9201/mode/device", { method: "POST" });
  console.log(await (await fetch("http://127.0.0.1:9201/status")).json());
} finally {
  c.close();
}
