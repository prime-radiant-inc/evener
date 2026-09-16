import { expect, test } from "vitest";
import type { MarketplaceEntry } from "@evener/appwire-client";
import type { MarketplaceCatalogEntry } from "@evener/appwire-client/state/extensions";
import { catalogToBrowse } from "./marketplaceBrowserModel";

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
