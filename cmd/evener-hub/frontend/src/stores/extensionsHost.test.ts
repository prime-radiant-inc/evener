import type { MarketplaceEntry } from "@evener/appwire-client";
import { FakeClient } from "@evener/appwire-client/testing/fakeClient";
import { afterEach, beforeEach, expect, test } from "vitest";
import { connectionStore } from "./connection";
import { extensionsInstanceForHost, extensionsStore, resetExtensionsStoreForTests } from "./extensions";

// Host-scoped extensions instances (component 07b): the marketplaces, plugins
// and global launch-layer cores for the selected host. Local is the app's one
// singleton byte-for-byte; a remote host gets its own instance over
// evener/host/request, whose data never lands in the controller's store.

function connectFakeClient(): FakeClient {
  const fake = new FakeClient("ready");
  connectionStore.getState().connect(fake);
  return fake;
}

const MARKETPLACE: MarketplaceEntry = {
  name: "acme-plugins",
  source: { kind: "github", repo: "acme/plugins" },
  lastUpdated: 1000,
};

beforeEach(() => {
  connectionStore.setState({ state: "idle", serverInfo: undefined, client: null });
  resetExtensionsStoreForTests();
});

afterEach(() => {
  resetExtensionsStoreForTests();
  connectionStore.setState({ state: "idle", serverInfo: undefined, client: null });
});

test("the local hub resolves to the controller's own store and issues the plain call", async () => {
  const fake = connectFakeClient();
  fake.on("evener/marketplace/list", () => ({ marketplaces: [MARKETPLACE] }));
  fake.on("evener/host/request", () => {
    throw new Error("the local hub must never route through the proxy");
  });

  expect(extensionsInstanceForHost("local").store).toBe(extensionsStore);
  expect(extensionsInstanceForHost(undefined).store).toBe(extensionsStore);

  await extensionsInstanceForHost("local").store.getState().fetchMarketplaces();
  expect(fake.calls).toEqual([{ method: "evener/marketplace/list", params: {} }]);
});

test("a remote host's marketplace list is read through evener/host/request", async () => {
  const fake = connectFakeClient();
  fake.on("evener/host/request", () => ({ marketplaces: [MARKETPLACE] }) as never);

  await extensionsInstanceForHost("beta").store.getState().fetchMarketplaces();

  expect(fake.calls).toEqual([
    { method: "evener/host/request", params: { host: "beta", method: "evener/marketplace/list", params: {} } },
  ]);
});

test("a remote host's data never lands in the controller's own store", async () => {
  const fake = connectFakeClient();
  fake.on("evener/host/request", () => ({ marketplaces: [MARKETPLACE] }) as never);

  const beta = extensionsInstanceForHost("beta");
  expect(beta.store).not.toBe(extensionsStore);

  await beta.store.getState().fetchMarketplaces();

  expect(beta.store.getState().marketplaces).toEqual([MARKETPLACE]);
  expect(extensionsStore.getState().marketplaces).toBeNull();
});

test("a remote plugin mutation goes through the proxy, never this hub's plugins", async () => {
  const fake = connectFakeClient();
  fake.on("evener/host/request", () => ({ plugins: [] }) as never);
  fake.on("evener/plugin/install", () => {
    throw new Error("a remote selection must not mutate this hub's plugins");
  });

  await extensionsInstanceForHost("beta").store.getState().installPlugin("linter", "acme-plugins");

  expect(fake.calls.map((call) => call.method)).toEqual(["evener/host/request"]);
  expect(fake.calls[0]?.params).toEqual({
    host: "beta",
    method: "evener/plugin/install",
    params: { plugin: "linter", marketplace: "acme-plugins" },
  });
});

test("a remote launch-layer write goes through the proxy, never this hub's setLayer", async () => {
  const fake = connectFakeClient();
  fake.on("evener/host/request", () => ({ effective: {}, layers: {}, provenance: {} }) as never);
  fake.on("evener/launch/setLayer", () => {
    throw new Error("a remote selection must not write this hub's launch layer");
  });

  await extensionsInstanceForHost("beta")
    .store.getState()
    .setLaunchLayer({ skillsDirs: ["/opt/skills"] });

  expect(fake.calls).toEqual([
    {
      method: "evener/host/request",
      params: {
        host: "beta",
        method: "evener/launch/setLayer",
        params: { cwd: "/", layer: "global", config: { skillsDirs: ["/opt/skills"] } },
      },
    },
  ]);
});

test("a remote host's path helpers go through the proxy too", async () => {
  const fake = connectFakeClient();
  fake.on("evener/host/request", () => ({ path: "/opt", valid: true }) as never);

  await extensionsInstanceForHost("beta").store.getState().validatePath("/opt", "dir");

  expect(fake.calls).toEqual([
    {
      method: "evener/host/request",
      params: { host: "beta", method: "evener/path/validate", params: { path: "/opt", kind: "dir" } },
    },
  ]);
});

test("resetExtensionsStoreForTests drops a per-host instance so it is rebuilt fresh", () => {
  const first = extensionsInstanceForHost("beta").store;
  resetExtensionsStoreForTests();
  const second = extensionsInstanceForHost("beta").store;
  expect(second).not.toBe(first);
});
