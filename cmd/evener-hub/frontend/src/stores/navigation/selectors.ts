import type { NavigationProjectSummary, NavigationSessionSummary } from "../../protocol/types.gen";
import { navigationStore } from "./store";
import { keyID, type ResourceKey, type ResourceState } from "./types";
export const selectAttentionSummary = (s: ReturnType<typeof navigationStore.getState>) => s.attention.summary;
export const selectResource = (key: ResourceKey) => (s: ReturnType<typeof navigationStore.getState>) =>
  s.resources.get(keyID(key));
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
  return walk(selectGlobalRows(state));
}
