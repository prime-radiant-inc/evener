import {
  type ActivityDelegate,
  type ActivityEntry,
  type ActivitySessionNode,
  type ActivityTree,
  activityNodeID,
} from "./activityData";

function cloneEntry(entry: ActivityEntry): ActivityEntry {
  return entry.kind === "shell"
    ? { kind: "shell", job: { ...entry.job } }
    : { kind: "delegate", delegate: cloneDelegate(entry.delegate) };
}

function cloneSession(session: ActivitySessionNode): ActivitySessionNode {
  return {
    ...session,
    counts: { ...session.counts },
    branch: { ...session.branch },
    entries: session.entries.map(cloneEntry),
  };
}

function cloneDelegate(delegate: ActivityDelegate): ActivityDelegate {
  return {
    ...delegate,
    warnings: delegate.warnings ? [...delegate.warnings] : undefined,
    diagnostics: delegate.diagnostics ? [...delegate.diagnostics] : undefined,
    usage: delegate.usage ? { ...delegate.usage } : undefined,
    turns: delegate.turns?.map((turn) => ({ ...turn })),
    worktree: delegate.worktree ? { ...delegate.worktree } : undefined,
    branch: { ...delegate.branch },
    child: delegate.child ? cloneSession(delegate.child) : undefined,
  };
}

function maxActivity(current: string | undefined, incoming: string | undefined): string | undefined {
  if (!incoming) return current;
  if (!current) return incoming;
  const currentMillis = Date.parse(current);
  const incomingMillis = Date.parse(incoming);
  if (Number.isNaN(incomingMillis)) return current;
  return Number.isNaN(currentMillis) || incomingMillis > currentMillis ? incoming : current;
}

function revisionFencedDelegate(current: ActivityDelegate, patch: ActivityDelegate): ActivityDelegate {
  const state =
    current.type !== "delegate" || patch.type !== "delegate"
      ? cloneDelegate(patch)
      : (patch.projectionRevision ?? 0) > (current.projectionRevision ?? 0)
        ? cloneDelegate(patch)
        : cloneDelegate(current);
  const latestActivityAt = maxActivity(current.latestActivityAt, patch.latestActivityAt);
  if (latestActivityAt !== state.latestActivityAt) state.latestActivityAt = latestActivityAt;
  return state;
}

function mergeDelegate(
  current: ActivityDelegate,
  patch: ActivityDelegate,
  targetID: string,
  inTarget: boolean,
): ActivityDelegate {
  const withinTarget = inTarget || activityNodeID({ kind: "delegate", delegate: current }) === targetID;
  const state = revisionFencedDelegate(current, patch);
  // Turn containers carry no projection revision to fence on. A page cut for a
  // descendant session still ships this container as it stood when the page was
  // cut, so only a page that targets the delegate itself may replace its turns.
  // Turns the client never had are new information, not a regression: keep the
  // patch's when there is nothing on screen to protect.
  if (!withinTarget && current.turns && (current.type !== "delegate" || patch.type !== "delegate")) {
    state.turns = current.turns.map((turn) => ({ ...turn }));
  }
  return {
    ...state,
    branch: withinTarget ? { ...patch.branch } : { ...current.branch },
    child:
      current.child && patch.child && current.child.sessionId === patch.child.sessionId
        ? mergeSession(current.child, patch.child, targetID, withinTarget)
        : patch.child
          ? cloneSession(patch.child)
          : current.child
            ? cloneSession(current.child)
            : undefined,
  };
}

export function fenceRootSession(current: ActivitySessionNode, incoming: ActivitySessionNode): ActivitySessionNode {
  const currentByID = new Map(current.entries.map((entry) => [activityNodeID(entry), entry]));
  const entries = incoming.entries.map((entry): ActivityEntry => {
    if (entry.kind === "shell") return cloneEntry(entry);
    const prior = currentByID.get(activityNodeID(entry));
    if (prior?.kind !== "delegate") return cloneEntry(entry);
    const delegate = revisionFencedDelegate(prior.delegate, entry.delegate);
    delegate.branch = { ...entry.delegate.branch };
    delegate.child =
      prior.delegate.child && entry.delegate.child && prior.delegate.child.sessionId === entry.delegate.child.sessionId
        ? fenceRootSession(prior.delegate.child, entry.delegate.child)
        : entry.delegate.child
          ? cloneSession(entry.delegate.child)
          : undefined;
    return { kind: "delegate", delegate };
  });
  return {
    ...incoming,
    counts: { ...incoming.counts },
    branch: { ...incoming.branch },
    entries,
  };
}

function summarizeSession(session: ActivitySessionNode): ActivitySessionNode {
  const completeBranch = (branch: ActivitySessionNode["branch"]) =>
    !branch.error && !branch.truncated && !branch.continuation;
  const counts = { active: 0, failed: 0, completed: 0, complete: completeBranch(session.branch) };
  const jobOutcomeFailure = (outcome: string | undefined) => outcome === "failure";
  const delegateOutcomeFailure = (outcome: string | undefined) => outcome === "failed" || outcome === "exhausted";
  const add = (terminal: boolean, failed: boolean) => {
    if (!terminal) counts.active++;
    else if (failed) counts.failed++;
    else counts.completed++;
  };
  for (const entry of session.entries) {
    if (entry.kind === "shell") {
      add(entry.job.terminal, jobOutcomeFailure(entry.job.outcome));
      continue;
    }
    const delegate = entry.delegate;
    if (delegate.type === "delegate") add(delegate.terminal === true, delegateOutcomeFailure(delegate.outcome));
    else for (const turn of delegate.turns ?? []) add(turn.terminal, jobOutcomeFailure(turn.outcome));
    if (!completeBranch(delegate.branch)) counts.complete = false;
    if (delegate.child) {
      counts.active += delegate.child.counts.active;
      counts.failed += delegate.child.counts.failed;
      counts.completed += delegate.child.counts.completed;
      if (!delegate.child.counts.complete) counts.complete = false;
    }
  }
  const aggregate =
    counts.active > 0
      ? "working"
      : counts.failed > 0
        ? "failed"
        : !counts.complete
          ? "unavailable"
          : counts.completed > 0
            ? "ended"
            : "idle";
  return { ...session, counts, aggregate };
}

function mergeSession(
  current: ActivitySessionNode,
  patch: ActivitySessionNode,
  targetID: string,
  inTarget: boolean,
): ActivitySessionNode {
  const withinTarget = inTarget || activityNodeID(current) === targetID;
  // A continuation omits entries before its cursor, including at the target session.
  const patchByID = new Map<string, ActivityEntry>();
  for (const entry of patch.entries) patchByID.set(activityNodeID(entry), entry);
  const mergedEntries = current.entries.map((entry) => {
    const id = activityNodeID(entry);
    const patchEntry = patchByID.get(id);
    if (!patchEntry) return cloneEntry(entry);
    if (entry.kind === "delegate" && patchEntry.kind === "delegate") {
      return { kind: "delegate", delegate: mergeDelegate(entry.delegate, patchEntry.delegate, targetID, withinTarget) };
    }
    return cloneEntry(patchEntry);
  }) as ActivityEntry[];
  for (const patchEntry of patch.entries) {
    const id = activityNodeID(patchEntry);
    if (!current.entries.some((entry) => activityNodeID(entry) === id)) mergedEntries.push(cloneEntry(patchEntry));
  }
  // Server summaries cover only the entries in that page. Summarize the graft,
  // including already loaded siblings and the merged descendant summaries.
  return summarizeSession({
    ...current,
    ref: patch.ref,
    label: patch.label,
    branch: withinTarget ? { ...patch.branch } : { ...current.branch },
    entries: mergedEntries,
  });
}

export function graftContinuationTree(current: ActivityTree, targetID: string, patch: ActivityTree): ActivityTree {
  const contains = (session: ActivitySessionNode): boolean =>
    activityNodeID(session) === targetID ||
    session.entries.some(
      (entry) =>
        activityNodeID(entry) === targetID ||
        (entry.kind === "delegate" && !!entry.delegate.child && contains(entry.delegate.child)),
    );
  if (!contains(current.root)) return current;
  const root = mergeSession(current.root, patch.root, targetID, false);
  return {
    revision: Math.max(current.revision, patch.revision),
    root,
  };
}
