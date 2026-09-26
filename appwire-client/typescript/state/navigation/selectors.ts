// The navigation selectors the web app reads a navigation store's state
// through (native has no `createNavigationStore` and adopts none of these
// yet, but could if it ever gains one): the manifest's launch sources,
// section rows with their remaining count and next offset, the pin-section
// summaries, the project catalog, and a session summary found by ref. Pure
// over NavigationStoreState - no store handle, no host API - so an app
// passes whichever instance's state it holds, and a selector that memoizes
// for one host's render loop stays in that host.
import type { NavigationProjectSummary, NavigationSessionSummary, Source } from "../../types.gen";
import type { NavigationStoreState } from "./store";
import {
  canonicalResourceKey,
  isNavigationUnavailable,
  keyID,
  navigationRootContainerKey,
  nextNavigationOffset,
  type ResourceKey,
  type ResourceState,
} from "./types";

/** Relative display age for a session's updated_at, mirroring the rail's
 * long-standing row contract (now/m/h/d).
 *
 * The instant it measures against is an argument, not an ambient read: a
 * caller that renders a live label must pass a ticking `now`, or the label
 * freezes at whatever the adapter observed when the summary last changed -
 * an idle session never changes, so it would sit at "now" until a refresh.
 * The default keeps every snapshot caller (and the package's own tests)
 * unchanged. */
export function relativeAge(updatedAt?: string, now: number = Date.now()): string | undefined {
  if (!updatedAt) return undefined;
  const timestamp = Date.parse(updatedAt);
  if (!Number.isFinite(timestamp)) return undefined;
  const seconds = Math.max(0, Math.floor((now - timestamp) / 1000));
  if (seconds < 60) return "now";
  if (seconds < 3600) return `${Math.floor(seconds / 60)}m`;
  if (seconds < 86400) return `${Math.floor(seconds / 3600)}h`;
  return `${Math.floor(seconds / 86400)}d`;
}

function normalizedRootCount(resource: ResourceState, slot: string): number | undefined {
  const normalized = resource.normalized;
  if (!normalized) return undefined;
  return normalized.graph.containers.get(navigationRootContainerKey(resource.key, slot))?.children.length ?? 0;
}
export const selectAttentionSummary = (s: NavigationStoreState) => s.attention.summary;
/** The manifest's configured launch sources (Component 06a). Empty until the
 * manifest loads, so a consumer can render across-host affordances only when a
 * remote host actually exists - the single-host UI stays untouched. */
const NO_SOURCES: Source[] = [];
export function selectSources(state: NavigationStoreState): Source[] {
  // A stable empty array, NOT a fresh `[]`: this selector is read through
  // useSyncExternalStore, whose snapshot identity must not change on every
  // call or the store's subscribers re-render forever.
  //
  // Only a SETTLED manifest may name launch sources. A resource keeps its last
  // snapshot while loading, re-validating (`stale`), or after a failed read,
  // and for an invalidation/reconnect that snapshot can list a host the fresh
  // manifest has since removed or taken offline. Exposing it would let a
  // consumer treat an outdated host as launchable - the picker would offer it
  // and the form could submit to it - instead of falling back to local. This
  // mirrors the store's settledness contract for normalized reads
  // (`settledPresence` also refuses a stale resource). A consumer that must not
  // lose a persisted choice while the manifest is in flight (the spawn draft)
  // retains its own value rather than reading this empty list as a fallback.
  const manifest = state.manifest;
  if (!manifest || manifest.loading || manifest.stale || manifest.error) return NO_SOURCES;
  return manifest.data?.sources ?? NO_SOURCES;
}
/** The same manifest sources, for DISPLAY only: the last-known list, whether
 * or not the read that produced it is still authoritative. A resource keeps its
 * last snapshot while loading, re-validating (`stale`), or after a failed read,
 * and a display consumer that withheld it would make the host picker disappear
 * and every remote row's offline badge flip to ONLINE for the length of the
 * refresh - the retained list is the best available description of what the
 * reader is looking at (Component 06b review, round nine).
 *
 * Only display reads this. Which host a launch may actually use is decided by
 * selectSources' settled list, so a host the fresh manifest has since removed
 * is never launchable merely because the UI still shows it. */
export function selectDisplaySources(state: NavigationStoreState): Source[] {
  return state.manifest?.data?.sources ?? NO_SOURCES;
}
const selectResource = (key: ResourceKey) => {
  const resourceKey = canonicalResourceKey(key);
  return (s: NavigationStoreState) => s.resources.get(keyID(resourceKey));
};
export const selectProjectResource = (projectKey: string) => (s: NavigationStoreState) =>
  s.resources.get(keyID({ kind: "project", projectKey }));
export const selectProjectPage =
  (projectKey: string, tier: "current" | "recent" | "archived", offset = 0, limit = 50) =>
  (s: NavigationStoreState) =>
    s.resources.get(keyID({ kind: "project_page", projectKey, tier, offset, limit }));
export const selectLocation = (ref: string) => selectResource({ kind: "location", ref });
function selectSectionRows(section: "live" | "needs_you", state: NavigationStoreState): NavigationSessionSummary[] {
  return loadedSectionRows(state, (key) => key.kind === "section" && key.section === section);
}
export function selectNeedsYouRows(state: NavigationStoreState): NavigationSessionSummary[] {
  return selectSectionRows("needs_you", state);
}
export function selectNeedsYouCount(state: NavigationStoreState): number {
  return state.manifest?.data?.sections.needs_you.count ?? selectNeedsYouRows(state).length;
}
export function selectSectionRemaining(section: "live" | "needs_you", state: NavigationStoreState): number {
  const pages = [...state.resources.values()].filter(
    (resource) => resource.key.kind === "section" && resource.key.section === section && resource.data !== null,
  );
  const last = pages
    .sort((a, b) =>
      a.key.kind === "section" && b.key.kind === "section"
        ? a.key.offset - b.key.offset || a.key.limit - b.key.limit
        : 0,
    )
    .at(-1);
  return (last?.data as { remaining?: number } | null)?.remaining ?? 0;
}
export function selectNextSectionOffset(section: "live" | "needs_you", state: NavigationStoreState): number {
  const pages = [...state.resources.values()].filter(
    (resource) => resource.key.kind === "section" && resource.key.section === section,
  );
  const last = pages
    .sort((a, b) =>
      a.key.kind === "section" && b.key.kind === "section"
        ? a.key.offset - b.key.offset || a.key.limit - b.key.limit
        : 0,
    )
    .at(-1);
  if (last?.key.kind !== "section") return 0;
  const returned =
    normalizedRootCount(last, "sessions") ??
    (last.data as { sessions?: NavigationSessionSummary[] } | null)?.sessions?.length ??
    0;
  return nextNavigationOffset(last.key.offset, returned);
}
export function selectLiveRows(state: NavigationStoreState): NavigationSessionSummary[] {
  return selectSectionRows("live", state);
}
function loadedSectionRows(
  state: NavigationStoreState,
  predicate: (key: Extract<ResourceKey, { kind: "section" | "pin_section" }>) => boolean,
): NavigationSessionSummary[] {
  const pages: Array<{ offset: number; limit: number; sessions: NavigationSessionSummary[] }> = [];
  for (const resource of state.resources.values()) {
    if (
      (resource.key.kind !== "section" && resource.key.kind !== "pin_section") ||
      !predicate(resource.key) ||
      resource.data === null
    )
      continue;
    pages.push({
      offset: resource.key.offset,
      limit: resource.key.limit,
      sessions: (resource as ResourceState<{ sessions: NavigationSessionSummary[] }>).data?.sessions ?? [],
    });
  }
  const seen = new Set<string>();
  return pages
    .sort((a, b) => a.offset - b.offset || a.limit - b.limit)
    .flatMap((page) => page.sessions.filter((session) => !seen.has(session.ref) && seen.add(session.ref)));
}
export function selectGlobalRows(state: NavigationStoreState): NavigationSessionSummary[] {
  return [...selectLiveRows(state), ...selectNeedsYouRows(state)];
}
export interface NavigationPinSectionSummary {
  id: string;
  name: string;
  member_count: number;
}
interface LoadedPinSection extends NavigationPinSectionSummary {
  sessions: NavigationSessionSummary[];
}
export function selectPinSectionSummaries(state: NavigationStoreState): NavigationPinSectionSummary[] {
  const descriptors = [...state.resources.values()]
    .filter((resource) => resource.key.kind === "pin_catalog" && resource.data !== null)
    .sort((a, b) => {
      const left = a.key.kind === "pin_catalog" ? a.key.offset : 0;
      const right = b.key.kind === "pin_catalog" ? b.key.offset : 0;
      return left - right;
    })
    .flatMap((resource) => {
      const data = resource.data as { pin_sections?: Array<{ id: string; name: string; count: number }> } | null;
      return data?.pin_sections ?? [];
    });
  const seen = new Set<string>();
  return descriptors.flatMap((descriptor) => {
    if (seen.has(descriptor.id)) return [];
    seen.add(descriptor.id);
    return [{ id: descriptor.id, name: descriptor.name, member_count: descriptor.count }];
  });
}
export function selectPinSections(state: NavigationStoreState): LoadedPinSection[] {
  return selectPinSectionSummaries(state).map((section) => ({
    ...section,
    sessions: loadedSectionRows(state, (key) => key.kind === "pin_section" && key.sectionId === section.id),
  }));
}
/** No caller outside `selectExpanded` today, but kept exported: the core's
 * private `selectSummaries` in `store.ts` duplicates this scan for the boot
 * fan-out, unsorted where this sorts by catalog order then offset. Folding
 * the two would change which projects hydrate first under the boot pool, so
 * this stays a public, sorted counterpart until that's resolved (#1596). */
export function selectProjectSummaries(state: NavigationStoreState): NavigationProjectSummary[] {
  const catalogOrder = { projects: 0, archived_projects: 1, test_runs: 2 } as const;
  return [...state.resources.values()]
    .filter((resource) => resource.key.kind === "catalog" && resource.data !== null)
    .sort((a, b) => {
      if (a.key.kind !== "catalog" || b.key.kind !== "catalog") return 0;
      return catalogOrder[a.key.catalog] - catalogOrder[b.key.catalog] || a.key.offset - b.key.offset;
    })
    .flatMap((resource) => {
      const data = resource.data as { projects?: NavigationProjectSummary[] } | null;
      return data?.projects ?? [];
    });
}
export const selectExpanded = (projectKey: string) => (s: NavigationStoreState) =>
  s.expanded.get(projectKey) ?? selectProjectSummaries(s).find((p) => p.key === projectKey)?.default_expanded ?? false;
export function selectSessionSummary(ref: string, state: NavigationStoreState): NavigationSessionSummary | null {
  const walk = (xs: NavigationSessionSummary[]): NavigationSessionSummary | null => {
    for (const x of xs) {
      if (x.ref === ref) return x;
      const y = walk(x.children);
      if (y) return y;
    }
    return null;
  };
  const rows = [...selectGlobalRows(state), ...selectPinSections(state).flatMap((section) => section.sessions)];
  for (const resource of state.resources.values()) {
    if (resource.key.kind === "project_page") {
      const data = resource.data as { sessions?: NavigationSessionSummary[] } | null;
      if (data?.sessions) rows.push(...data.sessions);
    }
    if (resource.key.kind === "project") {
      const data = resource.data as {
        current?: { sessions?: NavigationSessionSummary[] };
        recent?: { sessions?: NavigationSessionSummary[] };
        archived?: { sessions?: NavigationSessionSummary[] };
      } | null;
      for (const tier of [data?.current, data?.recent, data?.archived]) if (tier?.sessions) rows.push(...tier.sessions);
    }
    if (resource.key.kind === "location") {
      const data = resource.data as { session?: NavigationSessionSummary } | null;
      if (!isNavigationUnavailable(resource.error) && data?.session) rows.push(data.session);
    }
  }
  return walk(rows);
}
export const findSessionNode = selectSessionSummary;

/** The exact number of live-watch rows the hub omitted from `ref`'s summary
 * (over the per-session cap, unrepresentable, or shed by the byte fitter). Zero
 * when the session is not materialized or carries no count. The Activity panel
 * header reads this so it never silently undercounts. */
export function selectSessionOmittedWatches(ref: string, state: NavigationStoreState): number {
  const summary = selectSessionSummary(ref, state);
  return summary?.omitted_watches ?? 0;
}

/** The armed subset of the rows `selectSessionOmittedWatches` counts. A session
 * whose armed watches exceed the hub's per-session cap retains only the first
 * of them, so the retained list alone cannot state the true armed total; the
 * rail and the Activity panel add this to the armed rows they can see. Zero
 * when the session is not materialized or carries no count. */
export function selectSessionOmittedArmedWatches(ref: string, state: NavigationStoreState): number {
  const summary = selectSessionSummary(ref, state);
  return summary?.omitted_armed_watches ?? 0;
}
