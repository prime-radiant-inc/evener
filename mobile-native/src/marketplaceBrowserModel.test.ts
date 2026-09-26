import { expect, test } from "vitest";
import { ErrorMarketplaceRemoveApplied, WireError } from "@evener/appwire-client";
import { FakeClient } from "@evener/appwire-client/testing/fakeClient";
import { createMarketplacesStore } from "@evener/appwire-client/state/extensions";
import type { MarketplaceEntry } from "@evener/appwire-client";
import type { MarketplaceCatalogEntry } from "@evener/appwire-client/state/extensions";
import {
  addedMarketplaceNames,
  appliedRemovalNotice,
  catalogToBrowse,
  refetchAfterRemoval,
} from "./marketplaceBrowserModel";

const entry = (name: string): MarketplaceEntry => ({
  name,
  source: { kind: "directory", path: `/fixture/${name}` },
  lastUpdated: 1,
});
const loaded: MarketplaceCatalogEntry = { status: "loaded", plugins: [] };
const catalogs = (...names: string[]) =>
  new Map<string, MarketplaceCatalogEntry>(names.map((name) => [name, loaded]));

test("a selected marketplace on the list with no cached catalog is browsed", () => {
  expect(catalogToBrowse("a", [entry("a"), entry("b")], catalogs())).toBe("a");
});

test("no selection, or a cached catalog, browses nothing", () => {
  expect(catalogToBrowse(null, [entry("a")], catalogs())).toBeNull();
  expect(catalogToBrowse("a", [entry("a")], catalogs("a"))).toBeNull();
});

test("a selection the list no longer carries is not browsed: the selection is about to clear", () => {
  expect(catalogToBrowse("a", [entry("b")], catalogs())).toBeNull();
  expect(catalogToBrowse("a", [], catalogs())).toBeNull();
});

test("an unloaded list decides nothing yet", () => {
  expect(catalogToBrowse("a", null, catalogs())).toBeNull();
});

test("a removal whose list read failed shows nothing, never a retry hint", () => {
  const error = new WireError("marketplace removed, but the updated list was unavailable", -32603, {
    evenerErrorInfo: ErrorMarketplaceRemoveApplied,
    appliedUnavailable: true,
  });
  expect(appliedRemovalNotice(error)).toBeNull();
});

test("a clone-litter removal keeps its leftover-files warning", () => {
  const error = new WireError("clone could not be removed", -32603, {
    evenerErrorInfo: "marketplaceUnregisteredCloneRemains",
    applied: { marketplaces: [] },
  });
  expect(appliedRemovalNotice(error)).toBe(
    "Marketplace removed; clone cleanup failed. Remove the leftover clone files manually.",
  );
});

test("an ordinary removal failure is not an applied removal", () => {
  expect(appliedRemovalNotice(new Error("remove failed"))).toBeUndefined();
});

test("an add's answer names every registration it left against the stale list", () => {
  // A fresh name, a fresh stamp, and a re-sourcing all show up, in order.
  expect(
    addedMarketplaceNames([entry("a"), entry("b")], [
      entry("a"),
      entry("b"),
      entry("c"),
      { ...entry("b"), lastUpdated: 2 },
      { ...entry("a"), source: { kind: "github", repo: "a/plugins" } },
    ]),
  ).toEqual(["c", "b", "a"]);
});

test("a re-add the wire cannot tell from the stale row hides from the list", () => {
  expect(addedMarketplaceNames([entry("a")], [entry("a")])).toEqual([]);
  expect(addedMarketplaceNames([entry("a")], [])).toEqual([]);
});

test("a stale list that still carries the removed name refreshes; a reconciled one does not", () => {
  const storeWith = (marketplaces: readonly MarketplaceEntry[] | null) => ({
    getState: () => ({ marketplaces }),
  });
  expect(refetchAfterRemoval(storeWith([entry("a")]), "a")).toBe(true);
  expect(refetchAfterRemoval(storeWith([entry("b")]), "a")).toBe(false);
  expect(refetchAfterRemoval(storeWith(null), "a")).toBe(true);
});

test("an applied removal whose authoritative list the store already published needs no refetch", async () => {
  const fake = new FakeClient("ready");
  const store = createMarketplacesStore(fake);
  fake.on("evener/marketplace/list", () => ({ marketplaces: [entry("acme")] }));
  await store.getState().fetchMarketplaces();
  const error = new WireError("clone could not be removed", -32603, {
    evenerErrorInfo: "marketplaceUnregisteredCloneRemains",
    applied: { marketplaces: [] },
  });
  fake.on("evener/marketplace/remove", () => {
    throw error;
  });
  await expect(store.getState().removeMarketplace("acme")).rejects.toBe(error);
  // The store reconciled from the rejection's authoritative applied list...
  expect(store.getState().marketplaces).toEqual([]);
  // ...so the refetch decision, reading the live store, asks for nothing.
  expect(refetchAfterRemoval(store, "acme")).toBe(false);
});
