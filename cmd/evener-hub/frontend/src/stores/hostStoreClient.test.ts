// @vitest-environment node

import type { AnyNotification } from "@evener/appwire-client";
import { FakeClient } from "@evener/appwire-client/testing/fakeClient";
import { beforeEach, expect, test } from "vitest";
import { connectionStore } from "./connection";
import { remoteHostStoreClient } from "./hostStoreClient";

// The store-boundary port a REMOTE host's package stores take (component 07b):
// every request goes through evener/host/request, and only that host's own
// notifications are delivered (unwrapped). There is no controller fallback.

function connectFakeClient(): FakeClient {
  const fake = new FakeClient("ready");
  connectionStore.getState().connect(fake);
  return fake;
}

beforeEach(() => {
  connectionStore.setState({ state: "idle", serverInfo: undefined, client: null });
});

test("forwards every request through evener/host/request with the bound host", async () => {
  const fake = connectFakeClient();
  fake.on("evener/host/request", () => ({ data: [] }) as never);

  await remoteHostStoreClient("beta").request("model/list", {});

  expect(fake.calls).toEqual([
    { method: "evener/host/request", params: { host: "beta", method: "model/list", params: {} } },
  ]);
});

test("rejects instead of reaching a controller method when no client is connected", async () => {
  await expect(remoteHostStoreClient("beta").request("model/list", {})).rejects.toThrow(/no client connected/);
});

test("delivers only the frames wrapped for its host, unwrapped to the plain method", () => {
  const fake = connectFakeClient();
  const seen: AnyNotification[] = [];
  remoteHostStoreClient("beta").onNotification((n) => seen.push(n));

  fake.emitNotification({
    method: "evener/host/notification",
    params: { host: "gamma", method: "evener/plugin/updated", params: {} },
  } as AnyNotification);
  fake.emitNotification({
    method: "evener/host/notification",
    params: { host: "beta", method: "evener/plugin/updated", params: { revision: 1 } },
  } as AnyNotification);
  // The controller's own plain frame is not this host's change.
  fake.emitNotification({ method: "evener/plugin/updated", params: {} } as AnyNotification);

  expect(seen).toEqual([{ method: "evener/plugin/updated", params: { revision: 1 } }]);
});

// A caller's per-call deadline is what the LOCAL port's own request honours
// (AppwireClient.request's opts). The remote port dropped it on the floor: the
// forwarded call then ran under the client's DEFAULT_REQUEST_TIMEOUT_MS, so a
// caller who asked for a longer budget (stores/hosts.ts's own HOST_GATE_TIMEOUT_MS
// is 35 minutes) silently got the 30s default instead - the failure mode of
// timing out under a budget that was never honoured. The deadline now rides the
// one call that actually carries the caller's wait, evener/host/request itself.
test("a caller's timeoutMs rides the forwarded call rather than being dropped", async () => {
  const fake = connectFakeClient();
  fake.on("evener/host/request", () => ({ data: [] }) as never);

  await remoteHostStoreClient("beta").request("model/list", {}, { timeoutMs: 1234 });

  expect(fake.calls).toEqual([
    {
      method: "evener/host/request",
      params: { host: "beta", method: "model/list", params: {} },
      opts: { timeoutMs: 1234 },
    },
  ]);
});
