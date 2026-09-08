import assert from "node:assert/strict";
import WebSocket from "ws";
import type { WebSocketLike } from "../../cmd/evener-hub/frontend/src/protocol/transport";
import { createHubClient } from "../src/connection";

const origin = process.env.EVENER_NATIVE_AUTH_ORIGIN;
if (!origin)
  throw new Error(
    "Set EVENER_NATIVE_AUTH_ORIGIN to the owned loopback fixture.",
  );
const control = (path: string) => `${origin}${path}`;
const c = createHubClient(
  origin,
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
  if (!list.instances.some((x) => x.name === "work-secondary"))
    throw Error("second Codex instance missing");
  const secondary = await c.request("evener/auth/device/start", {
    provider: "work-secondary",
  });
  const secondaryPending = await c.request("evener/auth/device/poll", {
    provider: "work-secondary",
    flowId: secondary.flowId,
  });
  if (secondaryPending.state !== "pending")
    throw Error("secondary not pending");
  await assert.rejects(
    c.request("evener/auth/device/poll", {
      provider: "work",
      flowId: secondary.flowId,
    }),
  );
  await fetch(
    control(`/approve?code=${encodeURIComponent(secondary.userCode)}`),
    {
      method: "POST",
    },
  );
  const secondaryResult = await c.request("evener/auth/device/poll", {
    provider: "work-secondary",
    flowId: secondary.flowId,
  });
  if (secondaryResult.state !== "authorized")
    throw Error("secondary not authorized");
  await c.request("evener/auth/logout", { provider: "work-secondary" });
  const start = await c.request("evener/auth/device/start", {
    provider: "work",
  });
  const pending = await c.request("evener/auth/device/poll", {
    provider: "work",
    flowId: start.flowId,
  });
  if (pending.state !== "pending") throw Error("expected pending");
  await fetch(control(`/approve?code=${encodeURIComponent(start.userCode)}`), {
    method: "POST",
  });
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
  const expire = await fetch(control("/clock/expire"), {
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
  const fault = await fetch(
    control(`/fault/poll?code=${encodeURIComponent(retrying.userCode)}`),
    {
      method: "POST",
    },
  );
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
  const clear = await fetch(
    control(`/fault/clear?code=${encodeURIComponent(retrying.userCode)}`),
    {
      method: "POST",
    },
  );
  assert.equal(clear.status, 204, "fixture failure recovery control");
  await fetch(
    control(`/approve?code=${encodeURIComponent(retrying.userCode)}`),
    { method: "POST" },
  );
  const retried = await c.request("evener/auth/device/poll", {
    provider: "work",
    flowId: retrying.flowId,
  });
  assert.equal(retried.state, "authorized");
  assert.equal(retried.status?.activeSource, "oauth");
  await c.request("evener/auth/logout", { provider: "work" });
  console.log({ expiry: "expired", sameFlowRetry: "authorized" });
  await fetch(control("/mode/browser"), { method: "POST" });
  const fallback = await c.request("evener/auth/device/start", {
    provider: "work",
  });
  if (!fallback.fallback) throw Error("no fallback");
  const browser = await c.request("evener/auth/login/start", {
    provider: "work",
  });
  const browserPage = await fetch(browser.url);
  assert.equal(browserPage.status, 200, "fixture browser authorization page");
  const page = await browserPage.text();
  const match = page.match(/<textarea[^>]*>([\s\S]*?)<\/textarea>/i);
  if (!match) throw Error("browser page did not expose redirect metadata");
  const redirectUrl = match[1]
    .replaceAll("&amp;", "&")
    .replaceAll("&lt;", "<")
    .replaceAll("&gt;", ">");
  const complete = await c.request("evener/auth/login/complete", {
    provider: "work",
    flowId: browser.flowId,
    redirectUrl,
  });
  if (complete.status.activeSource !== "oauth")
    throw Error("browser not stored");
  console.log({ browser: complete.status.activeSource });
  await c.request("evener/auth/logout", { provider: "work" });
  await fetch(control("/mode/device"), { method: "POST" });
  console.log(await (await fetch(control("/status?provider=work"))).json());
} finally {
  c.close();
}
