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

/** Whether a marketplace write (add, remove, refresh) should refuse to
 * dispatch: this view's own write is already running, or the screen's
 * plugin-install gate is. The caller reads both live at dispatch time
 * (a ref for its own write, gate.isBusy() for the gate) rather than off a
 * render-time snapshot, because the gap between a button press and the
 * actual dispatch - a confirmation Alert, an open modal - can outlive the
 * render that last checked it. */
export function refusesMarketplaceWrite(mutating: boolean, gateBusy: boolean): boolean {
  return mutating || gateBusy;
}
