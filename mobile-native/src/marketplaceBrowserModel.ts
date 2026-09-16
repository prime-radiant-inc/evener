import type { MarketplaceEntry } from "@evener/appwire-client";
import type { MarketplaceCatalogEntry } from "@evener/appwire-client/state/extensions";

/** The marketplace whose catalog the browser should request now: the selected
 * one, once the list has loaded and still carries it, when the store has no
 * cache entry for it. A selection the list dropped is about to be cleared,
 * so requesting its catalog would be a wasted browse of a retired name. */
export function catalogToBrowse(
  selected: string | null,
  marketplaces: readonly MarketplaceEntry[] | null,
  browseCatalogs: ReadonlyMap<string, MarketplaceCatalogEntry>,
): string | null {
  if (!selected || browseCatalogs.has(selected)) return null;
  return marketplaces?.some((item) => item.name === selected) ? selected : null;
}
