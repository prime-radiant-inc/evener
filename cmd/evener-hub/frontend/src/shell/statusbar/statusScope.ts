// Activity context and summary are authoritative for the selected session.
// Navigation supplies a title and embedded Tasks when that exact row exists.
import type { NavigationSessionSummary, SessionActivitySnapshot } from "@evener/appwire-client";
import { type NavigationStoreState, selectSessionSummary } from "../../stores/navigation/selectors";

export type ActivityTab = "agents" | "jobs" | "watches" | "tasks";
export interface ScopeCrumb {
  ref: string;
  title: string;
}
export interface ScopeCounts {
  activeSubagents: number | null;
  runningJobs: number | null;
  armedWatches: number | null;
  tasksDone: number;
  tasksTotal: number;
}
export interface ActivityScope {
  leaf: Pick<NavigationSessionSummary, "ref" | "title" | "tasks">;
  path: ScopeCrumb[];
  ancestryKnown: boolean;
  counts: ScopeCounts;
  activity: SessionActivitySnapshot | null;
}

export function scopeCounts(session: ActivityScope["leaf"], activity: SessionActivitySnapshot | null): ScopeCounts {
  const summary = activity?.summary;
  return {
    activeSubagents: summary?.delegates.known ? summary.delegates.active : null,
    runningJobs: summary?.jobs.known ? summary.jobs.active : null,
    armedWatches: summary?.watches.known ? summary.watches.active : null,
    tasksDone: session.tasks?.done ?? 0,
    tasksTotal: session.tasks?.total ?? 0,
  };
}

export function deriveScope(
  navigation: NavigationStoreState,
  ref: string,
  activity: SessionActivitySnapshot | null = null,
): ActivityScope {
  const row = selectSessionSummary(ref, navigation);
  const leaf = {
    ref,
    title: row?.ref === ref ? row.title : ref,
    ...(row?.ref === ref && row.tasks ? { tasks: row.tasks } : {}),
  };
  const context = activity?.context;
  const ancestryKnown = context?.ancestryKnown === true;
  return {
    leaf,
    path: [
      ...(ancestryKnown
        ? context.ancestors.map((ancestor) => ({ ref: ancestor.ref, title: ancestor.title || ancestor.ref }))
        : []),
      { ref, title: leaf.title },
    ],
    ancestryKnown,
    counts: scopeCounts(leaf, activity),
    activity,
  };
}
