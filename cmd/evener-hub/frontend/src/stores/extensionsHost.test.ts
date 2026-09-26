// @vitest-environment node

import type { HostRow, MarketplaceEntry } from "@evener/appwire-client";
import { FakeClient } from "@evener/appwire-client/testing/fakeClient";
import { afterEach, beforeEach, expect, test } from "vitest";
import { connectionStore } from "./connection";
import { extensionsInstanceForHost, extensionsStore, resetExtensionsStoreForTests } from "./extensions";
import { hostsStore } from "./hosts";

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

function hostRow(overrides: Partial<HostRow> & Pick<HostRow, "name">): HostRow {
  return { origin: "sidecar", attached: false, midAttach: false, removed: false, ...overrides };
}

beforeEach(() => {
  connectionStore.setState({ state: "idle", serverInfo: undefined, client: null });
  resetExtensionsStoreForTests();
  hostsStore.getState().resetForTests();
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

// A host removed and re-added under the same name is a different registration:
// its marketplaces, plugins and launch layer are its own, and the previous
// registration's cached catalog must not be served for it (nor a response the
// old registration had in flight land in it). The instance is keyed on the
// registry's own row, so the re-registration builds a fresh one.
test("a host re-registered under the same name is rebuilt, never served the previous registration's data", async () => {
  const fake = connectFakeClient();
  let reads = 0;
  fake.on("evener/host/request", () => {
    reads += 1;
    return { marketplaces: [{ ...MARKETPLACE, lastUpdated: reads }] } as never;
  });
  hostsStore.setState({ load: { phase: "ready", hosts: [hostRow({ name: "beta", address: "old.example:22" })] } });
  const first = extensionsInstanceForHost("beta");
  await first.store.getState().fetchMarketplaces();
  expect(first.store.getState().marketplaces).toEqual([{ ...MARKETPLACE, lastUpdated: 1 }]);

  hostsStore.setState({ load: { phase: "ready", hosts: [hostRow({ name: "beta", address: "new.example:22" })] } });

  const second = extensionsInstanceForHost("beta");
  expect(second.store).not.toBe(first.store);
  // The new registration starts with nothing: the old one's catalog is not
  // carried over, it is re-read.
  expect(second.store.getState().marketplaces).toBeNull();
  await second.store.getState().fetchMarketplaces();
  expect(second.store.getState().marketplaces).toEqual([{ ...MARKETPLACE, lastUpdated: 2 }]);
  expect(reads).toBe(2);
});

// A pane can resolve a remote host before the registry has answered (a deep
// link mounts before the picker's own list read lands). The first answer
// identifies the instance; it does not replace it and re-read every catalog.
test("a host resolved before the registry answered is kept when the registry identifies it", () => {
  const instance = extensionsInstanceForHost("beta");

  hostsStore.setState({ load: { phase: "ready", hosts: [hostRow({ name: "beta", address: "beta.example:22" })] } });

  expect(extensionsInstanceForHost("beta").store).toBe(instance.store);
});

// The registry re-publishes when an attach or a version report moves, and
// rebuilds an instance for neither: those fields are not the registration.
test("an attach report on an unchanged registration keeps the instance", () => {
  const registered = hostRow({ name: "beta", address: "beta.example:22" });
  hostsStore.setState({ load: { phase: "ready", hosts: [registered] } });
  const instance = extensionsInstanceForHost("beta");

  hostsStore.setState({
    load: { phase: "ready", hosts: [{ ...registered, attached: true, serverVersion: "1.2.3" }] },
  });

  expect(extensionsInstanceForHost("beta").store).toBe(instance.store);
});

// And for the merged marketplaces/plugins/launch-layer instance, whose three
// cores each wire a subscription at start(): a host the registry does not list
// keeps no instance at all - the previous registration's cores are disposed and
// nothing replaces them - and a caller is handed the shared refusing store
// rather than one that would read that host's catalogs.
test("a host the registry reports gone keeps no instance and refuses instead of dialing", async () => {
  const fake = connectFakeClient();
  fake.on("evener/host/request", () => ({ marketplaces: [MARKETPLACE] }) as never);
  hostsStore.setState({ load: { phase: "ready", hosts: [hostRow({ name: "beta", address: "beta.example:22" })] } });
  await extensionsInstanceForHost("beta").store.getState().fetchMarketplaces();
  const dialed = fake.calls.length;

  hostsStore.setState({ load: { phase: "ready", hosts: [] } });

  const gone = extensionsInstanceForHost("beta");
  // A marketplaces FETCH never throws (the core tracks the failure in state),
  // so the refusal is what the call leaves behind - and, above all, that it
  // dialed nothing for a host the registry does not list.
  await gone.store.getState().fetchMarketplaces();
  expect(fake.calls.length).toBe(dialed);
  expect(gone.store.getState().marketplacesError).toMatch(/not registered/);
  expect(extensionsInstanceForHost("beta").store).toBe(gone.store);
});

/** Puts the connection through a reconnect cycle, keeping the client. */
async function reconnect(): Promise<void> {
  connectionStore.setState({ state: "reconnecting" });
  connectionStore.setState({ state: "ready" });
  await Promise.resolve();
  await Promise.resolve();
}

// The same removal, where the leak keeps THREE live cores (marketplaces, plugins
// and the launch layer), each of which `syncHostInstances` keeps feeding
// connectionChanged - so a removed host's instance goes on forwarding
// evener/host/request for a host the registry no longer lists, on every
// reconnect, for the rest of the session. Nothing evicts it lazily: the frame
// renders its own note for a host that is gone instead of the body, so no pane
// calls this accessor for it again.
test("a host removed while a pane is mounted is evicted, so nothing of it survives", async () => {
  const fake = connectFakeClient();
  fake.on("evener/host/request", () => ({ marketplaces: [MARKETPLACE] }) as never);
  hostsStore.setState({ load: { phase: "ready", hosts: [hostRow({ name: "beta", attached: true })] } });
  const mounted = extensionsInstanceForHost("beta");
  await mounted.store.getState().fetchMarketplaces();
  const dialed = fake.calls.length;

  hostsStore.setState({ load: { phase: "ready", hosts: [] } });

  // A wrapped update for that host is not this instance's business any more...
  fake.emitNotification({
    method: "evener/host/notification",
    params: { host: "beta", method: "evener/plugin/updated", params: {} },
  } as never);
  await Promise.resolve();

  // ...and a reconnect issues no request for a host the registry no longer lists.
  await reconnect();
  expect(fake.calls.length).toBe(dialed);
});
