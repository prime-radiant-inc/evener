import {
  type ActivityDelegate,
  type ActivityEntry,
  type ActivitySessionNode,
  type ActivityTree,
  isFailedDelegateOutcome,
  isFailedJobOutcome,
} from "./activityData";
import type { SessionActivitySnapshot } from "./sessionActivityStore";
import type {
  SessionActivityContext,
  SessionActivityIssue,
  SessionActivitySummary,
  SessionDelegate,
  SessionWatch,
} from "./types.gen";

export interface SessionActivityPresentation {
  tree: ActivityTree | null;
  context: SessionActivityContext | null;
  summary: SessionActivitySummary | null;
  watches: readonly SessionWatch[];
  complete: boolean;
  pending: boolean;
  issues: readonly SessionActivityIssue[];
}

/** Projects already loaded evidence. Logical session IDs remain absent when
 * only an opaque routing ref is supplied; summary counts stay independent of
 * this tree's loaded-row counts. It owns no reads or response ordering. */
export function projectSessionActivity(snapshot: SessionActivitySnapshot): SessionActivityPresentation {
  const { context, summary, delegates, jobs, watches } = snapshot;
  const issues = [...delegates.issues, ...jobs.issues, ...watches.issues];
  const result: SessionActivityPresentation = {
    tree: null,
    context,
    summary,
    watches: watches.rows,
    complete: delegates.complete && jobs.complete && delegates.issues.length === 0 && jobs.issues.length === 0,
    pending: snapshot.summaryState.pending || delegates.pending || jobs.pending || watches.pending,
    issues,
  };
  if (!context) return result;
  const nodes = new Map<string, ActivitySessionNode>();
  const session = (ref: string): ActivitySessionNode => {
    let node = nodes.get(ref);
    if (!node) {
      node = {
        kind: "session",
        ref,
        label: ref,
        aggregate: "unknown",
        counts: { active: 0, failed: 0, completed: 0, complete: false },
        entries: [],
        branch: {},
      };
      nodes.set(ref, node);
    }
    return node;
  };
  const root = session(context.ref);
  root.sessionId = context.sessionId;
  const projected = new Map<SessionDelegate, ActivityDelegate>();
  for (const row of delegates.rows) projected.set(row, { ...row, branch: {} });
  const byChildRef = new Map<string, SessionDelegate[]>();
  for (const row of delegates.rows) {
    const candidates = byChildRef.get(row.childRef) ?? [];
    candidates.push(row);
    byChildRef.set(row.childRef, candidates);
  }
  const parent = (row: SessionDelegate): SessionDelegate | undefined =>
    byChildRef
      .get(row.ownerRef)
      ?.find(
        (candidate) =>
          candidate.childRef === row.ownerRef &&
          candidate.rootRef === row.rootRef &&
          (!row.parentDelegateId || candidate.delegateId === row.parentDelegateId),
      );
  const cyclic = (row: SessionDelegate): boolean => {
    const seen = new Set<SessionDelegate>([row]);
    let ancestor = parent(row);
    while (ancestor) {
      if (seen.has(ancestor)) return true;
      seen.add(ancestor);
      ancestor = parent(ancestor);
    }
    return false;
  };
  let orphaned = false;
  for (const row of delegates.rows) {
    const delegate = projected.get(row);
    if (!delegate) continue;
    const entry: ActivityEntry = { kind: "delegate", delegate };
    if (cyclic(row)) orphaned = true;
    if (row.ownerRef === context.ref) root.entries.push(entry);
    else if (parent(row) && !cyclic(row)) session(row.ownerRef).entries.push(entry);
    else {
      root.entries.push(entry);
      orphaned = true;
    }
  }
  for (const row of jobs.rows) {
    const owner =
      row.ownerRef === context.ref
        ? root
        : byChildRef.get(row.ownerRef)?.some((candidate) => !cyclic(candidate))
          ? session(row.ownerRef)
          : root;
    if (owner === root && row.ownerRef !== context.ref) orphaned = true;
    if (owner.ref === row.ownerRef) owner.sessionId = row.ownerSessionId;
    owner.entries.push({ kind: "shell", job: { ...row } });
  }
  for (const row of delegates.rows) {
    const child = nodes.get(row.childRef);
    if (child && child !== root && child.entries.length > 0 && !cyclic(row)) {
      const delegate = projected.get(row);
      if (delegate) {
        delegate.child = child;
        if (child.sessionId) delegate.childSessionId = child.sessionId;
      }
    }
  }
  result.complete &&= !orphaned && context.ancestryKnown;
  const finish = (node: ActivitySessionNode): void => {
    for (const entry of node.entries) {
      const row = entry.kind === "shell" ? entry.job : entry.delegate;
      if (row.terminal !== true) node.counts.active += 1;
      else if (entry.kind === "shell" ? isFailedJobOutcome(row.outcome) : isFailedDelegateOutcome(row.outcome))
        node.counts.failed += 1;
      else node.counts.completed += 1;
      if (entry.kind === "delegate" && entry.delegate.child) {
        finish(entry.delegate.child);
        node.counts.active += entry.delegate.child.counts.active;
        node.counts.failed += entry.delegate.child.counts.failed;
        node.counts.completed += entry.delegate.child.counts.completed;
      }
    }
    node.counts.complete = result.complete;
    node.branch = result.complete ? {} : { truncated: true };
    node.aggregate =
      node.counts.active > 0
        ? "working"
        : node.counts.failed > 0
          ? "failed"
          : node.counts.completed > 0
            ? "completed"
            : "unknown";
  };
  finish(root);
  // Rendering revision is deliberately not an epoch or ordering authority.
  result.tree = { revision: 0, root };
  return result;
}
