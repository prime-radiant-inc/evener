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
} from "./activityData";
import { isFailedStatus } from "./activityFormat";

export interface ActivityRowBase {
  id: string;
  parentID?: string;
  level: number;
}

export interface ActivityJobRow extends ActivityRowBase {
  kind: "job";
  job: ActivityJob;
  live: boolean;
  transcriptRef?: string;
  parentRef: string;
}

export interface ActivityDelegateRow extends ActivityRowBase {
  kind: "delegate";
  delegate: ActivityDelegate;
  live: boolean;
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

function jobIsActive(job: ActivityJob): boolean {
  return !job.terminal;
}

function sessionIsActive(session: ActivitySessionNode): boolean {
  return session.counts.active > 0 || session.entries.some(entryIsActive);
}

function entryIsActive(entry: ActivityEntry): boolean {
  if (entry.kind === "shell") return jobIsActive(entry.job);
  return (
    entry.delegate.turns.some(jobIsActive) || (entry.delegate.child ? sessionIsActive(entry.delegate.child) : false)
  );
}

function entryIsFailed(entry: ActivityEntry): boolean {
  if (entry.kind === "shell") return isFailedStatus(entry.job.status);
  const delegate: ActivityDelegate = entry.delegate;
  return delegate.turns.some((turn) => isFailedStatus(turn.status)) || (delegate.child?.counts.failed ?? 0) > 0;
}

export function buildActivityRows(tree: ActivityTree, expandedFolds: ReadonlySet<string>): ActivityRow[] {
  const rows: ActivityRow[] = [];

  function appendEntry(entry: ActivityEntry, session: ActivitySessionNode, level: number, parentID: string): void {
    if (entry.kind === "shell") {
      rows.push({
        kind: "job",
        id: activityNodeID(entry),
        parentID,
        level,
        job: entry.job,
        live: jobIsActive(entry.job),
        transcriptRef: entry.job.transcriptRef,
        parentRef: session.ref,
      });
      return;
    }
    const delegate = entry.delegate;
    const id = activityNodeID(entry);
    rows.push({
      kind: "delegate",
      id,
      parentID,
      level,
      delegate,
      live: entryIsActive(entry),
      transcriptRef: delegate.childRef,
      parentRef: session.ref,
    });
    if (delegate.child) visitSession(delegate.child, level + 1, id);
  }

  function visitSession(session: ActivitySessionNode, level: number, parentID?: string): void {
    const entriesParentID = parentID ?? activityNodeID(session);
    const live: ActivityEntry[] = [];
    const inactive: ActivityEntry[] = [];
    for (const entry of session.entries) {
      (entryIsActive(entry) ? live : inactive).push(entry);
    }
    for (const entry of live) appendEntry(entry, session, level, entriesParentID);
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
    for (const entry of inactive) appendEntry(entry, session, level, entriesParentID);
  }

  visitSession(tree.root, 1);
  return rows;
}
