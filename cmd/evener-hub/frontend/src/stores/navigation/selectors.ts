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
  return pages.sort((a, b) => a.offset - b.offset || a.limit - b.limit).flatMap((page) => page.sessions);
}
export function selectGlobalRows(state = navigationStore.getState()): NavigationSessionSummary[] {
  return ["live", "needs_you"].flatMap((section) =>
    loadedSectionRows(state, (key) => key.kind === "section" && key.section === section),
  );
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
export function findSessionNode(ref: string, state = navigationStore.getState()): NavigationSessionSummary | null {
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
      if (data?.session) rows.push(data.session);
    }
  }
  return walk(rows);
}
