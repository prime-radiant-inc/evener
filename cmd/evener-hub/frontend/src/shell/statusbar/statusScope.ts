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
import { activeWatchCount, subagentIsCurrent } from "../rail/railNodes";

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

export interface ScopeSubagents {
  // The session's direct children across every loaded page, in page order
  // (each row keeps its own nested subtree). The Agents tab renders these.
  rows: NavigationSessionSummary[];
  // The deepest page's remainder: direct children the wire has not sent yet
  // (reachable by fetching the next page).
  remaining: number;
}

export interface ActivityScope {
  leaf: NavigationSessionSummary;
  path: ScopeCrumb[];
  counts: ScopeCounts;
  // The leaf's paged subagents resource, or null when no page has loaded.
  // Null is a loading state, never "no subagents".
  subagents: ScopeSubagents | null;
}

function locationOf(ref: string, navigation: NavigationStoreState): NavigationSessionLocation | undefined {
  const resource = selectLocation(ref)(navigation);
  return resource?.data as NavigationSessionLocation | undefined;
}

// subagentsOf gathers the session's loaded subagents pages in offset order.
// Lists and locations carry no children anymore; this resource is the tree.
function subagentsOf(navigation: NavigationStoreState, ref: string): ScopeSubagents | null {
  const pages: { offset: number; sessions: NavigationSessionSummary[]; remaining: number }[] = [];
  for (const resource of navigation.resources.values()) {
    const key = resource.key;
    if (key.kind !== "subagents" || key.ref !== ref || !resource.data) continue;
    const data = resource.data as { sessions?: NavigationSessionSummary[]; remaining?: number };
    pages.push({ offset: key.offset, sessions: data.sessions ?? [], remaining: data.remaining ?? 0 });
  }
  if (pages.length === 0) return null;
  pages.sort((a, b) => a.offset - b.offset);
  const last = pages[pages.length - 1];
  return { rows: pages.flatMap((page) => page.sessions), remaining: last ? last.remaining : 0 };
}

export function scopeCounts(session: NavigationSessionSummary, subagents: ScopeSubagents | null): ScopeCounts {
  return {
    // Agents come from the subagents resource's rows (fork originals are not
    // agents), never from the flat leaf. Unloaded resource: zero, briefly,
    // until the page lands - the surfaces ensure the fetch.
    activeSubagents: subagents
      ? subagents.rows.filter((row) => row.kind === "subagent" && subagentIsCurrent(row)).length
      : 0,
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
  // The root's flat summary carries its own crumb; the path below it walks
  // the root's subagents rows (the lists carry no children anymore).
  const root = selectSessionSummary(topLevelRef, navigation) ?? locationOf(topLevelRef, navigation)?.session;
  const rootRows = subagentsOf(navigation, topLevelRef)?.rows ?? [];
  const walk = (node: NavigationSessionSummary, ancestors: ScopeCrumb[]): ScopeCrumb[] | null => {
    const here = [...ancestors, { ref: node.ref, title: node.title }];
    if (node.ref === leafRef) return here;
    for (const child of node.children ?? []) {
      const found = walk(child, here);
      if (found) return found;
    }
    return null;
  };
  let full: ScopeCrumb[] | null = null;
  if (root) {
    const start = [{ ref: root.ref, title: root.title }];
    if (root.ref === leafRef) full = start;
    else {
      for (const row of rootRows) {
        const found = walk(row, start);
        if (found) {
          full = found;
          break;
        }
      }
    }
  }
  // An ancestor past the loaded pages degrades to the leaf alone, the same
  // contract a partially loaded tree always had.
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
  // One resolution feeds the path, the counts, and the subagents page (a
  // second full-store DFS for the leaf was the review's efficiency finding).
  const resolved = resolveLeaf(navigation, leafRef);
  if (!resolved) return null;
  const subagents = subagentsOf(navigation, leafRef);
  return {
    leaf: resolved.leaf,
    path: pathFor(navigation, leafRef, resolved),
    counts: scopeCounts(resolved.leaf, subagents),
    subagents,
  };
}
