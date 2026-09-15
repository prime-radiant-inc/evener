// Pure row-model builder for the dense activity tree: walks a parsed
// ActivityTree and returns the flat list of rows to render. Live entries keep
// their original order; terminal entries collapse behind one fold row per
// parent session (revealed in original order when the fold is expanded).
// Sessions never become rows — the panel header covers the root and a delegate
// row stands in for its child session.

import {
  type ActivityDelegate,
  type ActivityEntry,
  type ActivityJob,
  type ActivitySessionNode,
  type ActivityTree,
  activityNodeID,
  delegateHasActiveWork,
  isActivityFailure,
  isFailedDelegateOutcome,
  isFailedJobOutcome,
  isTurnContainer,
} from "./activityData";
import { stableDelegateDisplayStatus } from "./stableDelegate";

export interface ActivityRowBase {
  id: string;
  parentID?: string;
  level: number;
}

export interface ActivityJobRow extends ActivityRowBase {
  kind: "job";
  job: ActivityJob;
  live: boolean;
  // The detail strip's default before any chevron override: top-level rows
  // open, rows revealed by expanding the inactive fold stay collapsed (the
  // fold click means "show the list", not "expand every child").
  defaultDetailOpen: boolean;
  transcriptRef?: string;
  parentRef: string;
}

export interface ActivityDelegateRow extends ActivityRowBase {
  kind: "delegate";
  delegate: ActivityDelegate;
  live: boolean;
  defaultDetailOpen: boolean;
  transcriptRef: string;
  parentRef: string;
}

export interface ActivityFoldRow extends ActivityRowBase {
  kind: "fold";
  foldParentID: string;
  inactiveCount: number;
  failedCount: number;
}

export type ActivityRow = ActivityJobRow | ActivityDelegateRow | ActivityFoldRow;

export function foldRowID(sessionNodeID: string): string {
  return `${sessionNodeID}:inactive-fold`;
}

export interface ActivityDelegateState {
  active: boolean;
  failed: boolean;
  status: string;
}

// A terminal entry's failure is the outcome the daemon already decided, so the
// rows, the fold's failure count, and the merged badge counts stay one number.
// Work that has not ended carries no outcome and can only say so through its
// current status.
export function jobIsFailed(job: ActivityJob): boolean {
  return job.terminal ? isFailedJobOutcome(job.outcome) : isActivityFailure(job.outcome, job.status);
}

// Stable delegates describe one reusable resource; other delegate types are
// turn containers. Keep this in one place so row visibility, fold failure
// counts, and the status shown by the row all use the protocol's same truth.
export function activityDelegateState(delegate: ActivityDelegate): ActivityDelegateState {
  const childActive = delegate.child ? sessionIsActive(delegate.child) : false;
  const childFailed = (delegate.child?.counts.failed ?? 0) > 0;
  if (!isTurnContainer(delegate)) {
    const status = stableDelegateDisplayStatus(delegate) ?? delegate.child?.aggregate ?? "unknown";
    const ownFailure =
      delegate.terminal === true ? isFailedDelegateOutcome(delegate.outcome) : isActivityFailure(undefined, status);
    return {
      active: delegateHasActiveWork(delegate),
      failed: ownFailure || childFailed,
      status,
    };
  }
  const turns = delegate.turns ?? [];
  let activeTurn: ActivityJob | undefined;
  for (const turn of turns) {
    if (!turn.terminal) activeTurn = turn;
  }
  const latest = turns.at(-1);
  if (turns.length === 0) {
    return {
      active: childActive,
      failed: childFailed,
      status: delegate.child?.aggregate ?? "unknown",
    };
  }
  const failed = childFailed || turns.some(jobIsFailed);
  const active = delegateHasActiveWork(delegate);
  return {
    active,
    failed,
    status: activeTurn
      ? activeTurn.status
      : childActive
        ? (delegate.child?.aggregate ?? "working")
        : failed
          ? "failed"
          : latest
            ? latest.status
            : (delegate.child?.aggregate ?? "unknown"),
  };
}

function jobIsActive(job: ActivityJob): boolean {
  return !job.terminal;
}

function sessionIsActive(session: ActivitySessionNode): boolean {
  return session.counts.active > 0 || session.entries.some(entryIsActive);
}

function entryIsActive(entry: ActivityEntry): boolean {
  if (entry.kind === "shell") return jobIsActive(entry.job);
  return activityDelegateState(entry.delegate).active;
}

function entryIsFailed(entry: ActivityEntry): boolean {
  if (entry.kind === "shell") return jobIsFailed(entry.job);
  const delegate: ActivityDelegate = entry.delegate;
  const state = activityDelegateState(delegate);
  return state.failed;
}

export function jobRowFields(
  job: ActivityJob,
  parentRef: string,
): Pick<ActivityJobRow, "job" | "live" | "transcriptRef" | "parentRef"> {
  return {
    job,
    live: jobIsActive(job),
    transcriptRef: job.transcriptRef,
    parentRef,
  };
}

export function delegateRowFields(
  delegate: ActivityDelegate,
  parentRef: string,
): Pick<ActivityDelegateRow, "delegate" | "live" | "transcriptRef" | "parentRef"> {
  return {
    delegate,
    live: activityDelegateState(delegate).active,
    transcriptRef: delegate.childRef,
    parentRef,
  };
}

function activityEntryRow(
  entry: ActivityEntry,
  parentRef: string,
  level: number,
  parentID: string,
  fromFold: boolean,
): ActivityJobRow | ActivityDelegateRow {
  const id = activityNodeID(entry);
  const defaultDetailOpen = level === 1 && !fromFold;
  if (entry.kind === "shell") {
    const fields = jobRowFields(entry.job, parentRef);
    return {
      kind: "job",
      id,
      parentID,
      level,
      job: fields.job,
      live: fields.live,
      defaultDetailOpen,
      transcriptRef: fields.transcriptRef,
      parentRef: fields.parentRef,
    };
  }
  const fields = delegateRowFields(entry.delegate, parentRef);
  return {
    kind: "delegate",
    id,
    parentID,
    level,
    delegate: fields.delegate,
    live: fields.live,
    defaultDetailOpen,
    transcriptRef: fields.transcriptRef,
    parentRef: fields.parentRef,
  };
}

// What one session hands a caller mid-walk: the level and parent id its rows
// carry, its entries, and the one way to turn an entry into a row. Emitting a
// row also descends into a delegate's child, so the recursion exists once.
interface ActivitySessionWalk {
  session: ActivitySessionNode;
  level: number;
  entriesParentID: string;
  entries: readonly ActivityEntry[];
  emit(entry: ActivityEntry, fromFold: boolean): void;
}

interface ActivityRowVisit {
  onRow(row: ActivityJobRow | ActivityDelegateRow): void;
  visitSession(walk: ActivitySessionWalk): void;
}

// The single recursive walk behind both surfaces below. It owns the row
// factory, the entries' parent id, and the descent into delegate children;
// callers own which entries they visit, in what order, and where a fold row
// belongs among them.
function walkActivitySessions(tree: ActivityTree, visit: ActivityRowVisit): void {
  function visitSession(session: ActivitySessionNode, level: number, parentID: string | undefined): void {
    const entriesParentID = parentID ?? activityNodeID(session);
    visit.visitSession({
      session,
      level,
      entriesParentID,
      entries: session.entries,
      emit(entry, fromFold) {
        const row = activityEntryRow(entry, session.ref, level, entriesParentID, fromFold);
        visit.onRow(row);
        if (entry.kind === "delegate" && entry.delegate.child) {
          visitSession(entry.delegate.child, level + 1, row.id);
        }
      },
    });
  }

  visitSession(tree.root, 1, undefined);
}

// Walks every loaded entry, ignoring fold/disclosure state, so an entity
// resolves identically whether or not its fold is expanded.
export function indexActivityEntities(tree: ActivityTree): Map<string, ActivityJobRow | ActivityDelegateRow> {
  const index = new Map<string, ActivityJobRow | ActivityDelegateRow>();

  walkActivitySessions(tree, {
    onRow(row) {
      if (row.kind === "job") {
        index.set(row.job.jobId, row);
        return;
      }
      index.set(row.delegate.delegateId, row);
    },
    // Every entry, in the session's own order, each carrying the fold origin
    // the panel would give it: index membership stays independent of what is
    // disclosed, while an indexed row still matches that entry's panel row.
    visitSession(walk) {
      for (const entry of walk.entries) walk.emit(entry, !entryIsActive(entry));
    },
  });

  return index;
}

export function buildActivityRows(tree: ActivityTree, expandedFolds: ReadonlySet<string>): ActivityRow[] {
  const rows: ActivityRow[] = [];

  walkActivitySessions(tree, {
    onRow(row) {
      rows.push(row);
    },
    visitSession(walk) {
      const live: ActivityEntry[] = [];
      const inactive: ActivityEntry[] = [];
      for (const entry of walk.entries) {
        (entryIsActive(entry) ? live : inactive).push(entry);
      }
      for (const entry of live) walk.emit(entry, false);
      if (inactive.length === 0) return;
      const id = foldRowID(activityNodeID(walk.session));
      rows.push({
        kind: "fold",
        id,
        parentID: walk.entriesParentID,
        level: walk.level,
        foldParentID: activityNodeID(walk.session),
        inactiveCount: inactive.length,
        failedCount: inactive.filter(entryIsFailed).length,
      });
      if (!expandedFolds.has(id)) return;
      for (const entry of inactive) walk.emit(entry, true);
    },
  });

  return rows;
}
