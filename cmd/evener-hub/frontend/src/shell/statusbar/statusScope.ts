// statusScope derives what the activity status bar and sidebar describe - the
// path of sessions from the root to the one you're reading, and the four
// activity counts (agents, jobs, watches, tasks) - from the navigation store.
// Nothing here owns activity state: the store is the truth, these are reads.
//
// Counts report active work only. The rail's own predicates decide what
// counts: subagentIsCurrent for agents (the same split the rail's tree uses),
// activeWatchCount for armed watches including the hub-omitted rows. The
// completed/inactive folds never inflate a counter.

import type { NavigationSessionLocation, NavigationSessionSummary } from "@evener/appwire-client";
import { type NavigationStoreState, selectLocation, selectSessionSummary } from "../../stores/navigation/selectors";
import { activeWatchCount, subagentChildrenOf, subagentIsCurrent } from "../rail/railNodes";

export type ActivityTab = "agents" | "jobs" | "watches" | "tasks";

export interface ScopeCrumb {
  ref: string;
  title: string;
}

export interface ScopeCounts {
  activeSubagents: number;
  runningJobs: number;
  armedWatches: number;
  tasksDone: number;
  tasksTotal: number;
}

export interface ActivityScope {
  leaf: NavigationSessionSummary;
  path: ScopeCrumb[];
  counts: ScopeCounts;
}

function locationOf(ref: string, navigation: NavigationStoreState): NavigationSessionLocation | undefined {
  const resource = selectLocation(ref)(navigation);
  return resource?.data as NavigationSessionLocation | undefined;
}

export function scopeCounts(session: NavigationSessionSummary): ScopeCounts {
  return {
    activeSubagents: subagentChildrenOf(session).filter(subagentIsCurrent).length,
    runningJobs: session.running_jobs?.length ?? 0,
    armedWatches: activeWatchCount(session),
    tasksDone: session.tasks?.done ?? 0,
    tasksTotal: session.tasks?.total ?? 0,
  };
}

function resolveLeaf(
  navigation: NavigationStoreState,
  leafRef: string,
): { leaf: NavigationSessionSummary; topLevelRef: string | null } | null {
  const location = locationOf(leafRef, navigation);
  const leaf = location?.session ?? selectSessionSummary(leafRef, navigation);
  if (!leaf) return null;
  return { leaf, topLevelRef: location?.top_level_ref ?? null };
}

function pathFor(
  navigation: NavigationStoreState,
  leafRef: string,
  resolved: { leaf: NavigationSessionSummary; topLevelRef: string | null },
): ScopeCrumb[] {
  const { leaf, topLevelRef } = resolved;
  if (topLevelRef === null || topLevelRef === leafRef) {
    return [{ ref: leaf.ref, title: leaf.title }];
  }
  const root = selectSessionSummary(topLevelRef, navigation);
  const walk = (node: NavigationSessionSummary, ancestors: ScopeCrumb[]): ScopeCrumb[] | null => {
    const here = [...ancestors, { ref: node.ref, title: node.title }];
    if (node.ref === leafRef) return here;
    for (const child of node.children ?? []) {
      const found = walk(child, here);
      if (found) return found;
    }
    return null;
  };
  const full = root ? walk(root, []) : null;
  return full ?? [{ ref: leaf.ref, title: leaf.title }];
}

/** The title path from the root that contains `leafRef` down to it, walking
 * the root's loaded children. A tree that doesn't contain the ref (a
 * partially loaded project) degrades to the leaf alone rather than throwing,
 * and a ref the store has never heard of returns null. */
export function scopePath(navigation: NavigationStoreState, leafRef: string): ScopeCrumb[] | null {
  const resolved = resolveLeaf(navigation, leafRef);
  if (!resolved) return null;
  return pathFor(navigation, leafRef, resolved);
}

export function deriveScope(navigation: NavigationStoreState, leafRef: string): ActivityScope | null {
  // One resolution feeds both the path and the counts (a second full-store
  // DFS for the leaf was the review's efficiency finding).
  const resolved = resolveLeaf(navigation, leafRef);
  if (!resolved) return null;
  return { leaf: resolved.leaf, path: pathFor(navigation, leafRef, resolved), counts: scopeCounts(resolved.leaf) };
}
