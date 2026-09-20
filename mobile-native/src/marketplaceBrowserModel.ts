import type { MarketplaceEntry } from "@evener/appwire-client";
import {
  type MarketplaceCatalogEntry,
  marketplaceRemovalOutcome,
} from "@evener/appwire-client/state/extensions";

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

const MARKETPLACE_REMOVED_LITTER =
  "Marketplace removed; clone cleanup failed. Remove the leftover clone files manually.";

/** What the browser shows when a marketplace removal rejects but the hub says
 * the removal already stood (appwire/errors.go: reconcile, never retry): the
 * answer is never the generic write-failed copy, whose implied retry targets a
 * marketplace that is already gone. undefined is an ordinary failure; null
 * is a removal whose list read failed (show nothing); the litter copy is a
 * removal whose clone cleanup failed. */
export function appliedRemovalNotice(
  error: unknown,
): string | null | undefined {
  const outcome = marketplaceRemovalOutcome(error);
  if (!outcome) return undefined;
  return outcome.kind === "removed" ? null : MARKETPLACE_REMOVED_LITTER;
}

/** Whether the browser should re-read the list after a removal rejection the
 * hub says already stood. Reads the store's CURRENT list - never a render
 * snapshot - because the store publishes the clone-litter applied list
 * itself during the rejection: a list that no longer carries the removed
 * name is already reconciled and needs nothing, while a stale list still
 * carrying it - or no list at all - must refresh. */
export function refetchAfterRemoval(
  store: { getState(): { marketplaces: readonly MarketplaceEntry[] | null } },
  name: string,
): boolean {
  const { marketplaces } = store.getState();
  return marketplaces === null || marketplaces.some((item) => item.name === name);
}
