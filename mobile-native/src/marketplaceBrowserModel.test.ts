import { expect, test } from "vitest";
import { ErrorMarketplaceRemoveApplied, WireError } from "@evener/appwire-client";
import type { MarketplaceEntry } from "@evener/appwire-client";
import type { MarketplaceCatalogEntry } from "@evener/appwire-client/state/extensions";
import {
  appliedRemovalNotice,
  catalogToBrowse,
  shouldRefetchAfterRemoval,
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
  expect(appliedRemovalNotice(error)).toEqual({ notice: null });
});

test("a clone-litter removal keeps its leftover-files warning", () => {
  const error = new WireError("clone could not be removed", -32603, {
    evenerErrorInfo: "marketplaceUnregisteredCloneRemains",
    applied: { marketplaces: [] },
  });
  expect(appliedRemovalNotice(error)).toEqual({
    notice: "Marketplace removed; clone cleanup failed. Remove the leftover clone files manually.",
  });
});

test("an ordinary removal failure is not an applied removal", () => {
  expect(appliedRemovalNotice(new Error("remove failed"))).toBeUndefined();
});

test("a stale list that still carries the removed name refreshes; a reconciled one does not", () => {
  expect(shouldRefetchAfterRemoval([entry("a")], "a")).toBe(true);
  expect(shouldRefetchAfterRemoval([entry("b")], "a")).toBe(false);
  expect(shouldRefetchAfterRemoval(null, "a")).toBe(true);
});
