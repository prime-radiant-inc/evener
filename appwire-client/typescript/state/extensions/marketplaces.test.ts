import { afterEach, beforeEach, describe, expect, test, vi } from "vitest";
import { ErrorMarketplaceRemoveApplied, WireError } from "../../errors";
import { deferRequest, FakeClient, failing, gateSettlements } from "../../testing/fakeClient";
import type { MarketplaceCatalogPlugin, MarketplaceEntry } from "../../types.gen";
import {
  createMarketplacesStore,
  MARKETPLACE_REFETCH_DEBOUNCE_MS,
  type MarketplacesStore,
  marketplaceRemovalOutcome,
} from "./marketplaces";

const ACME: MarketplaceEntry = { name: "acme", source: { kind: "github", repo: "acme/plugins" }, lastUpdated: 1 };
const LOCAL: MarketplaceEntry = { name: "local", source: { kind: "directory", path: "/opt/plugins" }, lastUpdated: 2 };

const LIST = "evener/marketplace/list";
const BROWSE = "evener/marketplace/browse";

type BrowseResult = { name: string; description?: string; plugins: MarketplaceCatalogPlugin[] };

function storeWithFake() {
  const fake = new FakeClient("ready");
  return { fake, store: createMarketplacesStore(fake) };
}

function cloneLitterError(marketplaces: unknown, extra: Record<string, unknown> = {}): WireError {
  return new WireError("clone could not be removed", -32603, {
    evenerErrorInfo: "marketplaceUnregisteredCloneRemains",
    applied: { marketplaces },
    ...extra,
  });
}

function removeAppliedUnavailableError(): WireError {
  return new WireError("marketplace removed, but the updated list was unavailable", -32603, {
    evenerErrorInfo: ErrorMarketplaceRemoveApplied,
    appliedUnavailable: true,
  });
}

describe("marketplaceRemovalOutcome", () => {
  test("classifies an applied removal with an unavailable list separately from clone litter", () => {
    expect(marketplaceRemovalOutcome(removeAppliedUnavailableError())).toEqual({ kind: "removed" });
  });

  test("treats a bare remove-applied marker as removed even without data", () => {
    expect(
      marketplaceRemovalOutcome(
        new WireError("marketplace removed", -32603, { evenerErrorInfo: ErrorMarketplaceRemoveApplied }),
      ),
    ).toEqual({ kind: "removed" });
  });

  test("returns the authoritative applied list for clone cleanup failure", () => {
    expect(marketplaceRemovalOutcome(cloneLitterError([LOCAL]))).toEqual({
      kind: "applied",
      marketplaces: [LOCAL],
    });
  });

  test("returns unavailable when the applied list could not be read", () => {
    expect(marketplaceRemovalOutcome(cloneLitterError(null, { appliedUnavailable: true }))).toEqual({
      kind: "unavailable",
    });
  });

  test("keeps malformed marked outcomes applied-but-unconfirmed", () => {
    expect(marketplaceRemovalOutcome(cloneLitterError(null))).toEqual({ kind: "unavailable" });
    expect(marketplaceRemovalOutcome(cloneLitterError({ name: "not-a-list" }))).toEqual({ kind: "unavailable" });
    expect(
      marketplaceRemovalOutcome(
        new WireError("clone could not be removed", -32603, { evenerErrorInfo: "marketplaceUnregisteredCloneRemains" }),
      ),
    ).toEqual({ kind: "unavailable" });
  });

  test("keeps a malformed applied member applied-but-unconfirmed, never miscast", () => {
    // Each member must match appwire.MarketplaceEntry's wire shape
    // (types.gen.ts) before its list can be published as authoritative: a
    // malformed member is the malformed-applied-data case the doc comment on
    // MarketplaceRemovalOutcome names, not a row to cast blindly.
    for (const row of [
      { name: 42, source: { kind: "github" }, lastUpdated: 1 }, // non-string name
      { name: "acme", source: { kind: "github" } }, // missing lastUpdated
      { name: "acme", lastUpdated: 1 }, // missing source
      { name: "acme", source: "github", lastUpdated: 1 }, // source not an object
      { name: "acme", source: null, lastUpdated: 1 },
      { name: "acme", source: { repo: "acme/plugins" }, lastUpdated: 1 }, // source missing kind
      { name: "acme", source: { kind: 7 }, lastUpdated: 1 }, // non-string kind
      { name: "acme", source: { kind: "github" }, lastUpdated: "recent" }, // non-number lastUpdated
      { name: "acme", source: { kind: "github", repo: 42 }, lastUpdated: 1 }, // non-string optional
      { name: "acme", source: { kind: "github" }, installLocation: 7, lastUpdated: 1 },
    ]) {
      expect(marketplaceRemovalOutcome(cloneLitterError([row]))).toEqual({ kind: "unavailable" });
    }
    expect(marketplaceRemovalOutcome(cloneLitterError(["acme"]))).toEqual({ kind: "unavailable" });
    // One malformed member rejects the whole list, not only its own slot.
    expect(marketplaceRemovalOutcome(cloneLitterError([LOCAL, { name: "acme", lastUpdated: 1 }]))).toEqual({
      kind: "unavailable",
    });
  });

  test("still accepts a minimal well-formed member and an empty applied list", () => {
    // Every source field but kind is optional on the wire (appwire/types.go
    // omitempty) and an empty list is a legitimate empty registry, so member
    // validation must not reject payloads the hub really sends.
    const minimal = { name: "acme", source: { kind: "url" }, lastUpdated: 0 };
    expect(marketplaceRemovalOutcome(cloneLitterError([minimal]))).toEqual({
      kind: "applied",
      marketplaces: [minimal],
    });
    expect(marketplaceRemovalOutcome(cloneLitterError([]))).toEqual({ kind: "applied", marketplaces: [] });
  });

  test("keeps truly ordinary failures retryable", () => {
    expect(marketplaceRemovalOutcome(new Error("remove failed"))).toBeUndefined();
  });
});

describe("removeMarketplace applied-but-unconfirmed", () => {
  test("preserves the last snapshot and rejects with the classifiable error", async () => {
    const { fake, store } = storeWithFake();
    fake.on(LIST, () => ({ marketplaces: [ACME, LOCAL] }));
    await store.getState().fetchMarketplaces();
    await store.getState().browseMarketplace("acme");
    const error = removeAppliedUnavailableError();
    fake.on("evener/marketplace/remove", () => {
      throw error;
    });

    const removal = store.getState().removeMarketplace("acme");
    await expect(removal).rejects.toBe(error);
    expect(store.getState().marketplaces).toEqual([ACME, LOCAL]);
    expect(store.getState().browseCatalogs.has("acme")).toBe(true);
    expect(fake.calls.filter((call) => call.method === "evener/marketplace/remove")).toHaveLength(1);
  });
});

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

describe("removeMarketplace clone litter", () => {
  // The hub's marketplaceUnregisteredCloneRemains rejection carries the
  // updated list in data.applied: the unregister already landed, and only the
  // clone's own removal on disk failed. The wire shape emits marketplaces as
  // an array on a successful follow-up read, never as an omitted field.
  test("reconciles the list and browse cache from data.applied, then still rejects", async () => {
    const { fake, store } = storeWithFake();
    fake.on(LIST, () => ({ marketplaces: [ACME, LOCAL] }));
    await store.getState().fetchMarketplaces();
    await store.getState().browseMarketplace("acme");
    expect(store.getState().browseCatalogs.has("acme")).toBe(true);

    fake.on("evener/marketplace/remove", () => {
      throw new WireError(
        'marketplace "acme": marketplace unregistered, but its clone could not be removed; see the hub\'s log for detail',
        -32603,
        { evenerErrorInfo: "marketplaceUnregisteredCloneRemains", applied: { marketplaces: [LOCAL] } },
      );
    });
    await expect(store.getState().removeMarketplace("acme")).rejects.toThrow(/clone could not be removed/);

    expect(store.getState().marketplaces).toEqual([LOCAL]);
    expect(store.getState().browseCatalogs.has("acme")).toBe(false);
  });

  test("appliedUnavailable leaves the list and browse cache untouched, and still rejects", async () => {
    const { fake, store } = storeWithFake();
    fake.on(LIST, () => ({ marketplaces: [ACME, LOCAL] }));
    await store.getState().fetchMarketplaces();
    await store.getState().browseMarketplace("acme");

    fake.on("evener/marketplace/remove", () => {
      throw new WireError('marketplace "acme": marketplace unregistered, but its clone could not be removed', -32603, {
        evenerErrorInfo: "marketplaceUnregisteredCloneRemains",
        applied: { marketplaces: null },
        appliedUnavailable: true,
      });
    });
    await expect(store.getState().removeMarketplace("acme")).rejects.toThrow(/clone could not be removed/);

    // Nothing to reconcile from: the hub's own follow-up read failed too, so
    // this must not read an absent/null list as "every marketplace gone".
    expect(store.getState().marketplaces).toEqual([ACME, LOCAL]);
    expect(store.getState().browseCatalogs.has("acme")).toBe(true);
  });

  test("an ordinary remove failure (no evenerErrorInfo) leaves the list alone", async () => {
    const { fake, store } = storeWithFake();
    fake.on(LIST, () => ({ marketplaces: [ACME] }));
    await store.getState().fetchMarketplaces();
    fake.on("evener/marketplace/remove", failing("mutation failed"));
    await expect(store.getState().removeMarketplace("acme")).rejects.toThrow("mutation failed");
    expect(store.getState().marketplaces).toEqual([ACME]);
  });

  test("settles list loading and error with an accepted applied list", async () => {
    const { fake, store } = storeWithFake();
    fake.on(LIST, () => ({ marketplaces: [ACME, LOCAL] }));
    await store.getState().fetchMarketplaces();
    await store.getState().browseMarketplace("acme");
    store.setState({ marketplacesLoading: true, marketplacesError: "stale" });

    fake.on("evener/marketplace/remove", () => {
      throw cloneLitterError([LOCAL]);
    });
    await expect(store.getState().removeMarketplace("acme")).rejects.toBeInstanceOf(WireError);

    expect(store.getState()).toMatchObject({
      marketplaces: [LOCAL],
      marketplacesLoading: false,
      marketplacesError: null,
    });
    expect(store.getState().browseCatalogs.has("acme")).toBe(false);
  });

  test.each([
    ["null list", cloneLitterError(null)],
    ["object list", cloneLitterError({ name: "not-a-list" })],
    ["unavailable list", cloneLitterError(null, { appliedUnavailable: true })],
    ["row missing a field", cloneLitterError([{ name: "local" }])],
    [
      "row with an incorrectly-typed field",
      cloneLitterError([{ name: 42, source: { kind: "github" }, lastUpdated: 1 }]),
    ],
    ["non-object row", cloneLitterError(["local"])],
  ])("does not publish a %s payload", async (_label, error) => {
    const { fake, store } = storeWithFake();
    fake.on(LIST, () => ({ marketplaces: [ACME, LOCAL] }));
    await store.getState().fetchMarketplaces();
    await store.getState().browseMarketplace("acme");
    store.setState({ marketplacesLoading: true, marketplacesError: "stale" });
    fake.on("evener/marketplace/remove", () => {
      throw error;
    });

    await expect(store.getState().removeMarketplace("acme")).rejects.toBe(error);
    expect(store.getState()).toMatchObject({
      marketplaces: [ACME, LOCAL],
      marketplacesLoading: true,
      marketplacesError: "stale",
    });
    expect(store.getState().browseCatalogs.has("acme")).toBe(true);
  });
});

describe("a write that outruns a read", () => {
  test("the mutation's list clears the loading flag the outrun read raised", async () => {
    const { fake, store } = storeWithFake();
    const releaseRead = deferRequest<{ marketplaces: MarketplaceEntry[] }>(fake, LIST);
    const reading = store.getState().fetchMarketplaces();
    await Promise.resolve();
    expect(store.getState().marketplacesLoading).toBe(true);

    fake.on("evener/marketplace/remove", () => ({ marketplaces: [LOCAL] }));
    await store.getState().removeMarketplace("acme");

    releaseRead({ marketplaces: [ACME] });
    await reading;
    expect(store.getState()).toMatchObject({
      marketplaces: [LOCAL],
      marketplacesLoading: false,
      marketplacesError: null,
    });
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

describe("reset fences a mutation's side effects", () => {
  // reset() is "forget everything this store read": a reply that started
  // before it writes no list, and the catalogs it names must not be retired
  // either. Retiring is monotonic WITHIN a generation - which is why an
  // outrun mutation still retires its own names - but a reset ends the
  // generation, and a browse started after it is about the state reset left
  // behind, not the one the reply belongs to.
  test("a mutation reply that lands after reset() retires nothing", async () => {
    const { fake, store } = storeWithFake();
    fake.on(BROWSE, () => ({ name: "acme", plugins: [{ name: "linter" }] }));
    const release = deferRequest<{ marketplaces: MarketplaceEntry[] }>(fake, "evener/marketplace/remove");
    const removing = store.getState().removeMarketplace("acme");
    await Promise.resolve();

    store.reset();
    await store.getState().browseMarketplace("acme");
    expect(store.getState().browseCatalogs.get("acme")).toMatchObject({ status: "loaded" });

    release({ marketplaces: [] });
    await removing;
    expect(store.getState().browseCatalogs.get("acme")).toMatchObject({ status: "loaded" });
  });

  test("a clone-litter failure after reset() publishes nothing", async () => {
    const { fake, store } = storeWithFake();
    fake.on(BROWSE, () => ({ name: "acme", plugins: [{ name: "linter" }] }));
    await store.getState().browseMarketplace("acme");
    const settlements = gateSettlements(fake, "evener/marketplace/remove");
    const removing = store.getState().removeMarketplace("acme");
    await Promise.resolve();

    store.reset();
    settlements[0]?.reject(cloneLitterError([]));
    await expect(removing).rejects.toBeInstanceOf(WireError);

    expect(store.getState()).toMatchObject({ marketplaces: null, marketplacesLoading: false, marketplacesError: null });
    expect(store.getState().browseCatalogs.size).toBe(0);
  });
});

describe("reconnect", () => {
  // A store belongs to one hub - the web builds one for the app's single
  // connection, native one per client under a screen keyed by hub - so a
  // cached catalog is never another hub's. What it can be is a catalog
  // browsed before the connection went away, whose evener/marketplace/updated
  // never arrived because this client was not there to receive it. A ready
  // connection therefore retires the catalogs exactly as that notification
  // does, so the next browse asks the hub again instead of showing what the
  // catalog held before.
  test("a catalog browsed before a disconnect is read again, not shown again", async () => {
    const { fake, store } = storeWithFake();
    store.connectionChanged(fake, "ready");
    fake.on(LIST, () => ({ marketplaces: [ACME] }));
    fake.on(BROWSE, () => ({ name: "acme", plugins: [{ name: "linter" }] }));
    await store.getState().fetchMarketplaces();
    await store.getState().browseMarketplace("acme");
    expect(store.getState().browseCatalogs.get("acme")).toMatchObject({ status: "loaded" });

    store.connectionChanged(fake, "reconnecting");
    store.connectionChanged(fake, "ready");
    expect(store.getState().browseCatalogs.size).toBe(0);

    fake.on(BROWSE, () => ({ name: "acme", plugins: [{ name: "formatter" }] }));
    await store.getState().browseMarketplace("acme");
    expect(store.getState().browseCatalogs.get("acme")).toEqual({
      status: "loaded",
      plugins: [{ name: "formatter" }],
    });
  });

  // A replaced client fences the browse the same way it fences the list - see
  // onFence - but the catalog entry a fenced browse wrote is "loading", not a
  // list field, and nothing else was going to lower it. Left behind, it makes
  // browseMarketplace's own settled-entry check ("Loaded, errored, or already
  // in flight") true forever: the entry reads as in flight, browses.inFlight()
  // finds no registration for it (retireInFlight() forgot it too), and the
  // call resolves having sent nothing.
  test("a browse still in flight when the client is replaced does not stick on loading", async () => {
    const { fake, store } = storeWithFake();
    store.connectionChanged(fake, "ready");
    const release = deferRequest<BrowseResult>(fake, BROWSE);
    const browsing = store.getState().browseMarketplace("acme");
    await Promise.resolve();
    expect(store.getState().browseCatalogs.get("acme")).toEqual({ status: "loading" });

    // "connecting", not "ready": isolates onFence's own cleanup from the
    // reconnect-while-away path (onNotified), which retires every catalog
    // regardless of status and would otherwise mask this finding.
    store.connectionChanged(new FakeClient("connecting"), "connecting");
    expect(store.getState().browseCatalogs.has("acme")).toBe(false);

    release({ name: "acme", plugins: [{ name: "stale" }] });
    await browsing;
    expect(store.getState().browseCatalogs.has("acme")).toBe(false);

    fake.on(BROWSE, () => ({ name: "acme", plugins: [{ name: "fresh" }] }));
    await store.getState().browseMarketplace("acme");
    expect(fake.calls.filter((c) => c.method === BROWSE)).toHaveLength(2);
    expect(store.getState().browseCatalogs.get("acme")).toEqual({ status: "loaded", plugins: [{ name: "fresh" }] });
  });
});

describe("list ordering", () => {
  test("an accepted newer list retires a catalog omitted by an older applied failure", async () => {
    const { fake, store } = storeWithFake();
    fake.on(LIST, () => ({ marketplaces: [ACME, LOCAL] }));
    await store.getState().fetchMarketplaces();
    await store.getState().browseMarketplace("acme");
    const settlements = gateSettlements(fake, "evener/marketplace/remove");
    const older = store.getState().removeMarketplace("acme");
    const newer = store.getState().removeMarketplace("local");
    await Promise.resolve();

    settlements[0]?.reject(cloneLitterError([LOCAL]));
    await expect(older).rejects.toBeInstanceOf(WireError);
    settlements[1]?.resolve({ marketplaces: [] });
    await newer;

    expect(store.getState().marketplaces).toEqual([]);
    expect(store.getState().browseCatalogs.has("acme")).toBe(false);
  });

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
    // The outrun fetch writes none of its three fields, the flag it raised
    // included; the mutation that outran it answers all three.
    expect(store.getState().marketplacesLoading).toBe(false);
  });

  test("a newer successful write wins over an older applied clone-litter failure", async () => {
    const { fake, store } = storeWithFake();
    fake.on(LIST, () => ({ marketplaces: [ACME, LOCAL] }));
    await store.getState().fetchMarketplaces();
    await store.getState().browseMarketplace("acme");
    const settlements = gateSettlements(fake, "evener/marketplace/remove");
    const older = store.getState().removeMarketplace("acme");
    const newer = store.getState().removeMarketplace("local");
    await Promise.resolve();

    settlements[1]?.resolve({ marketplaces: [ACME] });
    await newer;
    settlements[0]?.reject(cloneLitterError([LOCAL]));
    await expect(older).rejects.toBeInstanceOf(WireError);

    expect(store.getState().marketplaces).toEqual([ACME]);
    expect(store.getState().browseCatalogs.has("acme")).toBe(true);
  });

  test("a newer failed write releases an older applied clone-litter outcome", async () => {
    const { fake, store } = storeWithFake();
    fake.on(LIST, () => ({ marketplaces: [ACME, LOCAL] }));
    await store.getState().fetchMarketplaces();
    await store.getState().browseMarketplace("acme");
    store.setState({ marketplacesLoading: true, marketplacesError: "stale" });
    const settlements = gateSettlements(fake, "evener/marketplace/remove");
    const older = store.getState().removeMarketplace("acme");
    const newer = store.getState().removeMarketplace("local");
    await Promise.resolve();

    settlements[0]?.reject(cloneLitterError([LOCAL]));
    await expect(older).rejects.toBeInstanceOf(WireError);
    expect(store.getState()).toMatchObject({
      marketplaces: [ACME, LOCAL],
      marketplacesLoading: true,
      marketplacesError: "stale",
    });
    expect(store.getState().browseCatalogs.has("acme")).toBe(true);

    const newerFailure = new Error("newer failed");
    settlements[1]?.reject(newerFailure);
    await expect(newer).rejects.toBe(newerFailure);
    expect(store.getState()).toMatchObject({
      marketplaces: [LOCAL],
      marketplacesLoading: false,
      marketplacesError: null,
    });
    expect(store.getState().browseCatalogs.has("acme")).toBe(false);
  });
});

describe("marketplace publication version", () => {
  test("advances once for each accepted list writer", async () => {
    const { fake, store } = storeWithFake();
    expect(store.getState().marketplacesPublicationVersion).toBe(0);

    fake.on(LIST, () => ({ marketplaces: [ACME] }));
    await store.getState().fetchMarketplaces();
    expect(store.getState().marketplacesPublicationVersion).toBe(1);

    fake.on("evener/marketplace/add", () => ({ marketplaces: [ACME, LOCAL] }));
    await store.getState().addMarketplace({ source: LOCAL.source });
    expect(store.getState().marketplacesPublicationVersion).toBe(2);

    fake.on("evener/marketplace/remove", () => {
      throw cloneLitterError([LOCAL]);
    });
    await expect(store.getState().removeMarketplace("acme")).rejects.toBeInstanceOf(WireError);
    expect(store.getState().marketplacesPublicationVersion).toBe(3);
  });

  test("does not advance for failed reads, ordinary failures, or unconfirmed applied data", async () => {
    const { fake, store } = storeWithFake();
    fake.on(LIST, failing("list failed"));
    await store.getState().fetchMarketplaces();
    expect(store.getState().marketplacesPublicationVersion).toBe(0);

    fake.on("evener/marketplace/add", failing("add failed"));
    await expect(store.getState().addMarketplace({ source: LOCAL.source })).rejects.toThrow("add failed");
    expect(store.getState().marketplacesPublicationVersion).toBe(0);

    for (const error of [
      cloneLitterError(null),
      cloneLitterError({ malformed: true }),
      cloneLitterError(null, { appliedUnavailable: true }),
    ]) {
      fake.on("evener/marketplace/remove", () => {
        throw error;
      });
      await expect(store.getState().removeMarketplace("acme")).rejects.toBe(error);
      expect(store.getState().marketplacesPublicationVersion).toBe(0);
    }
  });

  test("a held applied failure advances only when a newer failed write releases it", async () => {
    const { fake, store } = storeWithFake();
    fake.on(LIST, () => ({ marketplaces: [ACME, LOCAL] }));
    await store.getState().fetchMarketplaces();
    const settlements = gateSettlements(fake, "evener/marketplace/remove");
    const older = store.getState().removeMarketplace("acme");
    const newer = store.getState().removeMarketplace("local");
    await Promise.resolve();

    settlements[0]?.reject(cloneLitterError([LOCAL]));
    await expect(older).rejects.toBeInstanceOf(WireError);
    expect(store.getState().marketplacesPublicationVersion).toBe(1);

    settlements[1]?.reject(new Error("newer failed"));
    await expect(newer).rejects.toThrow("newer failed");
    expect(store.getState().marketplacesPublicationVersion).toBe(2);
  });

  test("fences, reset, and dispose preserve the last publication version", async () => {
    const { fake, store } = storeWithFake();
    fake.on(LIST, () => ({ marketplaces: [ACME] }));
    await store.getState().fetchMarketplaces();
    expect(store.getState().marketplacesPublicationVersion).toBe(1);

    const releaseFenced = deferRequest<{ marketplaces: MarketplaceEntry[] }>(fake, LIST);
    const fenced = store.getState().fetchMarketplaces();
    await Promise.resolve();
    store.connectionChanged(new FakeClient("connecting"), "connecting");
    releaseFenced({ marketplaces: [LOCAL] });
    await fenced;
    expect(store.getState().marketplacesPublicationVersion).toBe(1);

    const resetVersions: number[] = [];
    const unsubscribe = store.subscribe((state) => resetVersions.push(state.marketplacesPublicationVersion));
    store.reset();
    unsubscribe();
    expect(resetVersions.length).toBeGreaterThan(0);
    expect(resetVersions.every((version) => version === 1)).toBe(true);
    expect(store.getState().marketplacesPublicationVersion).toBe(1);
    fake.on(LIST, () => ({ marketplaces: [LOCAL] }));
    await store.getState().fetchMarketplaces();
    expect(store.getState().marketplacesPublicationVersion).toBe(2);

    const releaseDisposed = deferRequest<{ marketplaces: MarketplaceEntry[] }>(fake, LIST);
    const disposed = store.getState().fetchMarketplaces();
    await Promise.resolve();
    store.dispose();
    releaseDisposed({ marketplaces: [ACME] });
    await disposed;
    expect(store.getState().marketplacesPublicationVersion).toBe(2);
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
