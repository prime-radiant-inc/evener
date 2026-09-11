import {
  type ActivityDelegate,
  type ActivityEntry,
  type ActivityJob,
  type ActivitySessionNode,
  type ActivityTree,
  activityNodeID,
  isFailedDelegateOutcome,
  isFailedJobOutcome,
  isTurnContainer,
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

// The backend's own activityBranchComplete: a branch is a complete statement
// only when nothing cut it short.
function completeBranch(branch: ActivitySessionNode["branch"]): boolean {
  return !branch.error && !branch.truncated && !branch.continuation;
}

// Turns by identity: `speaks` is the list whose word is taken, so its object
// wins for every job it carries, and jobs only `rest` knows follow in `rest`'s
// own order. Neither side loses a turn. Which list speaks is the caller's to
// decide - the two callers here answer it differently.
function unionTurns(speaks: ActivityJob[] | undefined, rest: ActivityJob[] | undefined): ActivityJob[] | undefined {
  if (!speaks?.length) return rest?.map((turn) => ({ ...turn }));
  const seen = new Set(speaks.map((turn) => turn.jobId));
  return [...speaks, ...(rest ?? []).filter((turn) => !seen.has(turn.jobId))].map((turn) => ({ ...turn }));
}

function maxActivity(current: string | undefined, incoming: string | undefined): string | undefined {
  if (!incoming) return current;
  if (!current) return incoming;
  const currentMillis = Date.parse(current);
  const incomingMillis = Date.parse(incoming);
  if (Number.isNaN(incomingMillis)) return current;
  return Number.isNaN(currentMillis) || incomingMillis > currentMillis ? incoming : current;
}

// A stable delegate fences on its projection revision, which orders the two
// snapshots. A turn container carries no revision, so nothing orders them and
// authority has to come from elsewhere: patchIsAuthority says whether this
// patch speaks for the container or merely carries it as a by-product. Only a
// continuation page aimed at a descendant is the latter - a root refresh is a
// whole freshly fetched tree, and rounds 1-2 keep one from overlapping a page,
// so whatever it lists is strictly newer than what is on screen. A patch that
// does not speak for the container may still contribute what is purely
// additive: turns the client has never seen, and a later timestamp. Required
// rather than defaulted, so a new caller has to say which it is.
function revisionFencedDelegate(
  current: ActivityDelegate,
  patch: ActivityDelegate,
  patchIsAuthority: boolean,
): ActivityDelegate {
  const turnContainer = isTurnContainer(current) || isTurnContainer(patch);
  const authoritative = turnContainer
    ? patchIsAuthority
    : (patch.projectionRevision ?? 0) > (current.projectionRevision ?? 0);
  const state = cloneDelegate(authoritative ? patch : current);
  if (turnContainer && !patchIsAuthority) state.turns = unionTurns(current.turns, patch.turns);
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
  // A page speaks for this delegate only when it targets it.
  const state = revisionFencedDelegate(current, patch, withinTarget);
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

// fenceSession reports whether anything was retained anywhere beneath it, so a
// session whose descendant kept a loaded page recomputes its own counts too -
// the same bottom-up pass the daemon's recomputeActivitySession makes.
function fenceSession(
  current: ActivitySessionNode,
  incoming: ActivitySessionNode,
): { session: ActivitySessionNode; retained: boolean } {
  const currentByID = new Map(current.entries.map((entry) => [activityNodeID(entry), entry]));
  let retainedBelow = false;
  const entries = incoming.entries.map((entry): ActivityEntry => {
    if (entry.kind === "shell") return cloneEntry(entry);
    const prior = currentByID.get(activityNodeID(entry));
    if (prior?.kind !== "delegate") return cloneEntry(entry);
    // A refresh speaks for every delegate it lists, so its projection wins
    // outright. Coverage only governs what it left out: a bounded branch may
    // have stopped part-way through the turn list, and turns already on screen
    // are not contradicted by a page that never reached them.
    const delegate = revisionFencedDelegate(prior.delegate, entry.delegate, true);
    delegate.branch = { ...entry.delegate.branch };
    if (!completeBranch(entry.delegate.branch)) {
      // A bounded page is a prefix of the turn list as much as of the entry
      // list: it speaks for every turn it reached, so those arrive in the state
      // it gave them, and the turns on screen survive only past where it
      // stopped, appended after it.
      const turns = unionTurns(entry.delegate.turns, prior.delegate.turns);
      // A turn kept past the incoming list is work this session still holds,
      // so it has to be counted, exactly as a kept entry or child is.
      if ((turns?.length ?? 0) > (entry.delegate.turns?.length ?? 0)) retainedBelow = true;
      delegate.turns = turns;
    }
    if (
      prior.delegate.child &&
      entry.delegate.child &&
      prior.delegate.child.sessionId === entry.delegate.child.sessionId
    ) {
      const child = fenceSession(prior.delegate.child, entry.delegate.child);
      delegate.child = child.session;
      retainedBelow = retainedBelow || child.retained;
    } else if (entry.delegate.child) {
      delegate.child = cloneSession(entry.delegate.child);
    } else if (prior.delegate.child && !completeBranch(entry.delegate.branch)) {
      // The same rule on the child axis. Every projectStableActivityDelegate
      // path that returns without a child marks this branch first - the
      // child-unavailable and link-mismatch errors, and the depth cut-off's
      // truncation - so an incomplete branch with no child says the daemon
      // could not render the subtree, not that there is none to render.
      delegate.child = cloneSession(prior.delegate.child);
      retainedBelow = true;
    } else {
      delegate.child = undefined;
    }
    return { kind: "delegate", delegate };
  });
  // A bounded page is a prefix of this session's entry order (projectActivitySessionAt
  // renders owned shell jobs, then sorted stable delegates, and a continuation
  // resumes at the exact index it stopped on), so it makes no claim at all about
  // what lies past its cutoff: entries loaded from later pages stay. A complete
  // page is the whole statement, and what it leaves out is genuinely gone - that
  // is how an evicted delegate reaches the screen.
  const incomingIDs = new Set(incoming.entries.map(activityNodeID));
  const kept = completeBranch(incoming.branch)
    ? []
    : current.entries.filter((entry) => !incomingIDs.has(activityNodeID(entry))).map(cloneEntry);
  const session = {
    ...incoming,
    counts: { ...incoming.counts },
    branch: { ...incoming.branch },
    entries: [...entries, ...kept],
  };
  const retained = kept.length > 0 || retainedBelow;
  // The server's counts describe the page it sent. Anything kept beyond it is
  // ours to account for, the same way a grafted page is.
  return { session: retained ? summarizeSession(session) : session, retained };
}

export function fenceRootSession(current: ActivitySessionNode, incoming: ActivitySessionNode): ActivitySessionNode {
  return fenceSession(current, incoming).session;
}

function summarizeSession(session: ActivitySessionNode): ActivitySessionNode {
  const counts = { active: 0, failed: 0, completed: 0, complete: completeBranch(session.branch) };
  const add = (terminal: boolean, failed: boolean) => {
    if (!terminal) counts.active++;
    else if (failed) counts.failed++;
    else counts.completed++;
  };
  for (const entry of session.entries) {
    if (entry.kind === "shell") {
      add(entry.job.terminal, isFailedJobOutcome(entry.job.outcome));
      continue;
    }
    const delegate = entry.delegate;
    if (!isTurnContainer(delegate)) add(delegate.terminal === true, isFailedDelegateOutcome(delegate.outcome));
    else for (const turn of delegate.turns ?? []) add(turn.terminal, isFailedJobOutcome(turn.outcome));
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
