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
  const currentRevision = current.projectionRevision ?? 0;
  const patchRevision = patch.projectionRevision ?? 0;
  const state = patchRevision > currentRevision ? cloneDelegate(patch) : cloneDelegate(current);
  const latestActivityAt = maxActivity(current.latestActivityAt, patch.latestActivityAt);
  if (latestActivityAt !== state.latestActivityAt) state.latestActivityAt = latestActivityAt;
  return state;
}

function mergeDelegate(current: ActivityDelegate, patch: ActivityDelegate): ActivityDelegate {
  const state = revisionFencedDelegate(current, patch);
  return {
    ...state,
    branch: { ...patch.branch },
    child:
      current.child && patch.child && current.child.sessionId === patch.child.sessionId
        ? mergeSession(current.child, patch.child)
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

function mergeSession(current: ActivitySessionNode, patch: ActivitySessionNode): ActivitySessionNode {
  // A continuation omits entries before its cursor, including at the target session.
  const patchByID = new Map<string, ActivityEntry>();
  for (const entry of patch.entries) patchByID.set(activityNodeID(entry), entry);
  const mergedEntries = current.entries.map((entry) => {
    const id = activityNodeID(entry);
    const patchEntry = patchByID.get(id);
    if (!patchEntry) return cloneEntry(entry);
    if (entry.kind === "delegate" && patchEntry.kind === "delegate") {
      return { kind: "delegate", delegate: mergeDelegate(entry.delegate, patchEntry.delegate) };
    }
    return cloneEntry(patchEntry);
  }) as ActivityEntry[];
  for (const patchEntry of patch.entries) {
    const id = activityNodeID(patchEntry);
    if (!current.entries.some((entry) => activityNodeID(entry) === id)) mergedEntries.push(cloneEntry(patchEntry));
  }
  return {
    ...current,
    ref: patch.ref,
    label: patch.label,
    aggregate: patch.aggregate,
    counts: { ...patch.counts },
    branch: { ...patch.branch },
    entries: mergedEntries,
  };
}

export function graftContinuationTree(current: ActivityTree, patch: ActivityTree): ActivityTree {
  const root = mergeSession(current.root, patch.root);
  // A continuation response describes one retained branch and can carry counts
  // for that partial window. The root counts are the badge's authoritative
  // summary, so a continuation must never replace them.
  return {
    revision: Math.max(current.revision, patch.revision),
    root: {
      ...root,
      aggregate: current.root.aggregate,
      counts: { ...current.root.counts },
    },
  };
}
