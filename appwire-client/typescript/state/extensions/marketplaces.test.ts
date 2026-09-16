import { afterEach, beforeEach, describe, expect, test, vi } from "vitest";
import { deferRequest, FakeClient, failing } from "../../testing/fakeClient";
import type { MarketplaceCatalogPlugin, MarketplaceEntry } from "../../types.gen";
import { createMarketplacesStore, MARKETPLACE_REFETCH_DEBOUNCE_MS, type MarketplacesStore } from "./marketplaces";

const ACME: MarketplaceEntry = { name: "acme", source: { kind: "github", repo: "acme/plugins" }, lastUpdated: 1 };
const LOCAL: MarketplaceEntry = { name: "local", source: { kind: "directory", path: "/opt/plugins" }, lastUpdated: 2 };

const LIST = "evener/marketplace/list";
const BROWSE = "evener/marketplace/browse";

type BrowseResult = { name: string; description?: string; plugins: MarketplaceCatalogPlugin[] };

function storeWithFake() {
  const fake = new FakeClient("ready");
  return { fake, store: createMarketplacesStore(fake) };
}

describe("store shape", () => {
  test("two stores share nothing: lists, catalogs and errors stay with their own instance", async () => {
    const first = storeWithFake();
    first.fake.on(LIST, () => ({ marketplaces: [ACME] }));
    first.fake.on(BROWSE, () => ({ name: "acme", plugins: [{ name: "linter" }] }));
    const second = storeWithFake();
    second.fake.on(LIST, failing("boom"));

    await first.store.getState().fetchMarketplaces();
    await first.store.getState().browseMarketplace("acme");
    await second.store.getState().fetchMarketplaces();

    expect(first.store.getState().marketplaces).toEqual([ACME]);
    expect(first.store.getState().browseCatalogs.get("acme")).toEqual({
      status: "loaded",
      plugins: [{ name: "linter" }],
    });
    expect(first.store.getState().marketplacesError).toBeNull();
    expect(second.store.getState().marketplaces).toBeNull();
    expect(second.store.getState().browseCatalogs.size).toBe(0);
    expect(second.store.getState().marketplacesError).toBe("boom");

    first.store.reset();
    expect(first.store.getState().marketplaces).toBeNull();
    expect(second.store.getState().marketplacesError).toBe("boom");
  });
});

describe("fetches never throw, mutations reject", () => {
  test("a failed list records its error in state and resolves", async () => {
    const { fake, store } = storeWithFake();
    fake.on(LIST, failing("network down"));
    await expect(store.getState().fetchMarketplaces()).resolves.toBeUndefined();
    expect(store.getState()).toMatchObject({
      marketplaces: null,
      marketplacesLoading: false,
      marketplacesError: "network down",
    });
  });

  test("a failed browse is cached as status:error and resolves", async () => {
    const { fake, store } = storeWithFake();
    fake.on(BROWSE, failing("no catalog"));
    await expect(store.getState().browseMarketplace("acme")).resolves.toBeUndefined();
    expect(store.getState().browseCatalogs.get("acme")).toEqual({ status: "error", error: "no catalog" });
  });

  test.each([
    [
      "addMarketplace",
      (s: MarketplacesStore) => s.getState().addMarketplace({ source: { kind: "github", repo: "a/b" } }),
      "evener/marketplace/add",
    ],
    [
      "removeMarketplace",
      (s: MarketplacesStore) => s.getState().removeMarketplace("acme"),
      "evener/marketplace/remove",
    ],
    [
      "refreshMarketplace",
      (s: MarketplacesStore) => s.getState().refreshMarketplace("acme"),
      "evener/marketplace/refresh",
    ],
    [
      "editMarketplace",
      (s: MarketplacesStore) => s.getState().editMarketplace({ name: "acme", newName: "acme2" }),
      "evener/marketplace/edit",
    ],
  ] as const)("%s rejects with the hub's error and leaves the list alone", async (_name, mutate, method) => {
    const { fake, store } = storeWithFake();
    fake.on(LIST, () => ({ marketplaces: [ACME] }));
    await store.getState().fetchMarketplaces();
    fake.on(method, failing("mutation failed"));
    await expect(mutate(store)).rejects.toThrow("mutation failed");
    expect(store.getState().marketplaces).toEqual([ACME]);
    expect(store.getState().marketplacesError).toBeNull();
  });

  test("a mutation's response replaces the list", async () => {
    const { fake, store } = storeWithFake();
    fake.on("evener/marketplace/add", (params) => {
      expect(params).toEqual({ name: "local", source: LOCAL.source });
      return { marketplaces: [ACME, LOCAL] };
    });
    await store.getState().addMarketplace({ name: "local", source: LOCAL.source });
    expect(store.getState().marketplaces).toEqual([ACME, LOCAL]);
  });
});

describe("browse cache", () => {
  test("a second browse of a settled catalog sends nothing", async () => {
    const { fake, store } = storeWithFake();
    fake.on(BROWSE, () => ({ name: "acme", plugins: [] }));
    await store.getState().browseMarketplace("acme");
    await store.getState().browseMarketplace("acme");
    expect(fake.calls.filter((c) => c.method === BROWSE)).toHaveLength(1);
  });

  test("a browse for a catalog already in flight sends nothing and waits for it", async () => {
    const { fake, store } = storeWithFake();
    const release = deferRequest<BrowseResult>(fake, BROWSE);
    const first = store.getState().browseMarketplace("acme");
    await Promise.resolve();
    let secondSettled = false;
    const second = store
      .getState()
      .browseMarketplace("acme")
      .then(() => {
        secondSettled = true;
      });
    await new Promise((resolve) => setTimeout(resolve, 0));
    expect(secondSettled).toBe(false);
    release({ name: "acme", plugins: [{ name: "linter" }] });
    await Promise.all([first, second]);
    expect(secondSettled).toBe(true);
    expect(fake.calls.filter((c) => c.method === BROWSE)).toHaveLength(1);
    expect(store.getState().browseCatalogs.get("acme")).toEqual({ status: "loaded", plugins: [{ name: "linter" }] });
  });

  test("a mutation retires the catalogs it names and fences a browse still on the wire", async () => {
    const { fake, store } = storeWithFake();
    fake.on(BROWSE, () => ({ name: "local", plugins: [{ name: "kept" }] }));
    await store.getState().browseMarketplace("local");
    const release = deferRequest<BrowseResult>(fake, BROWSE);
    const stale = store.getState().browseMarketplace("acme");
    await Promise.resolve();

    fake.on("evener/marketplace/edit", () => ({ marketplaces: [{ ...ACME, name: "acme2" }, LOCAL] }));
    await store.getState().editMarketplace({ name: "acme", newName: "acme2" });
    expect(store.getState().browseCatalogs.has("acme")).toBe(false);
    expect(store.getState().browseCatalogs.get("local")).toEqual({ status: "loaded", plugins: [{ name: "kept" }] });

    release({ name: "acme", plugins: [{ name: "stale" }] });
    await stale;
    expect(store.getState().browseCatalogs.has("acme")).toBe(false);
  });

  test("reloadCatalog drops the cached entry and browses again, whatever the entry's status", async () => {
    const { fake, store } = storeWithFake();
    fake.on(BROWSE, failing("first try"));
    await store.getState().browseMarketplace("acme");
    expect(store.getState().browseCatalogs.get("acme")).toEqual({ status: "error", error: "first try" });

    fake.on(BROWSE, () => ({ name: "acme", plugins: [{ name: "linter" }] }));
    await store.getState().reloadCatalog("acme");
    expect(store.getState().browseCatalogs.get("acme")).toEqual({ status: "loaded", plugins: [{ name: "linter" }] });

    fake.on(BROWSE, () => ({ name: "acme", plugins: [{ name: "linter" }, { name: "formatter" }] }));
    await store.getState().reloadCatalog("acme");
    expect(store.getState().browseCatalogs.get("acme")).toEqual({
      status: "loaded",
      plugins: [{ name: "linter" }, { name: "formatter" }],
    });
    expect(fake.calls.filter((c) => c.method === BROWSE)).toHaveLength(3);
  });
});

describe("list ordering", () => {
  test("a list that resolves after a newer mutation committed does not roll the list back", async () => {
    const { fake, store } = storeWithFake();
    const release = deferRequest<{ marketplaces: MarketplaceEntry[] }>(fake, LIST);
    const fetching = store.getState().fetchMarketplaces();
    await Promise.resolve();
    fake.on("evener/marketplace/remove", () => ({ marketplaces: [] }));
    await store.getState().removeMarketplace("acme");
    expect(store.getState().marketplaces).toEqual([]);

    release({ marketplaces: [ACME] });
    await fetching;
    expect(store.getState().marketplaces).toEqual([]);
    // The outrun fetch's loading flag belongs to it as much as its list does.
    expect(store.getState().marketplacesLoading).toBe(true);
  });
});

describe("notifications", () => {
  beforeEach(() => {
    vi.useFakeTimers();
  });
  afterEach(() => {
    vi.useRealTimers();
  });

  test("start() follows evener/marketplace/updated: every catalog is retired and the list refetched after the debounce", async () => {
    const { fake, store } = storeWithFake();
    fake.on(LIST, () => ({ marketplaces: [ACME] }));
    fake.on(BROWSE, () => ({ name: "acme", plugins: [] }));
    await store.getState().fetchMarketplaces();
    await store.getState().browseMarketplace("acme");
    store.start();
    store.start(); // idempotent: one subscription, one refetch
    fake.on(LIST, () => ({ marketplaces: [ACME, LOCAL] }));

    fake.emitNotification({ method: "evener/marketplace/updated", params: {} });
    expect(store.getState().browseCatalogs.size).toBe(0);
    await vi.advanceTimersByTimeAsync(MARKETPLACE_REFETCH_DEBOUNCE_MS - 1);
    expect(store.getState().marketplaces).toEqual([ACME]);
    fake.emitNotification({ method: "evener/marketplace/updated", params: {} });
    await vi.advanceTimersByTimeAsync(MARKETPLACE_REFETCH_DEBOUNCE_MS - 1);
    expect(store.getState().marketplaces).toEqual([ACME]); // the second notification reset the window
    await vi.advanceTimersByTimeAsync(1);
    expect(store.getState().marketplaces).toEqual([ACME, LOCAL]);
    expect(fake.calls.filter((c) => c.method === LIST)).toHaveLength(2);
  });
});

describe("dispose fences mutations", () => {
  test("a mutation that resolves after dispose() leaves the catalogs it names alone", async () => {
    const { fake, store } = storeWithFake();
    fake.on(BROWSE, () => ({ name: "acme", plugins: [] }));
    await store.getState().browseMarketplace("acme");
    const release = deferRequest<{ marketplaces: MarketplaceEntry[] }>(fake, "evener/marketplace/remove");
    const removing = store.getState().removeMarketplace("acme");
    await Promise.resolve();

    store.dispose();
    release({ marketplaces: [] });
    await removing;

    expect(store.getState().browseCatalogs.has("acme")).toBe(true);
  });
});

describe("reset", () => {
  test("reset() fences the list and the browses still in flight", async () => {
    const { fake, store } = storeWithFake();
    const releaseList = deferRequest<{ marketplaces: MarketplaceEntry[] }>(fake, LIST);
    const fetching = store.getState().fetchMarketplaces();
    await Promise.resolve();
    const releaseBrowse = deferRequest<BrowseResult>(fake, BROWSE);
    const browsing = store.getState().browseMarketplace("acme");
    await Promise.resolve();

    store.reset();
    expect(store.getState()).toMatchObject({ marketplaces: null, marketplacesLoading: false, marketplacesError: null });
    expect(store.getState().browseCatalogs.size).toBe(0);

    releaseList({ marketplaces: [ACME] });
    releaseBrowse({ name: "acme", plugins: [{ name: "stale" }] });
    await Promise.all([fetching, browsing]);
    expect(store.getState().marketplaces).toBeNull();
    expect(store.getState().marketplacesLoading).toBe(false);
    expect(store.getState().browseCatalogs.size).toBe(0);

    // The store keeps working after a reset.
    fake.on(BROWSE, () => ({ name: "acme", plugins: [{ name: "fresh" }] }));
    await store.getState().browseMarketplace("acme");
    expect(store.getState().browseCatalogs.get("acme")).toEqual({ status: "loaded", plugins: [{ name: "fresh" }] });
  });
});
