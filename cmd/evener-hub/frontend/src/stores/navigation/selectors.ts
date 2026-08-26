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
export function selectGlobalRows(state = navigationStore.getState()): NavigationSessionSummary[] {
  const rows: NavigationSessionSummary[] = [];
  for (const k of [
    { kind: "section", section: "live", offset: 0, limit: 50 },
    { kind: "section", section: "needs_you", offset: 0, limit: 50 },
  ] as ResourceKey[]) {
    const r = state.resources.get(keyID(k)) as ResourceState<{ sessions: NavigationSessionSummary[] }> | undefined;
    if (r?.data) rows.push(...r.data.sessions);
  }
  return rows;
}
export function selectProjectSummaries(state = navigationStore.getState()): NavigationProjectSummary[] {
  const out: NavigationProjectSummary[] = [];
  for (const r of state.resources.values()) {
    const d = r.data as { projects?: NavigationProjectSummary[] } | null;
    if (r.key.kind === "catalog" && d?.projects) out.push(...d.projects);
  }
  return out;
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
  const rows = [...selectGlobalRows(state)];
  for (const resource of state.resources.values()) {
    if (resource.key.kind === "pin_section" || resource.key.kind === "project_page") {
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
