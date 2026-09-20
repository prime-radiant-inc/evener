import type { MarketplaceCatalogPlugin, MarketplaceEntry, PluginEntry } from "@evener/appwire-client";
import { FakeClient } from "@evener/appwire-client/testing/fakeClient";
import { threadStartedNotification } from "@evener/appwire-client/testing/notifications";
import { afterEach, beforeEach, describe, expect, test, vi } from "vitest";
import { connectionStore } from "./connection";
import { extensionsStore, type MarketplaceCatalogEntry, resetExtensionsStoreForTests } from "./extensions";

function connectFakeClient(): FakeClient {
  const fake = new FakeClient("ready");
  connectionStore.getState().connect(fake);
  return fake;
}

type BrowseResult = { name: string; plugins: MarketplaceCatalogPlugin[] };

// Scripts evener/marketplace/browse to hang, and returns the resolver for
// whichever request is in flight. FakeClient.request() defers the handler call
// by one microtask, so the resolver only exists once that has flushed - which
// every awaited mutation in these tests does before the resolver is called.
function deferBrowse(fake: FakeClient): (result: BrowseResult) => void {
  let resolveRequest!: (result: BrowseResult) => void;
  fake.on(
    "evener/marketplace/browse",
    () =>
      new Promise<BrowseResult>((resolve) => {
        resolveRequest = resolve;
      }),
  );
  return (result) => resolveRequest(result);
}

// Scripts evener/marketplace/browse to hang the way deferBrowse does, but
// keeps one resolver per call, so requests that overlap on the same name can
// be released one at a time.
function gateBrowseCalls(fake: FakeClient): ((result: BrowseResult) => void)[] {
  const releases: ((result: BrowseResult) => void)[] = [];
  fake.on(
    "evener/marketplace/browse",
    () =>
      new Promise<BrowseResult>((resolve) => {
        releases.push(resolve);
      }),
  );
  return releases;
}

// Runs everything the microtask queue has waiting. The retire-window tests
// below assert that a caller has *not* resumed yet, which only says something
// once nothing is left to run: a promise resolved with another promise hands
// its waiters on an unpredictable number of ticks later.
function drainMicrotasks(): Promise<void> {
  return new Promise<void>((resolve) => setTimeout(resolve, 0));
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

describe("resetExtensionsStoreForTests", () => {
  test("preserves the marketplace publication version across the combined-store reset", async () => {
    const fake = connectFakeClient();
    fake.on("evener/marketplace/list", () => ({ marketplaces: [MARKETPLACE_A] }));
    const before = extensionsStore.getState().marketplacesPublicationVersion;

    await extensionsStore.getState().fetchMarketplaces();
    const published = extensionsStore.getState().marketplacesPublicationVersion;
    expect(published).toBe(before + 1);

    resetExtensionsStoreForTests();
    expect(extensionsStore.getState().marketplacesPublicationVersion).toBe(published);
  });

  test("clears marketplaces fields seeded straight into the store, not only ones the core published", () => {
    extensionsStore.setState({
      marketplaces: [MARKETPLACE_A],
      marketplacesError: "stale",
      browseCatalogs: new Map([["acme-plugins", { status: "loaded" as const, plugins: [] }]]),
    });
    resetExtensionsStoreForTests();
    expect(extensionsStore.getState()).toMatchObject({
      marketplaces: null,
      marketplacesLoading: false,
      marketplacesError: null,
    });
    expect(extensionsStore.getState().browseCatalogs.size).toBe(0);
  });
});

describe("fetchMarketplaces", () => {
  test("populates marketplaces from evener/marketplace/list", async () => {
    const fake = connectFakeClient();
    fake.on("evener/marketplace/list", () => ({ marketplaces: [MARKETPLACE_A, MARKETPLACE_B] }));
    await extensionsStore.getState().fetchMarketplaces();
    expect(extensionsStore.getState().marketplaces).toEqual([MARKETPLACE_A, MARKETPLACE_B]);
    expect(extensionsStore.getState().marketplacesLoading).toBe(false);
    expect(extensionsStore.getState().marketplacesError).toBeNull();
  });

  test("marketplacesLoading is true while the request is in flight", async () => {
    const fake = connectFakeClient();
    let resolveRequest!: (v: { marketplaces: MarketplaceEntry[] }) => void;
    fake.on(
      "evener/marketplace/list",
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
    fake.on("evener/marketplace/list", () => {
      throw new Error("boom");
    });
    await expect(extensionsStore.getState().fetchMarketplaces()).resolves.toBeUndefined();
    expect(extensionsStore.getState().marketplaces).toBeNull();
    expect(extensionsStore.getState().marketplacesError).toBe("boom");
  });

  test("a later successful fetch clears a previous error", async () => {
    const fake = connectFakeClient();
    fake.on("evener/marketplace/list", () => {
      throw new Error("boom");
    });
    await extensionsStore.getState().fetchMarketplaces();
    expect(extensionsStore.getState().marketplacesError).toBe("boom");

    fake.on("evener/marketplace/list", () => ({ marketplaces: [MARKETPLACE_A] }));
    await extensionsStore.getState().fetchMarketplaces();
    expect(extensionsStore.getState().marketplacesError).toBeNull();
    expect(extensionsStore.getState().marketplaces).toEqual([MARKETPLACE_A]);
  });
});

describe("addMarketplace", () => {
  test("calls evener/marketplace/add with the given params and applies the response's marketplaces directly (no separate list round-trip)", async () => {
    const fake = connectFakeClient();
    fake.on("evener/marketplace/add", (params) => {
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
    fake.on("evener/marketplace/add", () => {
      throw new Error("add failed");
    });
    await expect(
      extensionsStore.getState().addMarketplace({ source: { kind: "url", url: "https://example.com/x.git" } }),
    ).rejects.toThrow("add failed");
  });
});

describe("removeMarketplace", () => {
  test("calls evener/marketplace/remove with the name and applies the response", async () => {
    const fake = connectFacadeForRemove();
    await extensionsStore.getState().removeMarketplace("acme-plugins");
    expect(fake.calls).toContainEqual({ method: "evener/marketplace/remove", params: { name: "acme-plugins" } });
    expect(extensionsStore.getState().marketplaces).toEqual([MARKETPLACE_B]);

    function connectFacadeForRemove() {
      const client = connectFakeClient();
      client.on("evener/marketplace/remove", () => ({ marketplaces: [MARKETPLACE_B] }));
      return client;
    }
  });

  test("invalidates the browse cache entry for the removed marketplace", async () => {
    const fake = connectFakeClient();
    fake.on("evener/marketplace/browse", () => ({ name: "acme-plugins", plugins: [] }));
    await extensionsStore.getState().browseMarketplace("acme-plugins");
    expect(extensionsStore.getState().browseCatalogs.has("acme-plugins")).toBe(true);

    fake.on("evener/marketplace/remove", () => ({ marketplaces: [] }));
    await extensionsStore.getState().removeMarketplace("acme-plugins");
    expect(extensionsStore.getState().browseCatalogs.has("acme-plugins")).toBe(false);
  });

  test("a browse that predates the removal cannot refill the cache it dropped", async () => {
    const fake = connectFakeClient();
    const resolveBrowse = deferBrowse(fake);
    const stale = extensionsStore.getState().browseMarketplace("acme-plugins");

    fake.on("evener/marketplace/remove", () => ({ marketplaces: [] }));
    await extensionsStore.getState().removeMarketplace("acme-plugins");

    resolveBrowse({ name: "acme-plugins", plugins: [{ name: "stale" }] });
    await stale;
    expect(extensionsStore.getState().browseCatalogs.has("acme-plugins")).toBe(false);
  });

  test("a rejection propagates to the caller", async () => {
    const fake = connectFakeClient();
    fake.on("evener/marketplace/remove", () => {
      throw new Error("remove failed");
    });
    await expect(extensionsStore.getState().removeMarketplace("acme-plugins")).rejects.toThrow("remove failed");
  });
});

describe("refreshMarketplace", () => {
  test("calls evener/marketplace/refresh, applies the response, and invalidates the browse cache", async () => {
    const fake = connectFakeClient();
    fake.on("evener/marketplace/browse", () => ({ name: "acme-plugins", plugins: [{ name: "linter" }] }));
    await extensionsStore.getState().browseMarketplace("acme-plugins");
    expect(extensionsStore.getState().browseCatalogs.get("acme-plugins")).toEqual({
      status: "loaded",
      description: undefined,
      plugins: [{ name: "linter" }],
    });

    fake.on("evener/marketplace/refresh", (params) => {
      expect(params).toEqual({ name: "acme-plugins" });
      return { marketplaces: [{ ...MARKETPLACE_A, lastUpdated: 9999 }] };
    });
    await extensionsStore.getState().refreshMarketplace("acme-plugins");
    expect(extensionsStore.getState().marketplaces).toEqual([{ ...MARKETPLACE_A, lastUpdated: 9999 }]);
    expect(extensionsStore.getState().browseCatalogs.has("acme-plugins")).toBe(false);
  });

  test("a browse that predates the refresh cannot refill the cache it dropped", async () => {
    const fake = connectFakeClient();
    const resolveBrowse = deferBrowse(fake);
    const stale = extensionsStore.getState().browseMarketplace("acme-plugins");

    fake.on("evener/marketplace/refresh", () => ({ marketplaces: [MARKETPLACE_A] }));
    await extensionsStore.getState().refreshMarketplace("acme-plugins");

    resolveBrowse({ name: "acme-plugins", plugins: [{ name: "stale" }] });
    await stale;
    expect(extensionsStore.getState().browseCatalogs.has("acme-plugins")).toBe(false);
  });

  test("a rejection propagates to the caller", async () => {
    const fake = connectFakeClient();
    fake.on("evener/marketplace/refresh", () => {
      throw new Error("refresh failed");
    });
    await expect(extensionsStore.getState().refreshMarketplace("acme-plugins")).rejects.toThrow("refresh failed");
  });
});

describe("editMarketplace", () => {
  test("calls evener/marketplace/edit, applies the response, and drops the browse cache for both names", async () => {
    const fake = connectFakeClient();
    fake.on("evener/marketplace/browse", () => ({ name: "acme-plugins", description: "", plugins: [] }));
    await extensionsStore.getState().browseMarketplace("acme-plugins");
    expect(extensionsStore.getState().browseCatalogs.has("acme-plugins")).toBe(true);

    fake.on("evener/marketplace/edit", (params) => {
      expect(params).toEqual({
        name: "acme-plugins",
        newName: "acme2",
        source: { kind: "url", url: "https://x/y.git" },
      });
      return { marketplaces: [{ ...MARKETPLACE_A, name: "acme2", source: { kind: "url", url: "https://x/y.git" } }] };
    });
    await extensionsStore.getState().editMarketplace({
      name: "acme-plugins",
      newName: "acme2",
      source: { kind: "url", url: "https://x/y.git" },
    });
    expect(extensionsStore.getState().marketplaces?.map((m) => m.name)).toEqual(["acme2"]);
    expect(extensionsStore.getState().browseCatalogs.has("acme-plugins")).toBe(false);
    expect(extensionsStore.getState().browseCatalogs.has("acme2")).toBe(false);
  });

  test("a browse that predates the edit cannot refill the cache the edit dropped", async () => {
    const fake = connectFakeClient();
    const resolveBrowse = deferBrowse(fake);
    const stale = extensionsStore.getState().browseMarketplace("acme-plugins");
    expect(extensionsStore.getState().browseCatalogs.get("acme-plugins")).toEqual({ status: "loading" });

    fake.on("evener/marketplace/edit", () => ({ marketplaces: [MARKETPLACE_A] }));
    // A same-name re-source: the name survives, so only the generation tells
    // the in-flight browse its catalog is the pre-edit one.
    await extensionsStore.getState().editMarketplace({
      name: "acme-plugins",
      source: { kind: "github", repo: "acme/plugins-v2" },
    });
    expect(extensionsStore.getState().browseCatalogs.has("acme-plugins")).toBe(false);

    resolveBrowse({ name: "acme-plugins", plugins: [{ name: "stale" }] });
    await stale;
    expect(extensionsStore.getState().browseCatalogs.has("acme-plugins")).toBe(false);

    fake.on("evener/marketplace/browse", () => ({ name: "acme-plugins", plugins: [{ name: "fresh" }] }));
    await extensionsStore.getState().browseMarketplace("acme-plugins");
    expect(extensionsStore.getState().browseCatalogs.get("acme-plugins")).toEqual({
      status: "loaded",
      description: undefined,
      plugins: [{ name: "fresh" }],
    });
  });

  test("a rename fences an in-flight browse under the new name too", async () => {
    const fake = connectFakeClient();
    const resolveBrowse = deferBrowse(fake);
    const stale = extensionsStore.getState().browseMarketplace("acme2");

    fake.on("evener/marketplace/edit", () => ({ marketplaces: [{ ...MARKETPLACE_A, name: "acme2" }] }));
    await extensionsStore.getState().editMarketplace({ name: "acme-plugins", newName: "acme2" });

    resolveBrowse({ name: "acme2", plugins: [{ name: "stale" }] });
    await stale;
    expect(extensionsStore.getState().browseCatalogs.has("acme2")).toBe(false);
  });

  test("a rejection propagates", async () => {
    const fake = connectFakeClient();
    fake.on("evener/marketplace/edit", () => {
      throw new Error("edit failed");
    });
    await expect(extensionsStore.getState().editMarketplace({ name: "acme-plugins", newName: "x" })).rejects.toThrow(
      "edit failed",
    );
  });
});

// Every mutation and the notification refetch replace the whole list from
// their own response, and the hub answers them independently: a slow refresh
// started before a rename can land after it. The later response wins whichever
// order they arrive in.
describe("marketplace mutation ordering", () => {
  test("a refresh that resolves after a newer rename cannot put the old name back", async () => {
    const fake = connectFakeClient();
    let resolveRefresh!: (v: { marketplaces: MarketplaceEntry[] }) => void;
    fake.on(
      "evener/marketplace/refresh",
      () =>
        new Promise((resolve) => {
          resolveRefresh = resolve;
        }),
    );
    const refreshing = extensionsStore.getState().refreshMarketplace("acme-plugins");
    // FakeClient.request() defers the handler call by one microtask, so
    // resolveRefresh is not assigned until that has run.
    await Promise.resolve();

    fake.on("evener/marketplace/edit", () => ({ marketplaces: [{ ...MARKETPLACE_A, name: "acme2" }] }));
    await extensionsStore.getState().editMarketplace({ name: "acme-plugins", newName: "acme2" });
    expect(extensionsStore.getState().marketplaces?.map((m) => m.name)).toEqual(["acme2"]);

    resolveRefresh({ marketplaces: [MARKETPLACE_A] });
    await refreshing;
    expect(extensionsStore.getState().marketplaces?.map((m) => m.name)).toEqual(["acme2"]);
  });

  test("a list response that resolves after a newer mutation cannot roll it back", async () => {
    const fake = connectFakeClient();
    let resolveList!: (v: { marketplaces: MarketplaceEntry[] }) => void;
    fake.on(
      "evener/marketplace/list",
      () =>
        new Promise((resolve) => {
          resolveList = resolve;
        }),
    );
    const fetching = extensionsStore.getState().fetchMarketplaces();
    await Promise.resolve();

    fake.on("evener/marketplace/remove", () => ({ marketplaces: [] }));
    await extensionsStore.getState().removeMarketplace("acme-plugins");
    expect(extensionsStore.getState().marketplaces).toEqual([]);

    resolveList({ marketplaces: [MARKETPLACE_A] });
    await fetching;
    expect(extensionsStore.getState().marketplaces).toEqual([]);
    // The outrun response writes none of its three fields, the loading flag
    // included - and the mutation that outran it answers all three, so the
    // flag this fetch raised on its way out comes down with the mutation's
    // list rather than waiting on the hub's broadcast, which a client that
    // was away never receives.
    expect(extensionsStore.getState().marketplacesLoading).toBe(false);
  });

  test("an outrun response still retires its own browse cache entry", async () => {
    const fake = connectFakeClient();
    fake.on("evener/marketplace/browse", () => ({ name: "local-plugins", plugins: [{ name: "linter" }] }));
    await extensionsStore.getState().browseMarketplace("local-plugins");
    let resolveRefresh!: (v: { marketplaces: MarketplaceEntry[] }) => void;
    fake.on(
      "evener/marketplace/refresh",
      () =>
        new Promise((resolve) => {
          resolveRefresh = resolve;
        }),
    );
    const refreshing = extensionsStore.getState().refreshMarketplace("local-plugins");
    await Promise.resolve();

    fake.on("evener/marketplace/edit", () => ({ marketplaces: [{ ...MARKETPLACE_A, name: "acme2" }] }));
    await extensionsStore.getState().editMarketplace({ name: "acme-plugins", newName: "acme2" });

    resolveRefresh({ marketplaces: [MARKETPLACE_A, MARKETPLACE_B] });
    await refreshing;
    expect(extensionsStore.getState().marketplaces?.map((m) => m.name)).toEqual(["acme2"]);
    expect(extensionsStore.getState().browseCatalogs.has("local-plugins")).toBe(false);
  });

  test("an in-order sequence applies every response", async () => {
    const fake = connectFakeClient();
    fake.on("evener/marketplace/add", () => ({ marketplaces: [MARKETPLACE_A] }));
    await extensionsStore.getState().addMarketplace({ source: { kind: "github", repo: "acme/plugins" } });
    expect(extensionsStore.getState().marketplaces).toEqual([MARKETPLACE_A]);

    fake.on("evener/marketplace/refresh", () => ({ marketplaces: [MARKETPLACE_A, MARKETPLACE_B] }));
    await extensionsStore.getState().refreshMarketplace("acme-plugins");
    expect(extensionsStore.getState().marketplaces).toEqual([MARKETPLACE_A, MARKETPLACE_B]);

    fake.on("evener/marketplace/edit", () => ({ marketplaces: [{ ...MARKETPLACE_A, name: "acme2" }, MARKETPLACE_B] }));
    await extensionsStore.getState().editMarketplace({ name: "acme-plugins", newName: "acme2" });
    expect(extensionsStore.getState().marketplaces?.map((m) => m.name)).toEqual(["acme2", "local-plugins"]);

    fake.on("evener/marketplace/remove", () => ({ marketplaces: [MARKETPLACE_B] }));
    await extensionsStore.getState().removeMarketplace("acme2");
    expect(extensionsStore.getState().marketplaces).toEqual([MARKETPLACE_B]);

    fake.on("evener/marketplace/list", () => ({ marketplaces: [MARKETPLACE_A, MARKETPLACE_B] }));
    await extensionsStore.getState().fetchMarketplaces();
    expect(extensionsStore.getState().marketplaces).toEqual([MARKETPLACE_A, MARKETPLACE_B]);
  });

  // The loading flag and the error describe the same response as the list, so
  // the fence covers all three: a fetch the store has already moved past can
  // no more post its error or clear a load than it can put its list back.
  test("a list failure that lands after a newer mutation cannot post an error over it", async () => {
    const fake = connectFakeClient();
    let rejectList!: (err: Error) => void;
    fake.on(
      "evener/marketplace/list",
      () =>
        new Promise<{ marketplaces: MarketplaceEntry[] }>((_, reject) => {
          rejectList = reject;
        }),
    );
    const fetching = extensionsStore.getState().fetchMarketplaces();
    await Promise.resolve();

    fake.on("evener/marketplace/remove", () => ({ marketplaces: [] }));
    await extensionsStore.getState().removeMarketplace("acme-plugins");

    rejectList(new Error("list failed"));
    await fetching;
    expect(extensionsStore.getState().marketplacesError).toBeNull();
    expect(extensionsStore.getState().marketplaces).toEqual([]);
  });

  test("a list response that lands after a newer fetch failed leaves the newer error in place", async () => {
    const fake = connectFakeClient();
    let resolveList!: (v: { marketplaces: MarketplaceEntry[] }) => void;
    fake.on(
      "evener/marketplace/list",
      () =>
        new Promise((resolve) => {
          resolveList = resolve;
        }),
    );
    const stale = extensionsStore.getState().fetchMarketplaces();
    await Promise.resolve();

    fake.on("evener/marketplace/list", () => {
      throw new Error("list failed");
    });
    await extensionsStore.getState().fetchMarketplaces();
    expect(extensionsStore.getState().marketplacesError).toBe("list failed");

    resolveList({ marketplaces: [MARKETPLACE_A] });
    await stale;
    expect(extensionsStore.getState().marketplacesError).toBe("list failed");
    expect(extensionsStore.getState().marketplaces).toBeNull();
  });

  test("an in-order fetch after a failure clears both the error and the loading flag", async () => {
    const fake = connectFakeClient();
    fake.on("evener/marketplace/list", () => {
      throw new Error("list failed");
    });
    await extensionsStore.getState().fetchMarketplaces();
    expect(extensionsStore.getState().marketplacesError).toBe("list failed");
    expect(extensionsStore.getState().marketplacesLoading).toBe(false);

    fake.on("evener/marketplace/list", () => ({ marketplaces: [MARKETPLACE_A] }));
    await extensionsStore.getState().fetchMarketplaces();
    expect(extensionsStore.getState().marketplaces).toEqual([MARKETPLACE_A]);
    expect(extensionsStore.getState().marketplacesError).toBeNull();
    expect(extensionsStore.getState().marketplacesLoading).toBe(false);
  });
});

describe("browseMarketplace", () => {
  test("fetches and caches a marketplace's catalog as status:loaded", async () => {
    const fake = connectFakeClient();
    fake.on("evener/marketplace/browse", (params) => {
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
    fake.on("evener/marketplace/browse", () => {
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
    fake.on("evener/marketplace/browse", () => ({ name: "acme-plugins", plugins: [] }));
    await extensionsStore.getState().browseMarketplace("acme-plugins");
    await extensionsStore.getState().browseMarketplace("acme-plugins");
    expect(fake.calls.filter((c) => c.method === "evener/marketplace/browse")).toHaveLength(1);
  });

  test("does not re-fetch an already-cached (errored) marketplace - only refreshMarketplace invalidates it", async () => {
    const fake = connectFakeClient();
    fake.on("evener/marketplace/browse", () => {
      throw new Error("boom");
    });
    await extensionsStore.getState().browseMarketplace("acme-plugins");
    await extensionsStore.getState().browseMarketplace("acme-plugins");
    expect(fake.calls.filter((c) => c.method === "evener/marketplace/browse")).toHaveLength(1);
  });

  test("marks the entry status:loading synchronously before the request settles, so a concurrent call is a no-op", async () => {
    const fake = connectFakeClient();
    let resolveRequest!: (v: { name: string; plugins: MarketplaceCatalogPlugin[] }) => void;
    fake.on(
      "evener/marketplace/browse",
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
    expect(fake.calls.filter((c) => c.method === "evener/marketplace/browse")).toHaveLength(1);
  });

  // The synchronous "loading" marker keeps a second call from sending a
  // second request, but a caller that needs the catalog - the browse filter
  // waiting on a marketplace someone else's click started - also needs to
  // know when it lands, so the second call awaits the first's request.
  test("a call for a catalog already in flight resolves with that request, not before it", async () => {
    const fake = connectFakeClient();
    const resolveBrowse = deferBrowse(fake);
    const first = extensionsStore.getState().browseMarketplace("acme-plugins");
    const second = extensionsStore.getState().browseMarketplace("acme-plugins");
    let secondSettled = false;
    void second.then(() => {
      secondSettled = true;
    });
    // FakeClient.request() defers the handler call by one microtask; flush it
    // before using the resolver (see the fetchMarketplaces test above).
    await Promise.resolve();
    expect(secondSettled).toBe(false);
    expect(extensionsStore.getState().browseCatalogs.get("acme-plugins")).toEqual({ status: "loading" });

    resolveBrowse({ name: "acme-plugins", plugins: [{ name: "linter" }] });
    await Promise.all([first, second]);
    expect(secondSettled).toBe(true);
    expect(fake.calls.filter((c) => c.method === "evener/marketplace/browse")).toHaveLength(1);
    expect(extensionsStore.getState().browseCatalogs.get("acme-plugins")).toEqual({
      status: "loaded",
      description: undefined,
      plugins: [{ name: "linter" }],
    });

    // A settled catalog has no request left to wait for: this one resolves on
    // the very next microtask (adopting a leftover promise would cost two
    // more) and still sends nothing.
    let thirdSettled = false;
    void extensionsStore
      .getState()
      .browseMarketplace("acme-plugins")
      .then(() => {
        thirdSettled = true;
      });
    await Promise.resolve();
    expect(thirdSettled).toBe(true);
    expect(fake.calls.filter((c) => c.method === "evener/marketplace/browse")).toHaveLength(1);
  });

  // A retire drops the cache entry without touching the request already on
  // the wire, so a replacement request for the same name registers itself
  // alongside the fenced-out one. When that one lands it must leave the
  // replacement's registration alone: the entry the next caller reads is the
  // replacement's, and it needs the promise that goes with it.
  test("a browse retired mid-flight leaves the replacement request's registration in place", async () => {
    const fake = connectFakeClient();
    const releases = gateBrowseCalls(fake);
    const first = extensionsStore.getState().browseMarketplace("acme-plugins");

    fake.on("evener/marketplace/refresh", () => ({ marketplaces: [MARKETPLACE_A] }));
    await extensionsStore.getState().refreshMarketplace("acme-plugins");
    expect(extensionsStore.getState().browseCatalogs.has("acme-plugins")).toBe(false);

    const second = extensionsStore.getState().browseMarketplace("acme-plugins");
    await drainMicrotasks();
    expect(releases).toHaveLength(2);

    releases[0]!({ name: "acme-plugins", plugins: [{ name: "stale" }] });
    await first;
    expect(extensionsStore.getState().browseCatalogs.get("acme-plugins")).toEqual({ status: "loading" });

    // Only a caller that arrives after the fenced request has landed can see
    // the damage: one that arrives before it finds the replacement's promise
    // still in the map.
    let thirdSettled = false;
    void extensionsStore
      .getState()
      .browseMarketplace("acme-plugins")
      .then(() => {
        thirdSettled = true;
      });
    await drainMicrotasks();
    expect(thirdSettled).toBe(false);

    releases[1]!({ name: "acme-plugins", plugins: [{ name: "fresh" }] });
    await second;
    await drainMicrotasks();
    expect(thirdSettled).toBe(true);
    expect(extensionsStore.getState().browseCatalogs.get("acme-plugins")).toEqual({
      status: "loaded",
      description: undefined,
      plugins: [{ name: "fresh" }],
    });
    expect(fake.calls.filter((c) => c.method === "evener/marketplace/browse")).toHaveLength(2);
  });

  // The same window from the point of view of a caller that adopted the
  // fenced request's promise - the browse filter is the only one. The catalog
  // it is waiting for is the replacement's, so resolving it when the fenced
  // request lands would wake it on a "loading" entry.
  test("a caller waiting on a browse retired mid-flight waits for its replacement", async () => {
    const fake = connectFakeClient();
    const releases = gateBrowseCalls(fake);
    const first = extensionsStore.getState().browseMarketplace("acme-plugins");
    // The entry this caller woke on, so a failure says which one that was.
    let waiterWoke: MarketplaceCatalogEntry | "still waiting" | "no entry" = "still waiting";
    void extensionsStore
      .getState()
      .browseMarketplace("acme-plugins")
      .then(() => {
        waiterWoke = extensionsStore.getState().browseCatalogs.get("acme-plugins") ?? "no entry";
      });

    fake.on("evener/marketplace/refresh", () => ({ marketplaces: [MARKETPLACE_A] }));
    await extensionsStore.getState().refreshMarketplace("acme-plugins");

    const second = extensionsStore.getState().browseMarketplace("acme-plugins");
    await drainMicrotasks();
    expect(releases).toHaveLength(2);
    expect(waiterWoke).toBe("still waiting");

    releases[0]!({ name: "acme-plugins", plugins: [{ name: "stale" }] });
    await first;
    await drainMicrotasks();
    expect(waiterWoke).toBe("still waiting");

    releases[1]!({ name: "acme-plugins", plugins: [{ name: "fresh" }] });
    await second;
    await drainMicrotasks();
    expect(waiterWoke).toEqual({ status: "loaded", description: undefined, plugins: [{ name: "fresh" }] });
  });

  // A real reconnect's first event is a REPLACED client whose own `state` is
  // "connecting" (AppwireClient.connect() enters "connecting" before
  // "ready"), not "ready" itself - so this fences the browse still on the
  // wire without also running the reconnect-while-away refetch that a
  // "ready" transition would, which retires every catalog regardless of
  // status and would otherwise hide this from the package's own store.
  test("a browse still in flight when the connection is replaced does not stick on loading", async () => {
    const fake = connectFakeClient();
    const release = deferBrowse(fake);
    const browsing = extensionsStore.getState().browseMarketplace("acme-plugins");
    await drainMicrotasks();
    expect(extensionsStore.getState().browseCatalogs.get("acme-plugins")).toEqual({ status: "loading" });

    const replacement = new FakeClient("connecting");
    connectionStore.getState().connect(replacement);
    expect(extensionsStore.getState().browseCatalogs.has("acme-plugins")).toBe(false);

    release({ name: "acme-plugins", plugins: [{ name: "stale" }] });
    await browsing;
    expect(extensionsStore.getState().browseCatalogs.has("acme-plugins")).toBe(false);

    replacement.on("evener/marketplace/browse", () => ({ name: "acme-plugins", plugins: [{ name: "fresh" }] }));
    replacement.emitStateChange("ready");
    await extensionsStore.getState().browseMarketplace("acme-plugins");
    expect(extensionsStore.getState().browseCatalogs.get("acme-plugins")).toEqual({
      status: "loaded",
      plugins: [{ name: "fresh" }],
    });
  });
});

describe("fetchPlugins", () => {
  test("populates plugins from evener/plugin/list", async () => {
    const fake = connectFakeClient();
    fake.on("evener/plugin/list", () => ({ plugins: [PLUGIN_A] }));
    await extensionsStore.getState().fetchPlugins();
    expect(extensionsStore.getState().plugins).toEqual([PLUGIN_A]);
    expect(extensionsStore.getState().pluginsLoading).toBe(false);
    expect(extensionsStore.getState().pluginsError).toBeNull();
  });

  test("a rejected request records an error and never throws", async () => {
    const fake = connectFakeClient();
    fake.on("evener/plugin/list", () => {
      throw new Error("boom");
    });
    await expect(extensionsStore.getState().fetchPlugins()).resolves.toBeUndefined();
    expect(extensionsStore.getState().pluginsError).toBe("boom");
  });
});

describe("plugin mutations", () => {
  test("installPlugin calls evener/plugin/install with {plugin,marketplace} and applies the response", async () => {
    const fake = connectFakeClient();
    fake.on("evener/plugin/install", (params) => {
      expect(params).toEqual({ plugin: "linter", marketplace: "acme-plugins" });
      return { plugins: [PLUGIN_A] };
    });
    await extensionsStore.getState().installPlugin("linter", "acme-plugins");
    expect(extensionsStore.getState().plugins).toEqual([PLUGIN_A]);
  });

  test("upgradePlugin calls evener/plugin/upgrade with {plugin,marketplace} and applies the response", async () => {
    const fake = connectFakeClient();
    fake.on("evener/plugin/upgrade", (params) => {
      expect(params).toEqual({ plugin: "linter", marketplace: "acme-plugins" });
      return { plugins: [{ ...PLUGIN_A, version: "1.1.0" }] };
    });
    await extensionsStore.getState().upgradePlugin("linter", "acme-plugins");
    expect(extensionsStore.getState().plugins).toEqual([{ ...PLUGIN_A, version: "1.1.0" }]);
  });

  test("removePlugin calls evener/plugin/remove with {plugin,marketplace} and applies the response", async () => {
    const fake = connectFakeClient();
    fake.on("evener/plugin/remove", (params) => {
      expect(params).toEqual({ plugin: "linter", marketplace: "acme-plugins" });
      return { plugins: [] };
    });
    await extensionsStore.getState().removePlugin("linter", "acme-plugins");
    expect(extensionsStore.getState().plugins).toEqual([]);
  });

  test("enablePlugin calls evener/plugin/enable with {plugin,marketplace} and applies the response", async () => {
    const fake = connectFakeClient();
    fake.on("evener/plugin/enable", (params) => {
      expect(params).toEqual({ plugin: "linter", marketplace: "acme-plugins" });
      return { plugins: [{ ...PLUGIN_A, enabled: true }] };
    });
    await extensionsStore.getState().enablePlugin("linter", "acme-plugins");
    expect(extensionsStore.getState().plugins).toEqual([{ ...PLUGIN_A, enabled: true }]);
  });

  test("disablePlugin calls evener/plugin/disable with {plugin,marketplace} and applies the response", async () => {
    const fake = connectFakeClient();
    fake.on("evener/plugin/disable", (params) => {
      expect(params).toEqual({ plugin: "linter", marketplace: "acme-plugins" });
      return { plugins: [{ ...PLUGIN_A, enabled: false }] };
    });
    await extensionsStore.getState().disablePlugin("linter", "acme-plugins");
    expect(extensionsStore.getState().plugins).toEqual([{ ...PLUGIN_A, enabled: false }]);
  });

  test("setPluginAutoUpgrade calls evener/plugin/setAutoUpgrade with {plugin,marketplace,autoUpgrade} and applies the response", async () => {
    const fake = connectFakeClient();
    fake.on("evener/plugin/setAutoUpgrade", (params) => {
      expect(params).toEqual({ plugin: "linter", marketplace: "acme-plugins", autoUpgrade: true });
      return { plugins: [{ ...PLUGIN_A, autoUpgrade: true }] };
    });
    await extensionsStore.getState().setPluginAutoUpgrade("linter", "acme-plugins", true);
    expect(extensionsStore.getState().plugins).toEqual([{ ...PLUGIN_A, autoUpgrade: true }]);
  });

  test("a rejected plugin mutation propagates to the caller", async () => {
    const fake = connectFakeClient();
    fake.on("evener/plugin/upgrade", () => {
      throw new Error("upgrade failed");
    });
    await expect(extensionsStore.getState().upgradePlugin("linter", "acme-plugins")).rejects.toThrow("upgrade failed");
  });
});

describe("fetchLaunchLayer", () => {
  test("populates launchLayer from evener/launch/getLayer with cwd '/' and layer 'global'", async () => {
    const fake = connectFakeClient();
    fake.on("evener/launch/getLayer", (params) => {
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
    fake.on("evener/launch/getLayer", () => {
      throw new Error("boom");
    });
    await expect(extensionsStore.getState().fetchLaunchLayer()).resolves.toBeUndefined();
    expect(extensionsStore.getState().launchLayerError).toBe("boom");
  });
});

describe("setLaunchLayer", () => {
  test("calls evener/launch/setLayer with cwd '/', layer 'global', and the given config", async () => {
    const fake = connectFakeClient();
    fake.on("evener/launch/setLayer", (params) => {
      expect(params).toEqual({ cwd: "/", layer: "global", config: { pluginDirs: ["/opt/plugins"] } });
      return { effective: {}, layers: { global: { pluginDirs: ["/opt/plugins"] } }, provenance: {} };
    });
    await extensionsStore.getState().setLaunchLayer({ pluginDirs: ["/opt/plugins"] });
  });

  test("stores the config it sent as the new launchLayer on success (not a parsed response field)", async () => {
    const fake = connectFakeClient();
    fake.on("evener/launch/setLayer", () => ({
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
    fake.on("evener/launch/getLayer", () => ({ pluginDirs: ["/existing"] }));
    await extensionsStore.getState().fetchLaunchLayer();

    fake.on("evener/launch/setLayer", () => {
      throw new Error("save failed");
    });
    await expect(extensionsStore.getState().setLaunchLayer({ pluginDirs: ["/existing", "/new"] })).rejects.toThrow(
      "save failed",
    );
    expect(extensionsStore.getState().launchLayer).toEqual({ pluginDirs: ["/existing"] });
  });
});

describe("validatePath", () => {
  test("calls evener/path/validate with the given path and kind and returns the response", async () => {
    const fake = connectFakeClient();
    fake.on("evener/path/validate", (params) => {
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
    fake.on("evener/path/validate", () => ({ path: "/nope", valid: false, error: "path does not exist" }));
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
    fake.on("evener/paths/complete", (params) => {
      expect(params).toEqual({ prefix: "/opt/plug", includeFiles: false });
      return { data: ["/opt/plugins"] };
    });
    await expect(extensionsStore.getState().completePaths("/opt/plug", false)).resolves.toEqual(["/opt/plugins"]);
  });

  test("an empty prefix goes over the wire as-is, for the hub to resolve", async () => {
    const fake = connectFakeClient();
    fake.on("evener/paths/complete", (params) => {
      expect(params).toEqual({ prefix: "", includeFiles: false });
      return { data: ["/home/jesse/src"] };
    });
    await expect(extensionsStore.getState().completePaths("", false)).resolves.toEqual(["/home/jesse/src"]);
  });

  test("forwards includeFiles so file-kind fields get files as well as directories", async () => {
    const fake = connectFakeClient();
    fake.on("evener/paths/complete", (params) => {
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
    fake.on("evener/paths/complete", () => ({ data: null }) as unknown as { data: string[] });
    await expect(extensionsStore.getState().completePaths("/etc/", true)).resolves.toEqual([]);
  });
});

describe("reconnect-triggered refetch", () => {
  // The hub broadcasts evener/marketplace/updated and evener/plugin/updated to
  // every CONNECTED client, so a change another client made while this browser
  // was away arrives nowhere: the notification cannot recover it and the
  // reconnect has to. The sections' own mount effect is a one-shot (it latches
  // `started`), so without this the pane shows the pre-disconnect lists until
  // the user navigates away and back.
  test("a reconnect re-reads the lists something has already read", async () => {
    const fake = connectFakeClient();
    fake.on("evener/marketplace/list", () => ({ marketplaces: [MARKETPLACE_A] }));
    fake.on("evener/plugin/list", () => ({ plugins: [PLUGIN_A] }));
    fake.on("evener/launch/getLayer", () => ({ pluginDirs: ["/opt/plugins"] }));
    await extensionsStore.getState().fetchMarketplaces();
    await extensionsStore.getState().fetchPlugins();
    await extensionsStore.getState().fetchLaunchLayer();

    fake.emitStateChange("reconnecting");
    fake.emitReady();
    await drainMicrotasks();

    expect(fake.calls.filter((c) => c.method === "evener/marketplace/list")).toHaveLength(2);
    expect(fake.calls.filter((c) => c.method === "evener/plugin/list")).toHaveLength(2);
    expect(fake.calls.filter((c) => c.method === "evener/launch/getLayer")).toHaveLength(2);
  });

  // pluginRevision is what the spawn form's HOST-scoped consumers key on:
  // usePluginPreview and useSpawnSlashCatalog ask the selected host directly
  // and never read this store's installed list. A disconnection can hide any
  // number of plugin changes from them, so the revision has to move on the way
  // back regardless of whether anything ever read the controller's own list -
  // the established check belongs to the refetch decision, not to this.
  test("a reconnect moves pluginRevision even though no section read the list", async () => {
    const fake = connectFakeClient();
    fake.on("evener/plugin/list", () => ({ plugins: [PLUGIN_A] }));
    expect(extensionsStore.getState().pluginRevision).toBe(0);

    fake.emitStateChange("reconnecting");
    fake.emitReady();
    await drainMicrotasks();

    expect(extensionsStore.getState().pluginRevision).toBe(1);
    expect(fake.calls.filter((c) => c.method === "evener/plugin/list")).toHaveLength(0);
  });

  // This module is lazily loaded, so it usually initializes AFTER the client is
  // ready: the connection it must learn from is the one that already exists,
  // not the next transition. Without that first pass the cores' first sight of
  // the connection is the reconnect itself, which they then read as a first
  // connection - nothing invalidated, no revision moved, exactly when a
  // disconnection has just hidden changes from them.
  // resetExtensionsStoreForTests puts the singleton back the way a fresh load
  // leaves it, that pass included, which is what lets this be tested at all.
  test("a module that initializes while the client is ready still recovers on the next reconnect", async () => {
    const fake = connectFakeClient();
    resetExtensionsStoreForTests();
    expect(extensionsStore.getState().pluginRevision).toBe(0);

    fake.emitStateChange("reconnecting");
    fake.emitReady();
    await drainMicrotasks();

    expect(extensionsStore.getState().pluginRevision).toBe(1);
  });

  // The cores subscribe through this store's port, not to a client directly,
  // and the port follows connectionStore (onConnectionNotification re-wires on
  // every client change). So a notification from a client that has been
  // replaced must reach nothing: it describes a hub this browser no longer
  // speaks to, and acting on it would retire the new hub's caches or move the
  // revision its consumers key on.
  test("a notification from a replaced client moves nothing", async () => {
    const stale = connectFakeClient();
    stale.on("evener/plugin/list", () => ({ plugins: [PLUGIN_A] }));
    stale.on("evener/marketplace/list", () => ({ marketplaces: [MARKETPLACE_A] }));
    await extensionsStore.getState().fetchPlugins();
    await extensionsStore.getState().fetchMarketplaces();
    await extensionsStore.getState().browseMarketplace("acme-plugins");

    const current = connectFakeClient();
    const revision = extensionsStore.getState().pluginRevision;
    const calls = current.calls.length;

    stale.emitNotification({ method: "evener/plugin/updated", params: {} });
    stale.emitNotification({ method: "evener/marketplace/updated", params: {} });
    await drainMicrotasks();

    expect(extensionsStore.getState().pluginRevision).toBe(revision);
    expect(current.calls).toHaveLength(calls);
  });

  // The sections' loader is a one-shot: it latches `started` when it fires the
  // first fetch (marketplacesPlugins/index.tsx). So a read the client
  // replacement interrupted is never asked for again by the pane, and the
  // store has to carry the intent across the replacement itself - otherwise
  // the settings page keeps a null list with nothing loading.
  test("a read a client replacement interrupted is issued again, to the replacement", async () => {
    const interrupted = connectFakeClient();
    interrupted.on("evener/plugin/list", () => new Promise(() => {}));
    void extensionsStore.getState().fetchPlugins();
    await Promise.resolve();
    expect(extensionsStore.getState().pluginsLoading).toBe(true);

    // Scripted BEFORE it is connected: the recovery read goes out synchronously
    // with the connection it recovers on.
    const replacement = new FakeClient("ready");
    replacement.on("evener/plugin/list", () => ({ plugins: [PLUGIN_A] }));
    connectionStore.getState().connect(replacement);
    await drainMicrotasks();

    expect(replacement.calls.filter((c) => c.method === "evener/plugin/list")).toHaveLength(1);
    expect(extensionsStore.getState().plugins).toEqual([PLUGIN_A]);
    expect(extensionsStore.getState().pluginsLoading).toBe(false);
  });

  // What ConnectionBanner's retry does: connect(fresh) runs BEFORE
  // `await fresh.connect()`, so the store is told about the replacement while
  // it is still idle and only later hears it is ready. The read the
  // replacement interrupted has to survive that gap, because the sections'
  // loader latches `started` and will not ask again.
  test("a replacement named before it is dialled still gets the read it interrupted", async () => {
    const interrupted = connectFakeClient();
    interrupted.on("evener/plugin/list", () => new Promise(() => {}));
    void extensionsStore.getState().fetchPlugins();
    await Promise.resolve();
    expect(extensionsStore.getState().pluginsLoading).toBe(true);

    const fresh = new FakeClient("idle");
    fresh.on("evener/plugin/list", () => ({ plugins: [PLUGIN_A] }));
    connectionStore.getState().connect(fresh);
    expect(fresh.calls).toHaveLength(0);

    fresh.emitReady();
    await drainMicrotasks();

    expect(fresh.calls.filter((c) => c.method === "evener/plugin/list")).toHaveLength(1);
    expect(extensionsStore.getState().plugins).toEqual([PLUGIN_A]);
    expect(extensionsStore.getState().pluginsLoading).toBe(false);
  });

  test("a reconnect reads nothing for a section that was never opened", async () => {
    const fake = connectFakeClient();
    fake.on("evener/marketplace/list", () => ({ marketplaces: [MARKETPLACE_A] }));
    fake.on("evener/plugin/list", () => ({ plugins: [PLUGIN_A] }));
    await extensionsStore.getState().fetchMarketplaces();
    const marketplaceCalls = fake.calls.filter((c) => c.method === "evener/marketplace/list").length;

    fake.emitStateChange("reconnecting");
    fake.emitReady();
    await drainMicrotasks();

    expect(fake.calls.filter((c) => c.method === "evener/marketplace/list")).toHaveLength(marketplaceCalls + 1);
    expect(fake.calls.filter((c) => c.method === "evener/plugin/list")).toHaveLength(0);
    expect(fake.calls.filter((c) => c.method === "evener/launch/getLayer")).toHaveLength(0);
  });

  // A fresh screen that installs a plugin before ever fetching the list: none
  // of pluginsLoading/plugins/pluginsError is set yet, so nothing in the
  // store's own data marks the list as wanted - only the install still on the
  // wire does, at the seam writeRevisioned and readRevisioned share.
  test("a mutation issued before any list fetch still recovers on a replaced connection", async () => {
    const interrupted = connectFakeClient();
    interrupted.on("evener/plugin/install", () => new Promise(() => {}));
    void extensionsStore.getState().installPlugin("linter", "acme-plugins");
    await Promise.resolve();
    expect(extensionsStore.getState().plugins).toBeNull();

    const replacement = new FakeClient("ready");
    replacement.on("evener/plugin/list", () => ({ plugins: [PLUGIN_A] }));
    connectionStore.getState().connect(replacement);
    await drainMicrotasks();

    expect(replacement.calls.filter((c) => c.method === "evener/plugin/list")).toHaveLength(1);
    expect(extensionsStore.getState().plugins).toEqual([PLUGIN_A]);
  });
});

describe("notification-triggered refetch", () => {
  beforeEach(() => {
    vi.useFakeTimers();
  });

  afterEach(() => {
    vi.useRealTimers();
  });

  test("evener/marketplace/updated schedules a debounced fetchMarketplaces, 250ms", async () => {
    const fake = connectFakeClient();
    fake.on("evener/marketplace/list", () => ({ marketplaces: [MARKETPLACE_A] }));
    await extensionsStore.getState().fetchMarketplaces(); // initial load; also wires notification handling
    fake.on("evener/marketplace/list", () => ({ marketplaces: [MARKETPLACE_A, MARKETPLACE_B] }));

    fake.emitNotification({ method: "evener/marketplace/updated", params: {} });
    await vi.advanceTimersByTimeAsync(249);
    expect(extensionsStore.getState().marketplaces).toEqual([MARKETPLACE_A]);
    await vi.advanceTimersByTimeAsync(1);
    expect(extensionsStore.getState().marketplaces).toEqual([MARKETPLACE_A, MARKETPLACE_B]);
  });

  // The notification names nothing, so any marketplace's catalog may have
  // changed - an add, a removal, a refresh, a rename or a re-source from
  // another client all arrive as this one bare method.
  test("a marketplace update retires every catalog and fences in-flight browses", async () => {
    const fake = connectFakeClient();
    fake.on("evener/marketplace/list", () => ({ marketplaces: [MARKETPLACE_A, MARKETPLACE_B] }));
    fake.on("evener/marketplace/browse", () => ({ name: "acme-plugins", plugins: [{ name: "before" }] }));
    await extensionsStore.getState().browseMarketplace("acme-plugins");
    fake.on("evener/marketplace/browse", () => ({ name: "local-plugins", plugins: [{ name: "before" }] }));
    await extensionsStore.getState().browseMarketplace("local-plugins");

    const resolveBrowse = deferBrowse(fake);
    const stale = extensionsStore.getState().browseMarketplace("third-plugins");
    expect(extensionsStore.getState().browseCatalogs.get("third-plugins")).toEqual({ status: "loading" });
    // FakeClient.request() defers the handler call by one microtask; flush it
    // so deferBrowse's resolver exists (see browseMarketplace's own tests).
    await Promise.resolve();

    fake.emitNotification({ method: "evener/marketplace/updated", params: {} });
    expect([...extensionsStore.getState().browseCatalogs.keys()]).toEqual([]);

    resolveBrowse({ name: "third-plugins", plugins: [{ name: "stale" }] });
    await stale;
    expect(extensionsStore.getState().browseCatalogs.has("third-plugins")).toBe(false);
  });

  // The refetch a notification schedules is one more list response, and a
  // mutation this client makes while it is in flight is newer than it.
  test("a refetch that resolves after a newer mutation does not roll the list back", async () => {
    const fake = connectFakeClient();
    fake.on("evener/marketplace/list", () => ({ marketplaces: [MARKETPLACE_A] }));
    await extensionsStore.getState().fetchMarketplaces();

    let resolveList!: (v: { marketplaces: MarketplaceEntry[] }) => void;
    fake.on(
      "evener/marketplace/list",
      () =>
        new Promise((resolve) => {
          resolveList = resolve;
        }),
    );
    fake.emitNotification({ method: "evener/marketplace/updated", params: {} });
    await vi.advanceTimersByTimeAsync(250); // the refetch starts, and hangs

    fake.on("evener/marketplace/remove", () => ({ marketplaces: [] }));
    await extensionsStore.getState().removeMarketplace("acme-plugins");
    expect(extensionsStore.getState().marketplaces).toEqual([]);

    resolveList({ marketplaces: [MARKETPLACE_A] });
    await vi.advanceTimersByTimeAsync(0);
    expect(extensionsStore.getState().marketplaces).toEqual([]);
  });

  test("evener/plugin/updated schedules a debounced fetchPlugins, 250ms", async () => {
    const fake = connectFakeClient();
    fake.on("evener/plugin/list", () => ({ plugins: [] }));
    await extensionsStore.getState().fetchPlugins();
    fake.on("evener/plugin/list", () => ({ plugins: [PLUGIN_A] }));

    fake.emitNotification({ method: "evener/plugin/updated", params: {} });
    await vi.advanceTimersByTimeAsync(249);
    expect(extensionsStore.getState().plugins).toEqual([]);
    await vi.advanceTimersByTimeAsync(1);
    expect(extensionsStore.getState().plugins).toEqual([PLUGIN_A]);
  });

  test("evener/plugin/updated increments pluginRevision synchronously before refetch", async () => {
    const fake = connectFakeClient();
    fake.on("evener/plugin/list", () => ({ plugins: [] }));
    await extensionsStore.getState().fetchPlugins();
    expect(extensionsStore.getState().pluginRevision).toBe(0);

    fake.emitNotification({ method: "evener/plugin/updated", params: {} });
    expect(extensionsStore.getState().pluginRevision).toBe(1);
    await vi.advanceTimersByTimeAsync(250);
    expect(extensionsStore.getState().pluginRevision).toBe(1);
  });

  test("evener/launch/updated schedules a debounced fetchLaunchLayer, 250ms", async () => {
    const fake = connectFakeClient();
    fake.on("evener/launch/getLayer", () => ({ pluginDirs: [] }));
    await extensionsStore.getState().fetchLaunchLayer();
    fake.on("evener/launch/getLayer", () => ({ pluginDirs: ["/opt/plugins"] }));

    fake.emitNotification({ method: "evener/launch/updated", params: { cwd: "/tmp/project", layer: "project" } });
    await vi.advanceTimersByTimeAsync(249);
    expect(extensionsStore.getState().launchLayer).toEqual({ pluginDirs: [] });
    await vi.advanceTimersByTimeAsync(1);
    expect(extensionsStore.getState().launchLayer).toEqual({ pluginDirs: ["/opt/plugins"] });
  });

  // A remote host's own config notifications reach this browser wrapped in
  // evener/host/notification tagged with the host that owns them
  // (cmd/evener-hub/app_host_admin.go's relayHostNotifications). pluginRevision
  // is the revision the spawn form's HOST-scoped plugin consumers key on -
  // usePluginPreview/useSpawnSlashCatalog build their request key from it and
  // then ask the SELECTED host through evener/host/request - so a plugin
  // enabled or disabled on that host must bump it exactly as the controller's
  // own update does. Without this the form keeps rendering the pre-change list,
  // and a plugin reconciled from that stale preview is sent as a thread/start
  // launchOverride the host no longer has.
  test("a wrapped remote plugin update bumps pluginRevision", async () => {
    const fake = connectFakeClient();
    fake.on("evener/plugin/list", () => ({ plugins: [PLUGIN_A] }));
    await extensionsStore.getState().fetchPlugins();
    expect(extensionsStore.getState().pluginRevision).toBe(0);

    fake.emitNotification({
      method: "evener/host/notification",
      params: { host: "buildbox", method: "evener/plugin/updated", params: {} },
    });
    expect(extensionsStore.getState().pluginRevision).toBe(1);

    // The store's own list is the CONTROLLER's (evener/plugin/list over the
    // plain connection), and a remote host's change is not evidence about it:
    // no controller refetch is scheduled.
    const listCalls = fake.calls.filter((call) => call.method === "evener/plugin/list").length;
    await vi.advanceTimersByTimeAsync(250);
    expect(fake.calls.filter((call) => call.method === "evener/plugin/list").length).toBe(listCalls);
    expect(extensionsStore.getState().plugins).toEqual([PLUGIN_A]);
  });

  // The launch layer this store holds is the controller's own
  // (evener/launch/getLayer with GLOBAL_LAYER_PARAMS) and its only readers are
  // the controller-scoped settings sections, so a remote host's launch change
  // must not schedule that refetch - it would replace this hub's layer with
  // this hub's unchanged one.
  test("a wrapped remote launch update never refetches the controller's launch layer", async () => {
    const fake = connectFakeClient();
    fake.on("evener/launch/getLayer", () => ({ pluginDirs: [] }));
    await extensionsStore.getState().fetchLaunchLayer();
    const layerCalls = fake.calls.filter((call) => call.method === "evener/launch/getLayer").length;

    fake.emitNotification({
      method: "evener/host/notification",
      params: { host: "buildbox", method: "evener/launch/updated", params: {} },
    });
    await vi.advanceTimersByTimeAsync(250);
    expect(fake.calls.filter((call) => call.method === "evener/launch/getLayer").length).toBe(layerCalls);
  });

  // The wrapper is not a general-purpose remote-notification tunnel: only the
  // two host-owned methods the spawn form consumes move this store. A wrapped
  // evener/auth/updated is the spawn pane's own concern (it advances that pane's
  // catalog generation) and must not be read as a plugin change here.
  test("a wrapped remote method this store does not own leaves pluginRevision alone", () => {
    const fake = connectFakeClient();
    fake.emitNotification({
      method: "evener/host/notification",
      params: { host: "buildbox", method: "evener/auth/updated", params: {} },
    });
    expect(extensionsStore.getState().pluginRevision).toBe(0);
  });

  // The fan-out subscribes one remote host per goroutine, so a wrapper tagged
  // with the controller itself is not something the hub emits today. It is
  // this hub's own change by definition, and the seam that drops a remote
  // host's wrapper must not drop it with them: it is unwrapped and applied
  // exactly as the plain notification is, revision and refetch both.
  test("a wrapped update tagged with the controller is applied as the controller's own", async () => {
    const fake = connectFakeClient();
    fake.on("evener/plugin/list", () => ({ plugins: [] }));
    await extensionsStore.getState().fetchPlugins();
    fake.on("evener/plugin/list", () => ({ plugins: [PLUGIN_A] }));

    fake.emitNotification({
      method: "evener/host/notification",
      params: { host: "local", method: "evener/plugin/updated", params: {} },
    });
    expect(extensionsStore.getState().pluginRevision).toBe(1);

    await vi.advanceTimersByTimeAsync(250);
    expect(extensionsStore.getState().plugins).toEqual([PLUGIN_A]);
  });

  test("wiring attaches as soon as a client connects, with no prior fetch call required", async () => {
    const fake = connectFakeClient();
    fake.on("evener/marketplace/list", () => ({ marketplaces: [MARKETPLACE_A] }));
    fake.emitNotification({ method: "evener/marketplace/updated", params: {} });
    await vi.advanceTimersByTimeAsync(250);
    expect(extensionsStore.getState().marketplaces).toEqual([MARKETPLACE_A]);
  });

  test("an irrelevant notification triggers no refetch on any of the three channels", async () => {
    const fake = connectFakeClient();
    fake.on("evener/marketplace/list", () => ({ marketplaces: [] }));
    fake.on("evener/plugin/list", () => ({ plugins: [] }));
    fake.on("evener/launch/getLayer", () => ({}));
    await Promise.all([
      extensionsStore.getState().fetchMarketplaces(),
      extensionsStore.getState().fetchPlugins(),
      extensionsStore.getState().fetchLaunchLayer(),
    ]);
    const marketplaceSpy = vi.fn(() => ({ marketplaces: [MARKETPLACE_A] }));
    const pluginSpy = vi.fn(() => ({ plugins: [PLUGIN_A] }));
    const launchSpy = vi.fn(() => ({ pluginDirs: ["/opt/plugins"] }));
    fake.on("evener/marketplace/list", marketplaceSpy);
    fake.on("evener/plugin/list", pluginSpy);
    fake.on("evener/launch/getLayer", launchSpy);

    fake.emitNotification(threadStartedNotification());
    await vi.advanceTimersByTimeAsync(1000);
    expect(marketplaceSpy).not.toHaveBeenCalled();
    expect(pluginSpy).not.toHaveBeenCalled();
    expect(launchSpy).not.toHaveBeenCalled();
  });

  test("each channel debounces independently - a burst of the same notification coalesces into one refetch", async () => {
    const fake = connectFakeClient();
    fake.on("evener/marketplace/list", () => ({ marketplaces: [] }));
    await extensionsStore.getState().fetchMarketplaces();
    const marketplaceSpy = vi.fn(() => ({ marketplaces: [MARKETPLACE_A] }));
    fake.on("evener/marketplace/list", marketplaceSpy);

    fake.emitNotification({ method: "evener/marketplace/updated", params: {} });
    await vi.advanceTimersByTimeAsync(100);
    fake.emitNotification({ method: "evener/marketplace/updated", params: {} });
    await vi.advanceTimersByTimeAsync(100); // 200ms elapsed total, but the second notification reset the window
    expect(marketplaceSpy).not.toHaveBeenCalled();
    await vi.advanceTimersByTimeAsync(150); // 250ms since the last notification
    expect(marketplaceSpy).toHaveBeenCalledTimes(1);
  });
});
