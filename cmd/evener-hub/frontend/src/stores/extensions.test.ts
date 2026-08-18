import { afterEach, beforeEach, describe, expect, test, vi } from "vitest";
import { FakeClient } from "../protocol/testing/fakeClient";
import { threadStartedNotification } from "../protocol/testing/notifications";
import type { MarketplaceCatalogPlugin, MarketplaceEntry, PluginEntry } from "../protocol/types.gen";
import { connectionStore } from "./connection";
import { extensionsStore, resetExtensionsStoreForTests } from "./extensions";

function connectFakeClient(): FakeClient {
  const fake = new FakeClient("ready");
  connectionStore.getState().connect(fake);
  return fake;
}

const MARKETPLACE_A: MarketplaceEntry = {
  name: "acme-plugins",
  source: { kind: "github", repo: "acme/plugins" },
  lastUpdated: 1000,
};

const MARKETPLACE_B: MarketplaceEntry = {
  name: "local-plugins",
  source: { kind: "directory", path: "/opt/plugins-src" },
  lastUpdated: 2000,
};

const PLUGIN_A: PluginEntry = {
  plugin: "linter",
  marketplace: "acme-plugins",
  version: "1.0.0",
  enabled: true,
  autoUpgrade: false,
  broken: false,
  installPath: "/state/plugins/linter",
  installedAt: 1000,
  lastUpdated: 1000,
};

beforeEach(() => {
  connectionStore.setState({ state: "idle", serverInfo: undefined, client: null });
  resetExtensionsStoreForTests();
});

afterEach(() => {
  connectionStore.setState({ state: "idle", serverInfo: undefined, client: null });
  resetExtensionsStoreForTests();
});

describe("fetchMarketplaces", () => {
  test("populates marketplaces from serf/marketplace/list", async () => {
    const fake = connectFakeClient();
    fake.on("serf/marketplace/list", () => ({ marketplaces: [MARKETPLACE_A, MARKETPLACE_B] }));
    await extensionsStore.getState().fetchMarketplaces();
    expect(extensionsStore.getState().marketplaces).toEqual([MARKETPLACE_A, MARKETPLACE_B]);
    expect(extensionsStore.getState().marketplacesLoading).toBe(false);
    expect(extensionsStore.getState().marketplacesError).toBeNull();
  });

  test("marketplacesLoading is true while the request is in flight", async () => {
    const fake = connectFakeClient();
    let resolveRequest!: (v: { marketplaces: MarketplaceEntry[] }) => void;
    fake.on(
      "serf/marketplace/list",
      () =>
        new Promise((resolve) => {
          resolveRequest = resolve;
        }),
    );
    const pending = extensionsStore.getState().fetchMarketplaces();
    expect(extensionsStore.getState().marketplacesLoading).toBe(true);
    // FakeClient.request() defers the handler call by one microtask (mirrors
    // a real RPC round-trip - see its own source comment), so resolveRequest
    // isn't assigned until that microtask runs; flush it before using it.
    await Promise.resolve();
    resolveRequest({ marketplaces: [] });
    await pending;
    expect(extensionsStore.getState().marketplacesLoading).toBe(false);
  });

  test("a rejected request records an error and never throws", async () => {
    const fake = connectFakeClient();
    fake.on("serf/marketplace/list", () => {
      throw new Error("boom");
    });
    await expect(extensionsStore.getState().fetchMarketplaces()).resolves.toBeUndefined();
    expect(extensionsStore.getState().marketplaces).toBeNull();
    expect(extensionsStore.getState().marketplacesError).toBe("boom");
  });

  test("a later successful fetch clears a previous error", async () => {
    const fake = connectFakeClient();
    fake.on("serf/marketplace/list", () => {
      throw new Error("boom");
    });
    await extensionsStore.getState().fetchMarketplaces();
    expect(extensionsStore.getState().marketplacesError).toBe("boom");

    fake.on("serf/marketplace/list", () => ({ marketplaces: [MARKETPLACE_A] }));
    await extensionsStore.getState().fetchMarketplaces();
    expect(extensionsStore.getState().marketplacesError).toBeNull();
    expect(extensionsStore.getState().marketplaces).toEqual([MARKETPLACE_A]);
  });
});

describe("addMarketplace", () => {
  test("calls serf/marketplace/add with the given params and applies the response's marketplaces directly (no separate list round-trip)", async () => {
    const fake = connectFakeClient();
    fake.on("serf/marketplace/add", (params) => {
      expect(params).toEqual({ name: "acme-plugins", source: { kind: "github", repo: "acme/plugins" } });
      return { marketplaces: [MARKETPLACE_A] };
    });
    await extensionsStore
      .getState()
      .addMarketplace({ name: "acme-plugins", source: { kind: "github", repo: "acme/plugins" } });
    expect(extensionsStore.getState().marketplaces).toEqual([MARKETPLACE_A]);
  });

  test("a rejection propagates to the caller (so the section can toast) rather than being swallowed", async () => {
    const fake = connectFakeClient();
    fake.on("serf/marketplace/add", () => {
      throw new Error("add failed");
    });
    await expect(
      extensionsStore.getState().addMarketplace({ source: { kind: "url", url: "https://example.com/x.git" } }),
    ).rejects.toThrow("add failed");
  });
});

describe("removeMarketplace", () => {
  test("calls serf/marketplace/remove with the name and applies the response", async () => {
    const fake = connectFacadeForRemove();
    await extensionsStore.getState().removeMarketplace("acme-plugins");
    expect(fake.calls).toContainEqual({ method: "serf/marketplace/remove", params: { name: "acme-plugins" } });
    expect(extensionsStore.getState().marketplaces).toEqual([MARKETPLACE_B]);

    function connectFacadeForRemove() {
      const client = connectFakeClient();
      client.on("serf/marketplace/remove", () => ({ marketplaces: [MARKETPLACE_B] }));
      return client;
    }
  });

  test("invalidates the browse cache entry for the removed marketplace", async () => {
    const fake = connectFakeClient();
    fake.on("serf/marketplace/browse", () => ({ name: "acme-plugins", plugins: [] }));
    await extensionsStore.getState().browseMarketplace("acme-plugins");
    expect(extensionsStore.getState().browseCatalogs.has("acme-plugins")).toBe(true);

    fake.on("serf/marketplace/remove", () => ({ marketplaces: [] }));
    await extensionsStore.getState().removeMarketplace("acme-plugins");
    expect(extensionsStore.getState().browseCatalogs.has("acme-plugins")).toBe(false);
  });

  test("a rejection propagates to the caller", async () => {
    const fake = connectFakeClient();
    fake.on("serf/marketplace/remove", () => {
      throw new Error("remove failed");
    });
    await expect(extensionsStore.getState().removeMarketplace("acme-plugins")).rejects.toThrow("remove failed");
  });
});

describe("refreshMarketplace", () => {
  test("calls serf/marketplace/refresh, applies the response, and invalidates the browse cache", async () => {
    const fake = connectFakeClient();
    fake.on("serf/marketplace/browse", () => ({ name: "acme-plugins", plugins: [{ name: "linter" }] }));
    await extensionsStore.getState().browseMarketplace("acme-plugins");
    expect(extensionsStore.getState().browseCatalogs.get("acme-plugins")).toEqual({
      status: "loaded",
      description: undefined,
      plugins: [{ name: "linter" }],
    });

    fake.on("serf/marketplace/refresh", (params) => {
      expect(params).toEqual({ name: "acme-plugins" });
      return { marketplaces: [{ ...MARKETPLACE_A, lastUpdated: 9999 }] };
    });
    await extensionsStore.getState().refreshMarketplace("acme-plugins");
    expect(extensionsStore.getState().marketplaces).toEqual([{ ...MARKETPLACE_A, lastUpdated: 9999 }]);
    expect(extensionsStore.getState().browseCatalogs.has("acme-plugins")).toBe(false);
  });

  test("a rejection propagates to the caller", async () => {
    const fake = connectFakeClient();
    fake.on("serf/marketplace/refresh", () => {
      throw new Error("refresh failed");
    });
    await expect(extensionsStore.getState().refreshMarketplace("acme-plugins")).rejects.toThrow("refresh failed");
  });
});

describe("browseMarketplace", () => {
  test("fetches and caches a marketplace's catalog as status:loaded", async () => {
    const fake = connectFakeClient();
    fake.on("serf/marketplace/browse", (params) => {
      expect(params).toEqual({ name: "acme-plugins" });
      return {
        name: "acme-plugins",
        description: "Acme's plugins",
        plugins: [{ name: "linter", category: "quality" }],
      };
    });
    await extensionsStore.getState().browseMarketplace("acme-plugins");
    expect(extensionsStore.getState().browseCatalogs.get("acme-plugins")).toEqual({
      status: "loaded",
      description: "Acme's plugins",
      plugins: [{ name: "linter", category: "quality" }],
    });
  });

  test("caches a failure as status:error with the error message", async () => {
    const fake = connectFakeClient();
    fake.on("serf/marketplace/browse", () => {
      throw new Error("network down");
    });
    await extensionsStore.getState().browseMarketplace("acme-plugins");
    expect(extensionsStore.getState().browseCatalogs.get("acme-plugins")).toEqual({
      status: "error",
      error: "network down",
    });
  });

  test("does not re-fetch an already-cached (loaded) marketplace", async () => {
    const fake = connectFakeClient();
    fake.on("serf/marketplace/browse", () => ({ name: "acme-plugins", plugins: [] }));
    await extensionsStore.getState().browseMarketplace("acme-plugins");
    await extensionsStore.getState().browseMarketplace("acme-plugins");
    expect(fake.calls.filter((c) => c.method === "serf/marketplace/browse")).toHaveLength(1);
  });

  test("does not re-fetch an already-cached (errored) marketplace - only refreshMarketplace invalidates it", async () => {
    const fake = connectFakeClient();
    fake.on("serf/marketplace/browse", () => {
      throw new Error("boom");
    });
    await extensionsStore.getState().browseMarketplace("acme-plugins");
    await extensionsStore.getState().browseMarketplace("acme-plugins");
    expect(fake.calls.filter((c) => c.method === "serf/marketplace/browse")).toHaveLength(1);
  });

  test("marks the entry status:loading synchronously before the request settles, so a concurrent call is a no-op", async () => {
    const fake = connectFakeClient();
    let resolveRequest!: (v: { name: string; plugins: MarketplaceCatalogPlugin[] }) => void;
    fake.on(
      "serf/marketplace/browse",
      () =>
        new Promise((resolve) => {
          resolveRequest = resolve;
        }),
    );
    const first = extensionsStore.getState().browseMarketplace("acme-plugins");
    expect(extensionsStore.getState().browseCatalogs.get("acme-plugins")).toEqual({ status: "loading" });
    const second = extensionsStore.getState().browseMarketplace("acme-plugins");
    // FakeClient.request() defers the handler call by one microtask; flush
    // it before using resolveRequest (see the fetchMarketplaces test above).
    await Promise.resolve();
    resolveRequest({ name: "acme-plugins", plugins: [] });
    await Promise.all([first, second]);
    expect(fake.calls.filter((c) => c.method === "serf/marketplace/browse")).toHaveLength(1);
  });
});

describe("fetchPlugins", () => {
  test("populates plugins from serf/plugin/list", async () => {
    const fake = connectFakeClient();
    fake.on("serf/plugin/list", () => ({ plugins: [PLUGIN_A] }));
    await extensionsStore.getState().fetchPlugins();
    expect(extensionsStore.getState().plugins).toEqual([PLUGIN_A]);
    expect(extensionsStore.getState().pluginsLoading).toBe(false);
    expect(extensionsStore.getState().pluginsError).toBeNull();
  });

  test("a rejected request records an error and never throws", async () => {
    const fake = connectFakeClient();
    fake.on("serf/plugin/list", () => {
      throw new Error("boom");
    });
    await expect(extensionsStore.getState().fetchPlugins()).resolves.toBeUndefined();
    expect(extensionsStore.getState().pluginsError).toBe("boom");
  });
});

describe("plugin mutations", () => {
  test("installPlugin calls serf/plugin/install with {plugin,marketplace} and applies the response", async () => {
    const fake = connectFakeClient();
    fake.on("serf/plugin/install", (params) => {
      expect(params).toEqual({ plugin: "linter", marketplace: "acme-plugins" });
      return { plugins: [PLUGIN_A] };
    });
    await extensionsStore.getState().installPlugin("linter", "acme-plugins");
    expect(extensionsStore.getState().plugins).toEqual([PLUGIN_A]);
  });

  test("upgradePlugin calls serf/plugin/upgrade with {plugin,marketplace} and applies the response", async () => {
    const fake = connectFakeClient();
    fake.on("serf/plugin/upgrade", (params) => {
      expect(params).toEqual({ plugin: "linter", marketplace: "acme-plugins" });
      return { plugins: [{ ...PLUGIN_A, version: "1.1.0" }] };
    });
    await extensionsStore.getState().upgradePlugin("linter", "acme-plugins");
    expect(extensionsStore.getState().plugins).toEqual([{ ...PLUGIN_A, version: "1.1.0" }]);
  });

  test("removePlugin calls serf/plugin/remove with {plugin,marketplace} and applies the response", async () => {
    const fake = connectFakeClient();
    fake.on("serf/plugin/remove", (params) => {
      expect(params).toEqual({ plugin: "linter", marketplace: "acme-plugins" });
      return { plugins: [] };
    });
    await extensionsStore.getState().removePlugin("linter", "acme-plugins");
    expect(extensionsStore.getState().plugins).toEqual([]);
  });

  test("enablePlugin calls serf/plugin/enable with {plugin,marketplace} and applies the response", async () => {
    const fake = connectFakeClient();
    fake.on("serf/plugin/enable", (params) => {
      expect(params).toEqual({ plugin: "linter", marketplace: "acme-plugins" });
      return { plugins: [{ ...PLUGIN_A, enabled: true }] };
    });
    await extensionsStore.getState().enablePlugin("linter", "acme-plugins");
    expect(extensionsStore.getState().plugins).toEqual([{ ...PLUGIN_A, enabled: true }]);
  });

  test("disablePlugin calls serf/plugin/disable with {plugin,marketplace} and applies the response", async () => {
    const fake = connectFakeClient();
    fake.on("serf/plugin/disable", (params) => {
      expect(params).toEqual({ plugin: "linter", marketplace: "acme-plugins" });
      return { plugins: [{ ...PLUGIN_A, enabled: false }] };
    });
    await extensionsStore.getState().disablePlugin("linter", "acme-plugins");
    expect(extensionsStore.getState().plugins).toEqual([{ ...PLUGIN_A, enabled: false }]);
  });

  test("setPluginAutoUpgrade calls serf/plugin/setAutoUpgrade with {plugin,marketplace,autoUpgrade} and applies the response", async () => {
    const fake = connectFakeClient();
    fake.on("serf/plugin/setAutoUpgrade", (params) => {
      expect(params).toEqual({ plugin: "linter", marketplace: "acme-plugins", autoUpgrade: true });
      return { plugins: [{ ...PLUGIN_A, autoUpgrade: true }] };
    });
    await extensionsStore.getState().setPluginAutoUpgrade("linter", "acme-plugins", true);
    expect(extensionsStore.getState().plugins).toEqual([{ ...PLUGIN_A, autoUpgrade: true }]);
  });

  test("a rejected plugin mutation propagates to the caller", async () => {
    const fake = connectFakeClient();
    fake.on("serf/plugin/upgrade", () => {
      throw new Error("upgrade failed");
    });
    await expect(extensionsStore.getState().upgradePlugin("linter", "acme-plugins")).rejects.toThrow("upgrade failed");
  });
});

describe("fetchLaunchLayer", () => {
  test("populates launchLayer from serf/launch/getLayer with cwd '/' and layer 'global'", async () => {
    const fake = connectFakeClient();
    fake.on("serf/launch/getLayer", (params) => {
      expect(params).toEqual({ cwd: "/", layer: "global" });
      return { pluginDirs: ["/opt/plugins"], skillsDirs: [] };
    });
    await extensionsStore.getState().fetchLaunchLayer();
    expect(extensionsStore.getState().launchLayer).toEqual({ pluginDirs: ["/opt/plugins"], skillsDirs: [] });
    expect(extensionsStore.getState().launchLayerLoading).toBe(false);
    expect(extensionsStore.getState().launchLayerError).toBeNull();
  });

  test("a rejected request records an error and never throws", async () => {
    const fake = connectFakeClient();
    fake.on("serf/launch/getLayer", () => {
      throw new Error("boom");
    });
    await expect(extensionsStore.getState().fetchLaunchLayer()).resolves.toBeUndefined();
    expect(extensionsStore.getState().launchLayerError).toBe("boom");
  });
});

describe("setLaunchLayer", () => {
  test("calls serf/launch/setLayer with cwd '/', layer 'global', and the given config", async () => {
    const fake = connectFakeClient();
    fake.on("serf/launch/setLayer", (params) => {
      expect(params).toEqual({ cwd: "/", layer: "global", config: { pluginDirs: ["/opt/plugins"] } });
      return { effective: {}, layers: { global: { pluginDirs: ["/opt/plugins"] } }, provenance: {} };
    });
    await extensionsStore.getState().setLaunchLayer({ pluginDirs: ["/opt/plugins"] });
  });

  test("stores the config it sent as the new launchLayer on success (not a parsed response field)", async () => {
    const fake = connectFakeClient();
    fake.on("serf/launch/setLayer", () => ({
      // Deliberately a DIFFERENT shape than what was sent, to prove the store
      // trusts its own outgoing payload rather than the response's
      // .layers/.effective (see extensions.ts's own comment on why).
      effective: { pluginDirs: ["/should-not-be-used"] },
      layers: { global: { pluginDirs: ["/should-not-be-used-either"] } },
      provenance: {},
    }));
    await extensionsStore.getState().setLaunchLayer({ pluginDirs: ["/opt/plugins"] });
    expect(extensionsStore.getState().launchLayer).toEqual({ pluginDirs: ["/opt/plugins"] });
  });

  test("a rejection propagates to the caller and leaves launchLayer unchanged", async () => {
    const fake = connectFakeClient();
    fake.on("serf/launch/getLayer", () => ({ pluginDirs: ["/existing"] }));
    await extensionsStore.getState().fetchLaunchLayer();

    fake.on("serf/launch/setLayer", () => {
      throw new Error("save failed");
    });
    await expect(extensionsStore.getState().setLaunchLayer({ pluginDirs: ["/existing", "/new"] })).rejects.toThrow(
      "save failed",
    );
    expect(extensionsStore.getState().launchLayer).toEqual({ pluginDirs: ["/existing"] });
  });
});

describe("validatePath", () => {
  test("calls serf/path/validate with the given path and kind and returns the response", async () => {
    const fake = connectFakeClient();
    fake.on("serf/path/validate", (params) => {
      expect(params).toEqual({ path: "/opt/plugins", kind: "dir" });
      return { path: "/opt/plugins", valid: true };
    });
    await expect(extensionsStore.getState().validatePath("/opt/plugins", "dir")).resolves.toEqual({
      path: "/opt/plugins",
      valid: true,
    });
  });

  test("propagates an invalid-path response as-is (valid:false is not a rejection)", async () => {
    const fake = connectFakeClient();
    fake.on("serf/path/validate", () => ({ path: "/nope", valid: false, error: "path does not exist" }));
    await expect(extensionsStore.getState().validatePath("/nope", "dir")).resolves.toEqual({
      path: "/nope",
      valid: false,
      error: "path does not exist",
    });
  });
});

describe("completePaths", () => {
  test("passes the prefix through verbatim, with no trailing-slash normalization", async () => {
    const fake = connectFakeClient();
    fake.on("serf/paths/complete", (params) => {
      expect(params).toEqual({ prefix: "/opt/plug", includeFiles: false });
      return { data: ["/opt/plugins"] };
    });
    await expect(extensionsStore.getState().completePaths("/opt/plug", false)).resolves.toEqual(["/opt/plugins"]);
  });

  test("an empty prefix goes over the wire as-is, for the hub to resolve", async () => {
    const fake = connectFakeClient();
    fake.on("serf/paths/complete", (params) => {
      expect(params).toEqual({ prefix: "", includeFiles: false });
      return { data: ["/home/jesse/src"] };
    });
    await expect(extensionsStore.getState().completePaths("", false)).resolves.toEqual(["/home/jesse/src"]);
  });

  test("forwards includeFiles so file-kind fields get files as well as directories", async () => {
    const fake = connectFakeClient();
    fake.on("serf/paths/complete", (params) => {
      expect(params).toEqual({ prefix: "/etc/", includeFiles: true });
      return { data: ["/etc/ssl/", "/etc/hosts"] };
    });
    await expect(extensionsStore.getState().completePaths("/etc/", true)).resolves.toEqual(["/etc/ssl/", "/etc/hosts"]);
  });

  test("a null data payload resolves to an empty list", async () => {
    const fake = connectFakeClient();
    // A Go handler returning a nil slice sends `null` here, which the generated
    // type declares cannot happen; every PathField would then crash its whole
    // form on the first .length. Coalesced at the seam so no caller has to.
    // The cast is the point: the generated type forbids this payload, which is
    // exactly why TypeScript could never catch the real crash.
    fake.on("serf/paths/complete", () => ({ data: null }) as unknown as { data: string[] });
    await expect(extensionsStore.getState().completePaths("/etc/", true)).resolves.toEqual([]);
  });
});

describe("notification-triggered refetch", () => {
  beforeEach(() => {
    vi.useFakeTimers();
  });

  afterEach(() => {
    vi.useRealTimers();
  });

  test("serf/marketplace/updated schedules a debounced fetchMarketplaces, 250ms", async () => {
    const fake = connectFakeClient();
    fake.on("serf/marketplace/list", () => ({ marketplaces: [MARKETPLACE_A] }));
    await extensionsStore.getState().fetchMarketplaces(); // initial load; also wires notification handling
    fake.on("serf/marketplace/list", () => ({ marketplaces: [MARKETPLACE_A, MARKETPLACE_B] }));

    fake.emitNotification({ method: "serf/marketplace/updated", params: {} });
    await vi.advanceTimersByTimeAsync(249);
    expect(extensionsStore.getState().marketplaces).toEqual([MARKETPLACE_A]);
    await vi.advanceTimersByTimeAsync(1);
    expect(extensionsStore.getState().marketplaces).toEqual([MARKETPLACE_A, MARKETPLACE_B]);
  });

  test("serf/plugin/updated schedules a debounced fetchPlugins, 250ms", async () => {
    const fake = connectFakeClient();
    fake.on("serf/plugin/list", () => ({ plugins: [] }));
    await extensionsStore.getState().fetchPlugins();
    fake.on("serf/plugin/list", () => ({ plugins: [PLUGIN_A] }));

    fake.emitNotification({ method: "serf/plugin/updated", params: {} });
    await vi.advanceTimersByTimeAsync(249);
    expect(extensionsStore.getState().plugins).toEqual([]);
    await vi.advanceTimersByTimeAsync(1);
    expect(extensionsStore.getState().plugins).toEqual([PLUGIN_A]);
  });

  test("serf/launch/updated schedules a debounced fetchLaunchLayer, 250ms", async () => {
    const fake = connectFakeClient();
    fake.on("serf/launch/getLayer", () => ({ pluginDirs: [] }));
    await extensionsStore.getState().fetchLaunchLayer();
    fake.on("serf/launch/getLayer", () => ({ pluginDirs: ["/opt/plugins"] }));

    fake.emitNotification({ method: "serf/launch/updated", params: { cwd: "/tmp/project", layer: "project" } });
    await vi.advanceTimersByTimeAsync(249);
    expect(extensionsStore.getState().launchLayer).toEqual({ pluginDirs: [] });
    await vi.advanceTimersByTimeAsync(1);
    expect(extensionsStore.getState().launchLayer).toEqual({ pluginDirs: ["/opt/plugins"] });
  });

  test("wiring attaches as soon as a client connects, with no prior fetch call required", async () => {
    const fake = connectFakeClient();
    fake.on("serf/marketplace/list", () => ({ marketplaces: [MARKETPLACE_A] }));
    fake.emitNotification({ method: "serf/marketplace/updated", params: {} });
    await vi.advanceTimersByTimeAsync(250);
    expect(extensionsStore.getState().marketplaces).toEqual([MARKETPLACE_A]);
  });

  test("an irrelevant notification triggers no refetch on any of the three channels", async () => {
    const fake = connectFakeClient();
    fake.on("serf/marketplace/list", () => ({ marketplaces: [] }));
    fake.on("serf/plugin/list", () => ({ plugins: [] }));
    fake.on("serf/launch/getLayer", () => ({}));
    await Promise.all([
      extensionsStore.getState().fetchMarketplaces(),
      extensionsStore.getState().fetchPlugins(),
      extensionsStore.getState().fetchLaunchLayer(),
    ]);
    const marketplaceSpy = vi.fn(() => ({ marketplaces: [MARKETPLACE_A] }));
    const pluginSpy = vi.fn(() => ({ plugins: [PLUGIN_A] }));
    const launchSpy = vi.fn(() => ({ pluginDirs: ["/opt/plugins"] }));
    fake.on("serf/marketplace/list", marketplaceSpy);
    fake.on("serf/plugin/list", pluginSpy);
    fake.on("serf/launch/getLayer", launchSpy);

    fake.emitNotification(threadStartedNotification());
    await vi.advanceTimersByTimeAsync(1000);
    expect(marketplaceSpy).not.toHaveBeenCalled();
    expect(pluginSpy).not.toHaveBeenCalled();
    expect(launchSpy).not.toHaveBeenCalled();
  });

  test("each channel debounces independently - a burst of the same notification coalesces into one refetch", async () => {
    const fake = connectFakeClient();
    fake.on("serf/marketplace/list", () => ({ marketplaces: [] }));
    await extensionsStore.getState().fetchMarketplaces();
    const marketplaceSpy = vi.fn(() => ({ marketplaces: [MARKETPLACE_A] }));
    fake.on("serf/marketplace/list", marketplaceSpy);

    fake.emitNotification({ method: "serf/marketplace/updated", params: {} });
    await vi.advanceTimersByTimeAsync(100);
    fake.emitNotification({ method: "serf/marketplace/updated", params: {} });
    await vi.advanceTimersByTimeAsync(100); // 200ms elapsed total, but the second notification reset the window
    expect(marketplaceSpy).not.toHaveBeenCalled();
    await vi.advanceTimersByTimeAsync(150); // 250ms since the last notification
    expect(marketplaceSpy).toHaveBeenCalledTimes(1);
  });
});
