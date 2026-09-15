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

// Walks every loaded entry, ignoring fold/disclosure state, so an entity
// resolves identically whether or not its fold is expanded.
export function indexActivityEntities(tree: ActivityTree): Map<string, ActivityJobRow | ActivityDelegateRow> {
  const index = new Map<string, ActivityJobRow | ActivityDelegateRow>();

  function visitSession(session: ActivitySessionNode, parentRef: string, level: number, parentID?: string): void {
    const entriesParentID = parentID ?? activityNodeID(session);
    for (const entry of session.entries) {
      // Mirror the panel's fold origin so an indexed row matches that entry
      // when disclosed, without making index membership disclosure-dependent.
      const panelWouldFoldEntry = !entryIsActive(entry);
      const row = activityEntryRow(entry, parentRef, level, entriesParentID, panelWouldFoldEntry);
      if (row.kind === "job") {
        index.set(row.job.jobId, row);
        continue;
      }
      index.set(row.delegate.delegateId, row);
      if (row.delegate.child) visitSession(row.delegate.child, row.transcriptRef, level + 1, row.id);
    }
  }

  visitSession(tree.root, tree.root.ref, 1);
  return index;
}

export function buildActivityRows(tree: ActivityTree, expandedFolds: ReadonlySet<string>): ActivityRow[] {
  const rows: ActivityRow[] = [];

  function appendEntry(
    entry: ActivityEntry,
    session: ActivitySessionNode,
    level: number,
    parentID: string,
    fromFold: boolean,
  ): void {
    const row = activityEntryRow(entry, session.ref, level, parentID, fromFold);
    rows.push(row);
    if (entry.kind === "delegate" && entry.delegate.child) {
      visitSession(entry.delegate.child, level + 1, row.id);
    }
  }

  function visitSession(session: ActivitySessionNode, level: number, parentID?: string): void {
    const entriesParentID = parentID ?? activityNodeID(session);
    const live: ActivityEntry[] = [];
    const inactive: ActivityEntry[] = [];
    for (const entry of session.entries) {
      (entryIsActive(entry) ? live : inactive).push(entry);
    }
    for (const entry of live) appendEntry(entry, session, level, entriesParentID, false);
    if (inactive.length === 0) return;
    const id = foldRowID(activityNodeID(session));
    rows.push({
      kind: "fold",
      id,
      parentID: entriesParentID,
      level,
      foldParentID: activityNodeID(session),
      inactiveCount: inactive.length,
      failedCount: inactive.filter(entryIsFailed).length,
    });
    if (!expandedFolds.has(id)) return;
    for (const entry of inactive) appendEntry(entry, session, level, entriesParentID, true);
  }

  visitSession(tree.root, 1);
  return rows;
}
