import { afterEach, beforeEach, describe, expect, test, vi } from "vitest";
import { friendlyErrorMessage, WireError } from "../../errors";
import { deferRequest, FakeClient, failing } from "../../testing/fakeClient";
import type { MarketplaceEntry, PluginComponentsResponse, PluginComponentsResult, PluginEntry } from "../../types.gen";
import { REFETCH_DEBOUNCE_MS } from "../refetchDebounce";
import { createHubWriteGate, HubWriteBusyError, type HubWriteGate } from "./hubWriteGate";
import { createMarketplacesStore } from "./marketplaces";
import { createPluginsStore, PLUGIN_UPDATE_CHECK_TIMEOUT_MS, type PluginsStore } from "./plugins";

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
const COMPONENTS = "evener/plugin/components";

const componentRow = (plugin: string): PluginComponentsResult => ({
  plugin,
  marketplace: "acme",
  components: { skills: [`${plugin}:run`] },
});

function storeWithFake(gate: HubWriteGate = createHubWriteGate()) {
  const fake = new FakeClient("ready");
  return { fake, store: createPluginsStore(fake, gate), gate };
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
    expect(second.store.getState().pluginsError).toBe(friendlyErrorMessage(new Error("boom")));

    first.store.reset();
    expect(first.store.getState().plugins).toBeNull();
    expect(second.store.getState().pluginsError).toBe(friendlyErrorMessage(new Error("boom")));
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
      pluginsError: friendlyErrorMessage(new Error("network down")),
    });
  });

  // The list error is translated while it still has its type: a hub's own
  // WireError explanation survives, and a client-side failure gets the shared
  // friendly form. A host translating the stored text would lose the detail.
  test.each([
    ["a hub-reported WireError", new WireError("plugin index is rebuilding", -32000)],
    ["a client-side Error", new Error("AppwireClient internal detail")],
  ])("a failed list stores %s in its friendly form", async (_label, err) => {
    const { fake, store } = storeWithFake();
    fake.on(LIST, () => {
      throw err;
    });
    await store.getState().fetchPlugins();
    expect(store.getState().pluginsError).toBe(friendlyErrorMessage(err));
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

describe("checkPluginUpdates", () => {
  const CHECK = "evener/plugin/checkUpdates";

  test("says whether the hub gave its answer, so a host knows to ask again after a dropped connection", async () => {
    const { fake, store } = storeWithFake();
    fake.on(LIST, () => ({ plugins: [LINTER] }));
    fake.on(CHECK, () => ({ plugins: [LINTER] }));
    expect(await store.getState().checkPluginUpdates()).toBe(true);
    // An older hub's refusal is an answer too; the test below pins it.

    // The request never reached an answer: the connection dropped, or a proxy
    // turned a lost host channel into a wire error.
    fake.on(CHECK, failing("connection closed"));
    expect(await store.getState().checkPluginUpdates()).toBe(false);
    fake.on(CHECK, () => {
      throw new WireError("host m4 is not attached", -32014);
    });
    expect(await store.getState().checkPluginUpdates()).toBe(false);
  });

  test("asks the hub with the long timeout, then re-reads the list, which carries the flags", async () => {
    const { fake, store } = storeWithFake();
    fake.on(LIST, () => ({ plugins: [LINTER] }));
    await store.getState().fetchPlugins();
    fake.on(CHECK, () => ({ plugins: [{ ...LINTER, updateAvailable: true }] }));
    fake.on(LIST, () => ({ plugins: [{ ...LINTER, updateAvailable: true }] }));

    await store.getState().checkPluginUpdates();

    expect(store.getState().plugins).toEqual([{ ...LINTER, updateAvailable: true }]);
    expect(fake.calls.slice(-2)).toEqual([
      { method: CHECK, params: {}, opts: { timeoutMs: PLUGIN_UPDATE_CHECK_TIMEOUT_MS } },
      { method: LIST, params: {} },
    ]);
  });

  test("a check that lands after reset() reads nothing into the reset store", async () => {
    const { fake, store } = storeWithFake();
    const release = deferRequest<ListResult>(fake, CHECK);
    const checking = store.getState().checkPluginUpdates();
    await Promise.resolve();
    store.reset();
    fake.on(LIST, () => ({ plugins: [{ ...LINTER, updateAvailable: true }] }));

    release({ plugins: [{ ...LINTER, updateAvailable: true }] });
    await checking;
    expect(fake.calls.some((c) => c.method === LIST)).toBe(false);
    expect(store.getState().plugins).toBeNull();
  });

  test("a list answer issued during the check does not cost the flags", async () => {
    const { fake, store } = storeWithFake();
    const release = deferRequest<ListResult>(fake, CHECK);
    const checking = store.getState().checkPluginUpdates();
    await Promise.resolve();
    // A toggle lands while the hub is still asking remotes, before it holds
    // any answer, so the toggle's list is unflagged.
    fake.on("evener/plugin/disable", () => ({ plugins: [{ ...LINTER, enabled: false }] }));
    await store.getState().disablePlugin("linter", "acme");

    fake.on(LIST, () => ({ plugins: [{ ...LINTER, enabled: false, updateAvailable: true }] }));
    release({ plugins: [{ ...LINTER, updateAvailable: true }] });
    await checking;
    expect(store.getState().plugins).toEqual([{ ...LINTER, enabled: false, updateAvailable: true }]);
  });

  test("a failed check (an older hub has no such method) resolves and leaves the list and its error alone", async () => {
    const { fake, store } = storeWithFake();
    fake.on(LIST, () => ({ plugins: [LINTER] }));
    await store.getState().fetchPlugins();
    fake.on(CHECK, () => {
      throw new WireError("method not found", -32601);
    });

    await expect(store.getState().checkPluginUpdates()).resolves.toBe(true);

    expect(store.getState()).toMatchObject({ plugins: [LINTER], pluginsError: null, pluginsLoading: false });
  });

  test("a failed check does not swallow a list read started before it", async () => {
    const { fake, store } = storeWithFake();
    const release = deferRequest<ListResult>(fake, LIST);
    const fetching = store.getState().fetchPlugins();
    await Promise.resolve();
    fake.on(CHECK, failing("method not found"));
    await store.getState().checkPluginUpdates();

    release({ plugins: [LINTER] });
    await fetching;
    expect(store.getState()).toMatchObject({ plugins: [LINTER], pluginsLoading: false });
  });
});

describe("the shared hub write gate serializes plugin writes", () => {
  test("a plugin mutation is refused while another write already holds the gate", async () => {
    const gate = createHubWriteGate();
    const { fake, store } = storeWithFake(gate);
    let settle!: () => void;
    const held = new Promise<void>((resolve) => {
      settle = resolve;
    });
    const holding = gate.run(() => held);

    await expect(store.getState().installPlugin("linter", "acme")).rejects.toBeInstanceOf(HubWriteBusyError);
    expect(store.getState().plugins).toBeNull();
    expect(fake.calls).toEqual([]);

    settle();
    await holding;
    fake.on("evener/plugin/install", () => ({ plugins: [LINTER] }));
    await store.getState().installPlugin("linter", "acme");
    expect(store.getState().plugins).toEqual([LINTER]);
  });
});

describe("one gate serializes a plugin write and a marketplace write together", () => {
  test("a plugin mutation is refused while a marketplace write holds the same gate", async () => {
    const gate = createHubWriteGate();
    const pluginsFake = new FakeClient("ready");
    const marketplacesFake = new FakeClient("ready");
    const plugins = createPluginsStore(pluginsFake, gate);
    const marketplaces = createMarketplacesStore(marketplacesFake, gate);

    const release = deferRequest<{ marketplaces: MarketplaceEntry[] }>(marketplacesFake, "evener/marketplace/remove");
    const removing = marketplaces.getState().removeMarketplace("acme");
    await Promise.resolve();

    await expect(plugins.getState().installPlugin("linter", "acme")).rejects.toBeInstanceOf(HubWriteBusyError);
    expect(pluginsFake.calls).toEqual([]);

    release({ marketplaces: [] });
    await removing;
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
    expect(store.getState()).toMatchObject({
      plugins: [FORMATTER],
      pluginsError: friendlyErrorMessage(new Error("offline")),
      pluginsLoading: false,
    });
  });
});

describe("pluginComponents cache", () => {
  test("one batch read lands a loaded entry per pair target under its key", async () => {
    const { fake, store } = storeWithFake();
    fake.on(COMPONENTS, () => ({ results: [componentRow("linter"), componentRow("formatter")] }));
    await store.getState().fetchPluginComponents([
      { plugin: "linter", marketplace: "acme" },
      { plugin: "formatter", marketplace: "acme" },
    ]);
    expect(fake.calls.map((call) => [call.method, call.params])).toEqual([
      [
        COMPONENTS,
        {
          targets: [
            { plugin: "linter", marketplace: "acme" },
            { plugin: "formatter", marketplace: "acme" },
          ],
        },
      ],
    ]);
    expect(store.getState().pluginComponents.get("linter@acme")).toEqual({
      status: "loaded",
      components: { skills: ["linter:run"] },
    });
    expect(store.getState().pluginComponents.get("formatter@acme")).toEqual({
      status: "loaded",
      components: { skills: ["formatter:run"] },
    });
  });

  test("a path target lands its entry under the path: key form", async () => {
    const { fake, store } = storeWithFake();
    fake.on(COMPONENTS, () => ({ results: [{ path: "/opt/plugin", components: { commands: ["fmt"] } }] }));
    await store.getState().fetchPluginComponents([{ path: "/opt/plugin" }]);
    expect(fake.calls.map((call) => call.params)).toEqual([{ targets: [{ path: "/opt/plugin" }] }]);
    expect(store.getState().pluginComponents.get("path:/opt/plugin")).toEqual({
      status: "loaded",
      components: { commands: ["fmt"] },
    });
    expect(store.getState().pluginComponents.size).toBe(1);
  });

  test("a concurrent call for a key still loading waits for the first request instead of sending its own", async () => {
    const { fake, store } = storeWithFake();
    const release = deferRequest<PluginComponentsResponse>(fake, COMPONENTS);
    const first = store.getState().fetchPluginComponents([{ plugin: "linter", marketplace: "acme" }]);
    await Promise.resolve();
    const second = store.getState().fetchPluginComponents([{ plugin: "linter", marketplace: "acme" }]);
    expect(fake.calls.filter((call) => call.method === COMPONENTS)).toHaveLength(1);
    release({ results: [componentRow("linter")] });
    await Promise.all([first, second]);
    expect(fake.calls.filter((call) => call.method === COMPONENTS)).toHaveLength(1);
    expect(store.getState().pluginComponents.get("linter@acme")).toEqual({
      status: "loaded",
      components: { skills: ["linter:run"] },
    });
  });

  test("a broken target's error row lands its key as an error entry while its sibling still loads", async () => {
    const { fake, store } = storeWithFake();
    const loadError = "target-load-error-sentinel";
    fake.on(COMPONENTS, () => ({
      results: [{ plugin: "linter", marketplace: "acme", error: loadError }, componentRow("formatter")],
    }));
    await store.getState().fetchPluginComponents([
      { plugin: "linter", marketplace: "acme" },
      { plugin: "formatter", marketplace: "acme" },
    ]);
    expect(store.getState().pluginComponents.get("linter@acme")).toEqual({
      status: "error",
      source: "target",
      error: loadError,
    });
    expect(store.getState().pluginComponents.get("formatter@acme")).toEqual({
      status: "loaded",
      components: { skills: ["formatter:run"] },
    });
  });

  test("a hub that refuses the whole method (an older hub) lands an error entry for every requested key and does not throw", async () => {
    const { fake, store } = storeWithFake();
    fake.on(COMPONENTS, failing("method not found"));
    const reading = store.getState().fetchPluginComponents([
      { plugin: "linter", marketplace: "acme" },
      { plugin: "formatter", marketplace: "acme" },
    ]);
    await expect(reading).resolves.toBeUndefined();
    const error = expect.any(String);
    expect(store.getState().pluginComponents.get("linter@acme")).toEqual({ status: "error", source: "request", error });
    expect(store.getState().pluginComponents.get("formatter@acme")).toEqual({
      status: "error",
      source: "request",
      error,
    });
  });

  test("reloadPluginComponents drops a settled entry - error or loaded - so a retry re-reads it", async () => {
    const { fake, store } = storeWithFake();
    fake.on(COMPONENTS, failing("method not found"));
    await store.getState().fetchPluginComponents([{ plugin: "linter", marketplace: "acme" }]);
    const error = expect.any(String);
    expect(store.getState().pluginComponents.get("linter@acme")).toEqual({ status: "error", source: "request", error });
    // A plain fetch after the failure re-requests nothing: the settled entry
    // is the store's word on the key.
    await store.getState().fetchPluginComponents([{ plugin: "linter", marketplace: "acme" }]);
    expect(fake.calls.filter((call) => call.method === COMPONENTS)).toHaveLength(1);

    fake.on(COMPONENTS, () => ({ results: [componentRow("linter")] }));
    await store.getState().reloadPluginComponents([{ plugin: "linter", marketplace: "acme" }]);
    expect(fake.calls.filter((call) => call.method === COMPONENTS)).toHaveLength(2);
    expect(store.getState().pluginComponents.get("linter@acme")).toEqual({
      status: "loaded",
      components: { skills: ["linter:run"] },
    });

    // A reload also replaces a settled loaded entry, not only an error one.
    fake.on(COMPONENTS, () => ({ results: [componentRow("formatter")] }));
    await store.getState().reloadPluginComponents([
      { plugin: "linter", marketplace: "acme" },
      { plugin: "formatter", marketplace: "acme" },
    ]);
    expect(fake.calls.filter((call) => call.method === COMPONENTS)).toHaveLength(3);
    expect(store.getState().pluginComponents.get("linter@acme")).toEqual({
      status: "loaded",
      components: { skills: ["formatter:run"] },
    });
  });

  test("an answer that carries no rows lands an error entry for every requested key", async () => {
    const { fake, store } = storeWithFake();
    fake.on(COMPONENTS, () => ({ results: [] }));
    await store.getState().fetchPluginComponents([{ plugin: "linter", marketplace: "acme" }]);
    expect(store.getState().pluginComponents.get("linter@acme")).toEqual({
      status: "error",
      source: "request",
      error: expect.any(String),
    });
  });

  test("an install's updated list retires the cache; the next fetchPlugins re-issues the batch and re-lands the entries", async () => {
    const { fake, store } = storeWithFake();
    fake.on(LIST, () => ({ plugins: [LINTER, FORMATTER] }));
    fake.on(COMPONENTS, () => ({ results: [componentRow("linter"), componentRow("formatter")] }));
    await store.getState().fetchPlugins();
    await store.getState().fetchPluginComponents([{ plugin: "linter", marketplace: "acme" }]);
    expect(store.getState().pluginComponents.size).toBe(2);

    fake.on("evener/plugin/install", () => ({ plugins: [LINTER] }));
    await store.getState().installPlugin("linter", "acme");
    expect(store.getState().pluginComponents.size).toBe(0);

    fake.on(LIST, () => ({ plugins: [LINTER] }));
    await store.getState().fetchPlugins();
    await store.getState().fetchPluginComponents([{ plugin: "linter", marketplace: "acme" }]);
    expect(fake.calls.filter((call) => call.method === COMPONENTS)).toHaveLength(2);
    expect(fake.calls.at(-1)?.params).toEqual({ targets: [{ plugin: "linter", marketplace: "acme" }] });
    expect(store.getState().pluginComponents.get("linter@acme")).toEqual({
      status: "loaded",
      components: { skills: ["linter:run"] },
    });
    expect(store.getState().pluginComponents.has("formatter@acme")).toBe(false);
  });

  test("fetchPlugins landing a fresh list issues one batch read for the list's pair keys and never re-reads settled entries", async () => {
    const { fake, store } = storeWithFake();
    fake.on(LIST, () => ({ plugins: [LINTER, FORMATTER] }));
    fake.on(COMPONENTS, () => ({ results: [componentRow("linter"), componentRow("formatter")] }));
    await store.getState().fetchPlugins();
    await store.getState().fetchPluginComponents([{ plugin: "linter", marketplace: "acme" }]);
    expect(fake.calls.filter((call) => call.method === COMPONENTS)).toEqual([
      {
        method: COMPONENTS,
        params: {
          targets: [
            { plugin: "linter", marketplace: "acme" },
            { plugin: "formatter", marketplace: "acme" },
          ],
        },
      },
    ]);

    await store.getState().fetchPlugins();
    await store.getState().fetchPluginComponents([{ plugin: "linter", marketplace: "acme" }]);
    expect(fake.calls.filter((call) => call.method === COMPONENTS)).toHaveLength(1);
  });

  test.each([
    ["version", { ...LINTER, version: "2.0.0" }, true],
    ["installPath", { ...LINTER, installPath: "/state/plugins/linter-new" }, true],
    ["version and installPath", { ...LINTER, version: "2.0.0", installPath: "/state/plugins/linter-new" }, true],
    ["unrelated metadata", { ...LINTER, enabled: false, autoUpgrade: true, updateAvailable: true }, false],
  ] as const)(
    "a fresh list changing %s refetches only changed backing inventories",
    async (_label, updated, changed) => {
      const { fake, store } = storeWithFake();
      const targets = [
        { plugin: "linter", marketplace: "acme" },
        { plugin: "formatter", marketplace: "acme" },
      ];
      fake.on(LIST, () => ({ plugins: [LINTER, FORMATTER] }));
      fake.on(COMPONENTS, () => ({ results: [componentRow("linter"), componentRow("formatter")] }));
      await store.getState().fetchPlugins();
      await store.getState().fetchPluginComponents(targets);
      const cached = store.getState().pluginComponents;
      expect(cached.get("linter@acme")?.status).toBe("loaded");

      const fresh = { commands: ["linter:check", "linter:fix"] };
      fake.on(LIST, () => ({ plugins: [updated, { ...FORMATTER }] }));
      const release = deferRequest<PluginComponentsResponse>(fake, COMPONENTS);
      // Only a list read reveals the CLI-side change, without a notification or mutation.
      await store.getState().fetchPlugins();
      expect(store.getState().plugins).toEqual([updated, FORMATTER]);
      const reads = fake.calls.filter((call) => call.method === COMPONENTS);
      expect(reads).toHaveLength(changed ? 2 : 1);
      expect(store.getState().pluginComponents.get("formatter@acme")).toBe(cached.get("formatter@acme"));
      if (changed) {
        expect(reads.at(-1)?.params).toEqual({ targets: [targets[0]] });
        expect(store.getState().pluginComponents.get("linter@acme")).toEqual({ status: "loading" });
        release({ results: [{ plugin: "linter", marketplace: "acme", components: fresh }] });
        await store.getState().fetchPluginComponents(targets);
        expect(store.getState().pluginComponents.get("linter@acme")).toEqual({ status: "loaded", components: fresh });
      } else {
        expect(store.getState().pluginComponents.get("linter@acme")).toBe(cached.get("linter@acme"));
      }
      expect(store.getState().pluginRevision).toBe(0);
      expect(fake.calls.every((call) => call.method === LIST || call.method === COMPONENTS)).toBe(true);
      store.dispose();
    },
  );

  test.each([true, false])(
    "a fresh list changing broken from %s retires and refetches the inventory",
    async (broken) => {
      const { fake, store } = storeWithFake();
      const target = { plugin: LINTER.plugin, marketplace: LINTER.marketplace };
      const loadError = "in-place-load-failure-sentinel";
      fake.on(LIST, () => ({ plugins: [{ ...LINTER, broken }, FORMATTER] }));
      fake.on(COMPONENTS, () => ({
        results: [
          broken ? { ...target, error: loadError } : componentRow(LINTER.plugin),
          componentRow(FORMATTER.plugin),
        ],
      }));
      await store.getState().fetchPlugins();
      await store.getState().fetchPluginComponents([target]);
      const before = store.getState().pluginComponents;
      expect(before.get("linter@acme")?.status).toBe(broken ? "error" : "loaded");

      fake.on(LIST, () => ({ plugins: [{ ...LINTER, broken: !broken }, FORMATTER] }));
      const release = deferRequest<PluginComponentsResponse>(fake, COMPONENTS);
      await store.getState().fetchPlugins();
      expect(store.getState().plugins?.[0]).toEqual({ ...LINTER, broken: !broken });
      expect(fake.calls.filter((call) => call.method === COMPONENTS)).toHaveLength(2);
      expect(fake.calls.at(-1)?.params).toEqual({ targets: [target] });
      expect(store.getState().pluginComponents.get("linter@acme")).toEqual({ status: "loading" });
      expect(store.getState().pluginComponents.get("formatter@acme")).toBe(before.get("formatter@acme"));
      const reading = store.getState().fetchPluginComponents([target]);
      const fresh = { commands: ["repaired-command-sentinel"] };
      release({ results: [broken ? { ...target, components: fresh } : { ...target, error: loadError }] });
      await reading;
      expect(store.getState().pluginComponents.get("linter@acme")).toEqual(
        broken ? { status: "loaded", components: fresh } : { status: "error", source: "target", error: loadError },
      );
      store.dispose();
    },
  );

  test("a fresh list fences an old inventory still in flight before refilling its changed revision", async () => {
    const { fake, store } = storeWithFake();
    const target = { plugin: "linter", marketplace: "acme" };
    fake.on(LIST, () => ({ plugins: [LINTER] }));
    const releaseOld = deferRequest<PluginComponentsResponse>(fake, COMPONENTS);
    await store.getState().fetchPlugins();
    const oldReading = store.getState().fetchPluginComponents([target]);

    fake.on(LIST, () => ({ plugins: [{ ...LINTER, installPath: "/state/plugins/linter-new" }] }));
    const releaseFresh = deferRequest<PluginComponentsResponse>(fake, COMPONENTS);
    await store.getState().fetchPlugins();
    expect(fake.calls.filter((call) => call.method === COMPONENTS)).toHaveLength(2);
    const freshReading = store.getState().fetchPluginComponents([target]);
    const fresh = { commands: ["linter:fix"] };
    releaseFresh({ results: [{ ...target, components: fresh }] });
    await freshReading;
    releaseOld({ results: [componentRow("linter")] });
    await oldReading;
    expect(store.getState().pluginComponents.get("linter@acme")).toEqual({ status: "loaded", components: fresh });
    store.dispose();
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
    expect(store.getState().pluginsError).toBe(friendlyErrorMessage(new Error("offline")));

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

  test("a mutation landing after the notification refresh refills from the authoritative list", async () => {
    const { fake, store } = storeWithFake();
    fake.on(LIST, () => ({ plugins: [LINTER] }));
    fake.on(COMPONENTS, () => ({ results: [componentRow("linter")] }));
    await store.getState().fetchPlugins();
    await store.getState().fetchPluginComponents([{ plugin: "linter", marketplace: "acme" }]);
    store.start();
    const release = deferRequest<ListResult>(fake, "evener/plugin/upgrade");
    const writing = store.getState().upgradePlugin("linter", "acme");
    fake.on(LIST, () => ({ plugins: [FORMATTER] }));
    fake.on(COMPONENTS, () => ({ results: [componentRow("formatter")] }));
    fake.emitNotification({ method: "evener/plugin/updated", params: {} });
    await vi.advanceTimersByTimeAsync(REFETCH_DEBOUNCE_MS);
    expect(store.getState().pluginComponents.get("formatter@acme")?.status).toBe("loaded");
    release({ plugins: [LINTER] });
    await writing;
    expect(store.getState().plugins).toEqual([FORMATTER]);
    expect(fake.calls.filter((call) => call.method === COMPONENTS)).toHaveLength(3);
    expect(fake.calls.at(-1)?.params).toEqual({ targets: [{ plugin: "formatter", marketplace: "acme" }] });
    expect(store.getState().pluginComponents.get("formatter@acme")).toEqual({
      status: "loaded",
      components: { skills: ["formatter:run"] },
    });
    expect(store.getState().pluginComponents.has("linter@acme")).toBe(false);
    store.dispose();
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
    await vi.advanceTimersByTimeAsync(REFETCH_DEBOUNCE_MS - 1);
    expect(store.getState().plugins).toEqual([LINTER]);
    fake.emitNotification({ method: "evener/plugin/updated", params: {} });
    expect(store.getState().pluginRevision).toBe(2);
    await vi.advanceTimersByTimeAsync(REFETCH_DEBOUNCE_MS - 1);
    expect(store.getState().plugins).toEqual([LINTER]); // the second notification reset the window
    await vi.advanceTimersByTimeAsync(1);
    expect(store.getState().plugins).toEqual([LINTER, FORMATTER]);
    expect(store.getState().pluginRevision).toBe(2);
    expect(fake.calls.filter((c) => c.method === LIST)).toHaveLength(2);
  });

  test("evener/plugin/updated retires every cached inventory ahead of the debounced refetch", async () => {
    const { fake, store } = storeWithFake();
    fake.on(LIST, () => ({ plugins: [LINTER] }));
    fake.on(COMPONENTS, () => ({ results: [componentRow("linter")] }));
    await store.getState().fetchPlugins();
    await store.getState().fetchPluginComponents([{ plugin: "linter", marketplace: "acme" }]);
    expect(store.getState().pluginComponents.size).toBe(1);

    store.start();
    fake.emitNotification({ method: "evener/plugin/updated", params: {} });
    expect(store.getState().pluginRevision).toBe(1);
    expect(store.getState().pluginComponents.size).toBe(0);
  });
});
