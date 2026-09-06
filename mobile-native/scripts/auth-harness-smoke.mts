import assert from "node:assert/strict";
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
  const expiring = await c.request("evener/auth/device/start", {
    provider: "work",
  });
  const expire = await fetch("http://127.0.0.1:9201/clock/expire", {
    method: "POST",
  });
  assert.equal(expire.status, 204, "fixture expiry control");
  assert.equal(
    (
      await c.request("evener/auth/device/poll", {
        provider: "work",
        flowId: expiring.flowId,
      })
    ).state,
    "expired",
  );
  const retrying = await c.request("evener/auth/device/start", {
    provider: "work",
  });
  const fault = await fetch("http://127.0.0.1:9201/fault/poll", {
    method: "POST",
  });
  assert.equal(fault.status, 204, "fixture poll failure control");
  await assert.rejects(
    c.request("evener/auth/device/poll", {
      provider: "work",
      flowId: retrying.flowId,
    }),
  );
  const beforeRetry = await c.request("evener/auth/status", {
    provider: "work",
  });
  assert.equal(beforeRetry.signedIn, false);
  const clear = await fetch("http://127.0.0.1:9201/fault/clear", {
    method: "POST",
  });
  assert.equal(clear.status, 204, "fixture failure recovery control");
  await fetch("http://127.0.0.1:9201/approve", { method: "POST" });
  const retried = await c.request("evener/auth/device/poll", {
    provider: "work",
    flowId: retrying.flowId,
  });
  assert.equal(retried.state, "authorized");
  assert.equal(retried.status?.activeSource, "oauth");
  await c.request("evener/auth/logout", { provider: "work" });
  console.log({ expiry: "expired", sameFlowRetry: "authorized" });
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
