import type {
  MarketplaceEntry,
} from "@evener/appwire-client";
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

function marketplaceEntryKey(entry: MarketplaceEntry): string {
  return JSON.stringify([
    entry.name,
    entry.source.kind,
    entry.source.repo ?? null,
    entry.source.url ?? null,
    entry.source.path ?? null,
    entry.source.ref ?? null,
    entry.source.sha ?? null,
    entry.lastUpdated,
  ]);
}

/** The names a successful marketplace add registered, read off the list the
 * add's own answer published against the list the screen last carried: an
 * entry the answer newly carries is the registration the add left, however
 * the hub named it. A re-registration the wire cannot tell from the stale
 * row it replaced - same name, same source, same whole-second stamp - is
 * invisible to any list, and nothing names it here: the add's own answer
 * publishes as a trusted whole-list write, and its report retires the
 * fence the way every such publication does. */
export function addedMarketplaceNames(
  before: readonly MarketplaceEntry[],
  after: readonly MarketplaceEntry[],
): string[] {
  const seen = new Set(before.map(marketplaceEntryKey));
  const added: string[] = [];
  for (const entry of after) {
    const key = marketplaceEntryKey(entry);
    if (seen.has(key)) continue;
    seen.add(key);
    added.push(entry.name);
  }
  return added;
}

