import type { NavigationProjectSummary, NavigationSessionSummary } from "../../protocol/types.gen";
import { navigationStore } from "./store";
import { keyID, type ResourceKey, type ResourceState } from "./types";
export const selectAttentionSummary = (s: ReturnType<typeof navigationStore.getState>) => s.attention.summary;
export const selectResource = (key: ResourceKey) => (s: ReturnType<typeof navigationStore.getState>) =>
  s.resources.get(keyID(key));
export const selectProjectResource = (projectKey: string) => (s: ReturnType<typeof navigationStore.getState>) =>
  s.resources.get(keyID({ kind: "project", projectKey }));
export const selectProjectPage =
  (projectKey: string, tier: "current" | "recent" | "archived", offset = 0, limit = 50) =>
  (s: ReturnType<typeof navigationStore.getState>) =>
    s.resources.get(keyID({ kind: "project_page", projectKey, tier, offset, limit }));
export const selectLocation = (ref: string) => (s: ReturnType<typeof navigationStore.getState>) =>
  s.resources.get(keyID({ kind: "location", ref }));
export function selectSectionRows(
  section: "live" | "needs_you",
  state = navigationStore.getState(),
): NavigationSessionSummary[] {
  return loadedSectionRows(state, (key) => key.kind === "section" && key.section === section);
}
export function selectNeedsYouRows(state = navigationStore.getState()): NavigationSessionSummary[] {
  return selectSectionRows("needs_you", state);
}
export function selectNeedsYouCount(state = navigationStore.getState()): number {
  return state.manifest?.data?.sections.needs_you.count ?? selectNeedsYouRows(state).length;
}
export function selectSectionRemaining(section: "live" | "needs_you", state = navigationStore.getState()): number {
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
export function selectNextSectionOffset(section: "live" | "needs_you", state = navigationStore.getState()): number {
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
  return last.key.offset + ((last.data as { sessions?: unknown[] } | null)?.sessions?.length ?? 0);
}
export function selectLiveRows(state = navigationStore.getState()): NavigationSessionSummary[] {
  return selectSectionRows("live", state);
}
function loadedSectionRows(
  state: ReturnType<typeof navigationStore.getState>,
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
export function selectGlobalRows(state = navigationStore.getState()): NavigationSessionSummary[] {
  return [...selectLiveRows(state), ...selectNeedsYouRows(state)];
}
export interface LoadedPinSection {
  id: string;
  name: string;
  sessions: NavigationSessionSummary[];
}
export function selectPinSections(state = navigationStore.getState()): LoadedPinSection[] {
  const descriptors = [...state.resources.values()]
    .filter((resource) => resource.key.kind === "pin_catalog" && resource.data !== null)
    .sort((a, b) => {
      const left = a.key.kind === "pin_catalog" ? a.key.offset : 0;
      const right = b.key.kind === "pin_catalog" ? b.key.offset : 0;
      return left - right;
    })
    .flatMap((resource) => {
      const data = resource.data as { pin_sections?: Array<{ id: string; name: string }> } | null;
      return data?.pin_sections ?? [];
    });
  const seen = new Set<string>();
  return descriptors.flatMap((descriptor) => {
    if (seen.has(descriptor.id)) return [];
    seen.add(descriptor.id);
    return [
      {
        id: descriptor.id,
        name: descriptor.name,
        sessions: loadedSectionRows(state, (key) => key.kind === "pin_section" && key.sectionId === descriptor.id),
      },
    ];
  });
}
export function selectProjectSummaries(state = navigationStore.getState()): NavigationProjectSummary[] {
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
export const selectExpanded = (projectKey: string) => (s: ReturnType<typeof navigationStore.getState>) =>
  s.expanded.get(projectKey) ?? selectProjectSummaries(s).find((p) => p.key === projectKey)?.default_expanded ?? false;
export function selectSessionSummary(ref: string, state = navigationStore.getState()): NavigationSessionSummary | null {
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
      const status = (resource.error as { status?: unknown } | null)?.status;
      if (status !== 404 && data?.session) rows.push(data.session);
    }
  }
  return walk(rows);
}
export const findSessionNode = selectSessionSummary;
