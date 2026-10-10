// Activity context and summary are authoritative for the selected session.
// Navigation supplies a title and embedded Tasks when that exact row exists.
import type { NavigationSessionSummary, SessionActivitySnapshot, SessionActivitySummary } from "@evener/appwire-client";
import { taskSettledCount } from "@evener/appwire-client";
import { type NavigationStoreState, selectSessionSummary } from "../../stores/navigation/selectors";

export type ActivityTab = "agents" | "jobs" | "watches" | "tasks" | "about";
export interface ScopeCrumb {
  ref: string;
  title: string;
}
export interface ScopeCounts {
  activeSubagents: number | null;
  delegatesTotal: number | null;
  runningJobs: number | null;
  jobsTotal: number | null;
  armedWatches: number | null;
  watchesTotal: number | null;
  tasksSettled: number;
  tasksTotal: number;
}
export interface ActivityScope {
  leaf: Pick<NavigationSessionSummary, "ref" | "title" | "tasks">;
  path: ScopeCrumb[];
  ancestryKnown: boolean;
  counts: ScopeCounts;
  activity: SessionActivitySnapshot | null;
}

/** Subagents are counted at every depth (useSubagentCounts), the other
 * kinds for the session itself. */
export function scopeCounts(
  session: ActivityScope["leaf"],
  activity: SessionActivitySnapshot | null,
  subagents: SessionActivitySummary["delegates"] | null,
): ScopeCounts {
  const summary = activity?.summary;
  return {
    activeSubagents: subagents?.active ?? null,
    delegatesTotal: subagents?.total ?? null,
    runningJobs: summary?.jobs.known ? summary.jobs.active : null,
    jobsTotal: summary?.jobs.known ? summary.jobs.total : null,
    armedWatches: summary?.watches.known ? summary.watches.active : null,
    watchesTotal: summary?.watches.known ? summary.watches.total : null,
    // The panel's settled group and the inline card's meter count the same
    // tasks, so the chip must too.
    tasksSettled: session.tasks ? taskSettledCount(session.tasks) : 0,
    tasksTotal: session.tasks?.total ?? 0,
  };
}

export function deriveScope(
  navigation: NavigationStoreState,
  ref: string,
  activity: SessionActivitySnapshot | null,
  // Required, so no surface that shows the Agents count can leave it out.
  subagents: SessionActivitySummary["delegates"] | null,
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
    counts: scopeCounts(leaf, activity, subagents),
    activity,
  };
}
