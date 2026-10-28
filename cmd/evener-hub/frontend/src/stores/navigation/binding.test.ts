import { APPWIRE_PROTOCOL_VERSION } from "@evener/appwire-client";
import { FakeClient } from "@evener/appwire-client/testing/fakeClient";
import { capability, wireSnapshot } from "@evener/appwire-client/testing/navigation";
import { afterEach, expect, test, vi } from "vitest";
import { connectionStore } from "../connection";
import { bindNavigationConnection } from "./binding";
import { navigationStore, resetNavigationStoreForTests } from "./store";

const cleanups: Array<() => void> = [];
afterEach(() => {
  for (const cleanup of cleanups.splice(0)) cleanup();
  connectionStore.setState({ client: null, state: "idle", serverInfo: undefined });
  resetNavigationStoreForTests();
  vi.restoreAllMocks();
});

function readyClient() {
  const client = new FakeClient("ready");
  client.scriptConnect(() => ({
    serverInfo: { name: "fake", version: "1" },
    protocolVersion: APPWIRE_PROTOCOL_VERSION,
    sourceId: "fake",
    features: {} as never,
    navigation: capability("binding-generation"),
  }));
  client.on("evener/navigation/read", (params) => {
    if (params.resource !== "manifest") throw new Error(`unexpected navigation read: ${params.resource}`);
    return wireSnapshot(
      params,
      {
        generation_id: "binding-generation",
        revision: 1,
        sources: [],
        attentionSummary: { needsYou: 0, error: 0, working: 0 },
        sections: { live: { count: 0 }, needs_you: { count: 0 }, pin_sections: { count: 0 } },
        catalogs: { projects: { count: 0 }, archived_projects: { count: 0 }, test_runs: { count: 0 } },
      },
      '"binding"',
    );
  });
  return client;
}

test("initializes navigation on ready without starting notifications", async () => {
  const client = readyClient();
  const unsubscribe = bindNavigationConnection();
  cleanups.push(unsubscribe);
  connectionStore.getState().connect(client);
  await client.connect();
  await vi.waitFor(() => expect(navigationStore.getState().mode).toBe("v3"));
  expect(client.calls).toEqual([
    { method: "evener/navigation/read", params: { resource: "manifest", representationVersion: 3 } },
  ]);
});

test("adopts an already-ready client and stops after teardown", async () => {
  const first = readyClient();
  connectionStore.getState().connect(first);
  await first.connect();
  const unsubscribe = bindNavigationConnection();
  cleanups.push(unsubscribe);
  await vi.waitFor(() => expect(navigationStore.getState().mode).toBe("v3"));
  unsubscribe();
  cleanups.pop();
  expect(navigationStore.getState().mode).toBe("unknown");
  const next = readyClient();
  connectionStore.getState().connect(next);
  await next.connect();
  expect(next.calls).toEqual([]);
});

test("retains navigation state when the same client reconnects", async () => {
  const client = readyClient();
  const unsubscribe = bindNavigationConnection();
  cleanups.push(unsubscribe);
  connectionStore.getState().connect(client);
  await client.connect();
  await vi.waitFor(() => expect(navigationStore.getState().mode).toBe("v3"));
  const manifest = navigationStore.getState().manifest;
  client.emitStateChange("reconnecting");
  client.emitReady();
  expect(navigationStore.getState().mode).toBe("v3");
  expect(navigationStore.getState().manifest).toBe(manifest);
});
