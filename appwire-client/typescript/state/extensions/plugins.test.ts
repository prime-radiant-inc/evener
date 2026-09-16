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

  test("getInitialState is the state the store was created with, and setState notifies with new and previous", () => {
    const { store } = storeWithFake();
    const initial = store.getInitialState();
    const seen: Array<[boolean, boolean]> = [];
    store.subscribe((state, previous) => seen.push([state.pluginsLoading, previous.pluginsLoading]));

    store.setState({ pluginsLoading: true });
    expect(seen).toEqual([[true, false]]);
    expect(store.getInitialState()).toBe(initial);
    expect(initial.pluginsLoading).toBe(false);
    expect(initial.pluginRevision).toBe(0);
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
    // The outrun fetch's loading flag belongs to it as much as its list does.
    expect(store.getState().pluginsLoading).toBe(true);
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

  test("a store that never started ignores the notification", async () => {
    const { fake, store } = storeWithFake();
    fake.on(LIST, () => ({ plugins: [LINTER] }));
    fake.emitNotification({ method: "evener/plugin/updated", params: {} });
    await vi.advanceTimersByTimeAsync(PLUGIN_REFETCH_DEBOUNCE_MS);
    expect(fake.calls).toHaveLength(0);
    expect(store.getState().pluginRevision).toBe(0);
  });

  test("an unrelated notification moves nothing", async () => {
    const { fake, store } = storeWithFake();
    store.start();
    fake.emitNotification({ method: "evener/marketplace/updated", params: {} });
    await vi.advanceTimersByTimeAsync(PLUGIN_REFETCH_DEBOUNCE_MS);
    expect(fake.calls).toHaveLength(0);
    expect(store.getState().pluginRevision).toBe(0);
  });

  test("dispose() unsubscribes, cancels a pending refetch and fences replies in flight", async () => {
    const { fake, store } = storeWithFake();
    store.start();
    fake.on(LIST, () => ({ plugins: [LINTER] }));
    fake.emitNotification({ method: "evener/plugin/updated", params: {} });
    const release = deferRequest<ListResult>(fake, LIST);
    const fetching = store.getState().fetchPlugins();
    await Promise.resolve();
    const before = store.getState();

    store.dispose();
    await vi.advanceTimersByTimeAsync(PLUGIN_REFETCH_DEBOUNCE_MS);
    fake.emitNotification({ method: "evener/plugin/updated", params: {} });
    await vi.advanceTimersByTimeAsync(PLUGIN_REFETCH_DEBOUNCE_MS);
    release({ plugins: [LINTER] });
    await fetching;

    expect(store.getState()).toBe(before);
    expect(fake.calls.filter((c) => c.method === LIST)).toHaveLength(1);
    store.start(); // refused after dispose
    fake.emitNotification({ method: "evener/plugin/updated", params: {} });
    expect(store.getState()).toBe(before);
  });
});

describe("dispose fences mutations", () => {
  test("a mutation that resolves after dispose() publishes nothing", async () => {
    const { fake, store } = storeWithFake();
    fake.on(LIST, () => ({ plugins: [LINTER] }));
    await store.getState().fetchPlugins();
    const release = deferRequest<ListResult>(fake, "evener/plugin/remove");
    const removing = store.getState().removePlugin("linter", "acme");
    await Promise.resolve();
    const before = store.getState();
    let notified = 0;
    store.subscribe(() => {
      notified += 1;
    });

    store.dispose();
    release({ plugins: [] });
    await removing;

    expect(notified).toBe(0);
    expect(store.getState()).toBe(before);
    expect(store.getState().plugins).toEqual([LINTER]);
  });
});

describe("reset", () => {
  test("reset() returns to the initial state and fences the list still in flight", async () => {
    const { fake, store } = storeWithFake();
    const release = deferRequest<ListResult>(fake, LIST);
    const fetching = store.getState().fetchPlugins();
    await Promise.resolve();

    store.reset();
    expect(store.getState()).toMatchObject({
      plugins: null,
      pluginsLoading: false,
      pluginsError: null,
      pluginRevision: 0,
    });

    release({ plugins: [LINTER] });
    await fetching;
    expect(store.getState().plugins).toBeNull();
    expect(store.getState().pluginsLoading).toBe(false);

    // The store keeps working after a reset.
    fake.on(LIST, () => ({ plugins: [FORMATTER] }));
    await store.getState().fetchPlugins();
    expect(store.getState().plugins).toEqual([FORMATTER]);
  });
});
