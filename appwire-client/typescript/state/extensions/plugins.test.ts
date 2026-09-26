import { afterEach, beforeEach, describe, expect, test, vi } from "vitest";
import { deferRequest, FakeClient, failing } from "../../testing/fakeClient";
import type { PluginEntry } from "../../types.gen";
import { createPluginsStore, PLUGIN_REFETCH_DEBOUNCE_MS, type PluginsStore } from "./plugins";

const LINTER: PluginEntry = {
  plugin: "linter",
  marketplace: "acme",
  version: "1.0.0",
  enabled: true,
  autoUpgrade: false,
  broken: false,
  installPath: "/state/plugins/linter",
  installedAt: 1000,
  lastUpdated: 1000,
};
const FORMATTER: PluginEntry = { ...LINTER, plugin: "formatter", installPath: "/state/plugins/formatter" };

const LIST = "evener/plugin/list";
type ListResult = { plugins: PluginEntry[] };

function storeWithFake() {
  const fake = new FakeClient("ready");
  return { fake, store: createPluginsStore(fake) };
}

describe("store shape", () => {
  test("two stores share nothing: lists and errors stay with their own instance", async () => {
    const first = storeWithFake();
    first.fake.on(LIST, () => ({ plugins: [LINTER] }));
    const second = storeWithFake();
    second.fake.on(LIST, failing("boom"));

    await first.store.getState().fetchPlugins();
    await second.store.getState().fetchPlugins();

    expect(first.store.getState().plugins).toEqual([LINTER]);
    expect(first.store.getState().pluginsError).toBeNull();
    expect(second.store.getState().plugins).toBeNull();
    expect(second.store.getState().pluginsError).toBe("boom");

    first.store.reset();
    expect(first.store.getState().plugins).toBeNull();
    expect(second.store.getState().pluginsError).toBe("boom");
  });
});

describe("fetches never throw, mutations reject", () => {
  test("a failed list records its error in state, keeps the plugins it knew and resolves", async () => {
    const { fake, store } = storeWithFake();
    fake.on(LIST, () => ({ plugins: [LINTER] }));
    await store.getState().fetchPlugins();
    fake.on(LIST, failing("network down"));
    await expect(store.getState().fetchPlugins()).resolves.toBeUndefined();
    expect(store.getState()).toMatchObject({
      plugins: [LINTER],
      pluginsLoading: false,
      pluginsError: "network down",
    });
  });

  test("a list that succeeds clears the error a failed one left", async () => {
    const { fake, store } = storeWithFake();
    fake.on(LIST, failing("network down"));
    await store.getState().fetchPlugins();
    fake.on(LIST, () => ({ plugins: [LINTER] }));
    await store.getState().fetchPlugins();
    expect(store.getState()).toMatchObject({ plugins: [LINTER], pluginsLoading: false, pluginsError: null });
  });

  test.each([
    ["installPlugin", (s: PluginsStore) => s.getState().installPlugin("linter", "acme"), "evener/plugin/install"],
    ["upgradePlugin", (s: PluginsStore) => s.getState().upgradePlugin("linter", "acme"), "evener/plugin/upgrade"],
    ["removePlugin", (s: PluginsStore) => s.getState().removePlugin("linter", "acme"), "evener/plugin/remove"],
    ["enablePlugin", (s: PluginsStore) => s.getState().enablePlugin("linter", "acme"), "evener/plugin/enable"],
    ["disablePlugin", (s: PluginsStore) => s.getState().disablePlugin("linter", "acme"), "evener/plugin/disable"],
    [
      "setPluginAutoUpgrade",
      (s: PluginsStore) => s.getState().setPluginAutoUpgrade("linter", "acme", true),
      "evener/plugin/setAutoUpgrade",
    ],
  ] as const)("%s rejects with the hub's error and leaves the list alone", async (_name, mutate, method) => {
    const { fake, store } = storeWithFake();
    fake.on(LIST, () => ({ plugins: [LINTER] }));
    await store.getState().fetchPlugins();
    fake.on(method, failing("mutation failed"));
    await expect(mutate(store)).rejects.toThrow("mutation failed");
    expect(store.getState().plugins).toEqual([LINTER]);
    expect(store.getState().pluginsError).toBeNull();
  });

  test("every mutation carries {plugin, marketplace} as sent and replaces the list with its response", async () => {
    const { fake, store } = storeWithFake();
    const target = { plugin: "linter", marketplace: "acme" };
    const listAfter = (plugins: PluginEntry[]) => () => ({ plugins });
    fake.on("evener/plugin/install", listAfter([LINTER]));
    fake.on("evener/plugin/upgrade", listAfter([{ ...LINTER, version: "1.1.0" }]));
    fake.on("evener/plugin/disable", listAfter([{ ...LINTER, enabled: false }]));
    fake.on("evener/plugin/enable", listAfter([LINTER]));
    fake.on("evener/plugin/setAutoUpgrade", listAfter([{ ...LINTER, autoUpgrade: true }]));
    fake.on("evener/plugin/remove", listAfter([]));

    const state = store.getState();
    await state.installPlugin("linter", "acme");
    expect(store.getState().plugins).toEqual([LINTER]);
    await state.upgradePlugin("linter", "acme");
    expect(store.getState().plugins).toEqual([{ ...LINTER, version: "1.1.0" }]);
    await state.disablePlugin("linter", "acme");
    expect(store.getState().plugins).toEqual([{ ...LINTER, enabled: false }]);
    await state.enablePlugin("linter", "acme");
    await state.setPluginAutoUpgrade("linter", "acme", true);
    expect(store.getState().plugins).toEqual([{ ...LINTER, autoUpgrade: true }]);
    await state.removePlugin("linter", "acme");
    expect(store.getState().plugins).toEqual([]);

    expect(fake.calls.map((c) => [c.method, c.params])).toEqual([
      ["evener/plugin/install", target],
      ["evener/plugin/upgrade", target],
      ["evener/plugin/disable", target],
      ["evener/plugin/enable", target],
      ["evener/plugin/setAutoUpgrade", { ...target, autoUpgrade: true }],
      ["evener/plugin/remove", target],
    ]);
  });
});

describe("list ordering", () => {
  test("a list that resolves after a newer mutation committed does not roll the list back", async () => {
    const { fake, store } = storeWithFake();
    const release = deferRequest<ListResult>(fake, LIST);
    const fetching = store.getState().fetchPlugins();
    await Promise.resolve();
    fake.on("evener/plugin/remove", () => ({ plugins: [] }));
    await store.getState().removePlugin("linter", "acme");
    expect(store.getState().plugins).toEqual([]);

    release({ plugins: [LINTER] });
    await fetching;
    expect(store.getState().plugins).toEqual([]);
    // The outrun fetch writes none of its three fields, the flag it raised
    // included; the mutation that outran it answers all three.
    expect(store.getState().pluginsLoading).toBe(false);
  });

  test("an older list that resolves after a newer one does not overwrite it, and a later failure keeps the newer list", async () => {
    const { fake, store } = storeWithFake();
    const releaseOld = deferRequest<ListResult>(fake, LIST);
    const older = store.getState().fetchPlugins();
    await Promise.resolve();
    fake.on(LIST, () => ({ plugins: [FORMATTER] }));
    await store.getState().fetchPlugins();
    releaseOld({ plugins: [LINTER] });
    await older;
    expect(store.getState().plugins).toEqual([FORMATTER]);

    fake.on(LIST, failing("offline"));
    await store.getState().fetchPlugins();
    expect(store.getState()).toMatchObject({ plugins: [FORMATTER], pluginsError: "offline", pluginsLoading: false });
  });
});

describe("reconnect", () => {
  // pluginRevision is what a host re-keys data it derives from the plugin set
  // on. A connection that was away may have missed any number of
  // evener/plugin/updated, so a ready connection moves it exactly as one of
  // those would - the set is no more known to be unchanged than it is after a
  // notification that names nothing.
  // A client that is already reconnecting when the store first meets it has
  // been ready before - that is what reconnecting means - so the ready that
  // follows is a return, not an arrival. A store built by a lazily loaded
  // module during a flap would otherwise treat it as its first connection and
  // invalidate nothing.
  test("a client already reconnecting when the store meets it has been ready before", async () => {
    const { fake, store } = storeWithFake();
    store.connectionChanged(fake, "reconnecting");
    fake.on(LIST, () => ({ plugins: [LINTER] }));
    await store.getState().fetchPlugins();
    expect(store.getState().pluginRevision).toBe(0);

    store.connectionChanged(fake, "ready");
    expect(store.getState().pluginRevision).toBe(1);
  });

  test("a ready connection moves the revision the same way a notification does", async () => {
    const { fake, store } = storeWithFake();
    store.connectionChanged(fake, "ready");
    fake.on(LIST, () => ({ plugins: [LINTER] }));
    await store.getState().fetchPlugins();
    expect(store.getState().pluginRevision).toBe(0);

    store.connectionChanged(fake, "reconnecting");
    store.connectionChanged(fake, "ready");
    expect(store.getState().pluginRevision).toBe(1);
  });
});

describe("a write that outruns a read", () => {
  test("the mutation's list clears the loading flag the outrun read raised", async () => {
    const { fake, store } = storeWithFake();
    const releaseRead = deferRequest<ListResult>(fake, LIST);
    const reading = store.getState().fetchPlugins();
    await Promise.resolve();
    expect(store.getState().pluginsLoading).toBe(true);

    fake.on("evener/plugin/remove", () => ({ plugins: [FORMATTER] }));
    await store.getState().removePlugin("linter", "acme");

    // The read is fenced by the write, so it writes none of its three fields
    // when it lands - including the flag it raised on its way out. The write
    // answers the same question with a newer list, so it owns all three.
    releaseRead({ plugins: [LINTER] });
    await reading;
    expect(store.getState()).toMatchObject({
      plugins: [FORMATTER],
      pluginsLoading: false,
      pluginsError: null,
    });
  });

  test("the mutation's list clears an error a failed read left", async () => {
    const { fake, store } = storeWithFake();
    fake.on(LIST, failing("offline"));
    await store.getState().fetchPlugins();
    expect(store.getState().pluginsError).toBe("offline");

    fake.on("evener/plugin/install", () => ({ plugins: [LINTER] }));
    await store.getState().installPlugin("linter", "acme");
    expect(store.getState()).toMatchObject({ plugins: [LINTER], pluginsError: null, pluginsLoading: false });
  });
});

describe("notifications", () => {
  beforeEach(() => {
    vi.useFakeTimers();
  });
  afterEach(() => {
    vi.useRealTimers();
  });

  test("start() follows evener/plugin/updated: pluginRevision moves at once, the list is refetched after the debounce", async () => {
    const { fake, store } = storeWithFake();
    fake.on(LIST, () => ({ plugins: [LINTER] }));
    await store.getState().fetchPlugins();
    store.start();
    store.start(); // idempotent: one subscription, one refetch
    fake.on(LIST, () => ({ plugins: [LINTER, FORMATTER] }));

    fake.emitNotification({ method: "evener/plugin/updated", params: {} });
    expect(store.getState().pluginRevision).toBe(1);
    await vi.advanceTimersByTimeAsync(PLUGIN_REFETCH_DEBOUNCE_MS - 1);
    expect(store.getState().plugins).toEqual([LINTER]);
    fake.emitNotification({ method: "evener/plugin/updated", params: {} });
    expect(store.getState().pluginRevision).toBe(2);
    await vi.advanceTimersByTimeAsync(PLUGIN_REFETCH_DEBOUNCE_MS - 1);
    expect(store.getState().plugins).toEqual([LINTER]); // the second notification reset the window
    await vi.advanceTimersByTimeAsync(1);
    expect(store.getState().plugins).toEqual([LINTER, FORMATTER]);
    expect(store.getState().pluginRevision).toBe(2);
    expect(fake.calls.filter((c) => c.method === LIST)).toHaveLength(2);
  });
});
