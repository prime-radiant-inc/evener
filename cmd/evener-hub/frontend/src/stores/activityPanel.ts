import { useStore } from "zustand";
import { createStore } from "zustand/vanilla";
import {
  type ActivityDelegate,
  type ActivityDisclosureState,
  type ActivityEntry,
  type ActivitySessionNode,
  type ActivityTree,
  activityNodeID,
  defaultExpandedIDs,
  reconcileActivityState,
} from "../panes/session/chrome/activityData";
import { registerPanelStoreEvictor } from "./panelStoreEviction";
import type { PanelLoadFailure } from "./tasksPanel";

export type ActivityLoadState =
  | { kind: "idle" }
  | { kind: "loading" }
  | { kind: "ready"; tree: ActivityTree; staleError?: PanelLoadFailure }
  | { kind: "unsupported" }
  | { kind: "failed"; error: PanelLoadFailure }
  | { kind: "ended"; tree?: ActivityTree };

export interface ActivityPanelEntry {
  load: ActivityLoadState;
  disclosure: ActivityDisclosureState;
  established: boolean;
  continuationLoadingID?: string;
  continuationFailures: Record<string, string | undefined>;
  requestID: number;
  pending?: { kind: "root" } | { kind: "continuation"; nodeID: string };
  expandedFoldIDs: string[];
}

export type ActivityFetchResult =
  | { kind: "ready"; tree: ActivityTree }
  | { kind: "unsupported" }
  | { kind: "ended" }
  | { kind: "failed"; error: PanelLoadFailure }
  | { kind: "continuation-failed"; nodeID: string; message: string };

export interface ActivityPanelStoreState {
  entries: Map<string, ActivityPanelEntry>;
  beginFetch(ref: string, continuation?: { nodeID: string }): number;
  publishFetch(ref: string, requestID: number, result: ActivityFetchResult): void;
  setExpanded(ref: string, expandedIDs: string[]): void;
  setSelected(ref: string, selectedID?: string): void;
  toggleFold(ref: string, foldID: string): void;
  resetForTests(): void;
}

const INITIAL_DISCLOSURE: ActivityDisclosureState = {
  expandedIDs: [],
  selectedID: undefined,
  selectionPruned: false,
};

export const EMPTY_ACTIVITY_PANEL_ENTRY: ActivityPanelEntry = {
  load: { kind: "idle" },
  disclosure: { expandedIDs: [], selectedID: undefined, selectionPruned: false },
  established: false,
  continuationFailures: {},
  requestID: 0,
  expandedFoldIDs: [],
};

const INITIAL_LOAD: ActivityLoadState = { kind: "idle" };

function newEntry(): ActivityPanelEntry {
  return {
    load: INITIAL_LOAD,
    disclosure: { ...INITIAL_DISCLOSURE },
    established: false,
    continuationFailures: {},
    requestID: 0,
    expandedFoldIDs: [],
  };
}

function entryFor(entries: Map<string, ActivityPanelEntry>, ref: string): ActivityPanelEntry {
  return entries.get(ref) ?? newEntry();
}

// Monotonic across every entry and NOT stored per-entry: an entry recreated
// after eviction must never hand out an ID that a request begun in its
// previous life could still complete with (publishFetch matches on requestID).
let nextRequestID = 0;

function retainedTree(load: ActivityLoadState): ActivityTree | undefined {
  if (load.kind === "ready") return load.tree;
  if (load.kind === "ended") return load.tree;
  return undefined;
}

export function retainedActivityTree(entry: ActivityPanelEntry | undefined): ActivityTree | undefined {
  return entry ? retainedTree(entry.load) : undefined;
}

function initialDisclosure(tree: ActivityTree): ActivityDisclosureState {
  return {
    expandedIDs: defaultExpandedIDs(tree),
    selectedID: undefined,
    selectionPruned: false,
    tree,
  };
}

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

function mergeDelegate(current: ActivityDelegate, patch: ActivityDelegate, targetID: string): ActivityDelegate {
  const delegateID = activityNodeID({ kind: "delegate", delegateId: current.delegateId });
  const state = revisionFencedDelegate(current, patch);
  if (delegateID === targetID) return state;
  return {
    ...state,
    branch: { ...patch.branch },
    child:
      current.child && patch.child && current.child.sessionId === patch.child.sessionId
        ? mergeSession(current.child, patch.child, targetID)
        : patch.child
          ? cloneSession(patch.child)
          : current.child
            ? cloneSession(current.child)
            : undefined,
  };
}

function fenceRootSession(current: ActivitySessionNode, incoming: ActivitySessionNode): ActivitySessionNode {
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

function mergeSession(current: ActivitySessionNode, patch: ActivitySessionNode, targetID: string): ActivitySessionNode {
  if (activityNodeID(current) === targetID) return cloneSession(patch);
  const patchByID = new Map<string, ActivityEntry>();
  for (const entry of patch.entries) patchByID.set(activityNodeID(entry), entry);
  const mergedEntries = current.entries.map((entry) => {
    const id = activityNodeID(entry);
    const patchEntry = patchByID.get(id);
    if (!patchEntry) return cloneEntry(entry);
    if (entry.kind === "delegate" && patchEntry.kind === "delegate") {
      return { kind: "delegate", delegate: mergeDelegate(entry.delegate, patchEntry.delegate, targetID) };
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

export function graftContinuationTree(current: ActivityTree, targetID: string, patch: ActivityTree): ActivityTree {
  const root = mergeSession(current.root, patch.root, targetID);
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

function updateEntry(
  set: (update: (state: ActivityPanelStoreState) => Partial<ActivityPanelStoreState>) => void,
  ref: string,
  update: (entry: ActivityPanelEntry) => ActivityPanelEntry,
): void {
  set((state) => {
    const entries = new Map(state.entries);
    entries.set(ref, update(entryFor(state.entries, ref)));
    return { entries };
  });
}

export const activityPanelStore = createStore<ActivityPanelStoreState>((set) => ({
  entries: new Map(),

  beginFetch(ref, continuation) {
    let requestID = 0;
    set((state) => {
      const current = entryFor(state.entries, ref);
      requestID = ++nextRequestID;
      const tree = retainedTree(current.load);
      const next: ActivityPanelEntry = continuation
        ? {
            ...current,
            continuationLoadingID: continuation.nodeID,
            continuationFailures: { ...current.continuationFailures, [continuation.nodeID]: undefined },
            requestID,
            pending: { kind: "continuation", nodeID: continuation.nodeID },
          }
        : {
            ...current,
            load: tree ? { kind: "ready", tree } : { kind: "loading" },
            continuationLoadingID: undefined,
            established: true,
            requestID,
            pending: { kind: "root" },
          };
      const entries = new Map(state.entries);
      entries.set(ref, next);
      return { entries };
    });
    return requestID;
  },

  publishFetch(ref, requestID, result) {
    set((state) => {
      const current = state.entries.get(ref);
      if (!current || current.requestID !== requestID) return state;
      const pending = current.pending;
      if (!pending) return state;
      let next = current;

      if (pending.kind === "continuation") {
        if (result.kind === "continuation-failed") {
          next = {
            ...current,
            continuationLoadingID: undefined,
            continuationFailures: { ...current.continuationFailures, [result.nodeID]: result.message },
            pending: undefined,
          };
        } else if (result.kind === "ready") {
          const previousTree = retainedTree(current.load);
          if (previousTree) {
            const tree = graftContinuationTree(previousTree, pending.nodeID, result.tree);
            const disclosure = reconcileActivityState({ ...current.disclosure, tree: previousTree }, tree);
            const continuationFailures = { ...current.continuationFailures };
            delete continuationFailures[pending.nodeID];
            next = {
              ...current,
              load: { kind: "ready", tree },
              disclosure: { ...disclosure, tree },
              continuationLoadingID: undefined,
              continuationFailures,
              pending: undefined,
            };
          } else {
            next = readyRoot(current, result.tree);
          }
        } else if (result.kind === "failed") {
          next = {
            ...current,
            continuationLoadingID: undefined,
            continuationFailures: {
              ...current.continuationFailures,
              [pending.nodeID]: result.error.sentence,
            },
            pending: undefined,
          };
        } else {
          next = {
            ...current,
            continuationLoadingID: undefined,
            continuationFailures: {
              ...current.continuationFailures,
              [pending.nodeID]: "Couldn't load more retained activity for this branch.",
            },
            pending: undefined,
          };
        }
      } else {
        switch (result.kind) {
          case "ready":
            next = readyRoot(current, result.tree);
            break;
          case "unsupported":
            next = {
              ...current,
              load: { kind: "unsupported" },
              continuationLoadingID: undefined,
              continuationFailures: {},
              pending: undefined,
            };
            break;
          case "ended":
            next = {
              ...current,
              load: { kind: "ended", tree: retainedTree(current.load) },
              continuationLoadingID: undefined,
              pending: undefined,
            };
            break;
          case "failed": {
            const tree = retainedTree(current.load);
            next = {
              ...current,
              load: tree ? { kind: "ready", tree, staleError: result.error } : { kind: "failed", error: result.error },
              continuationLoadingID: undefined,
              continuationFailures: tree ? current.continuationFailures : {},
              pending: undefined,
            };
            break;
          }
          case "continuation-failed":
            next = {
              ...current,
              load: retainedTree(current.load)
                ? { kind: "ready", tree: retainedTree(current.load) as ActivityTree }
                : current.load,
              continuationLoadingID: undefined,
              continuationFailures: { ...current.continuationFailures, [result.nodeID]: result.message },
              pending: undefined,
            };
            break;
        }
      }

      const entries = new Map(state.entries);
      entries.set(ref, next);
      return { entries };
    });
  },

  setExpanded(ref, expandedIDs) {
    updateEntry(set, ref, (entry) => ({
      ...entry,
      disclosure: { ...entry.disclosure, expandedIDs, selectionPruned: false },
    }));
  },

  setSelected(ref, selectedID) {
    updateEntry(set, ref, (entry) => ({
      ...entry,
      disclosure: { ...entry.disclosure, selectedID, selectionPruned: false },
    }));
  },

  toggleFold(ref, foldID) {
    updateEntry(set, ref, (entry) => ({
      ...entry,
      expandedFoldIDs: entry.expandedFoldIDs.includes(foldID)
        ? entry.expandedFoldIDs.filter((id) => id !== foldID)
        : [...entry.expandedFoldIDs, foldID],
    }));
  },

  resetForTests() {
    nextRequestID = 0;
    set({ entries: new Map() });
  },
}));

function readyRoot(current: ActivityPanelEntry, tree: ActivityTree): ActivityPanelEntry {
  const previousTree = retainedTree(current.load);
  const fencedTree = previousTree ? { ...tree, root: fenceRootSession(previousTree.root, tree.root) } : tree;
  const disclosure = previousTree
    ? reconcileActivityState({ ...current.disclosure, tree: previousTree }, fencedTree)
    : initialDisclosure(fencedTree);
  return {
    ...current,
    load: { kind: "ready", tree: fencedTree },
    disclosure: { ...disclosure, tree: fencedTree },
    established: true,
    continuationLoadingID: undefined,
    pending: undefined,
  };
}

registerPanelStoreEvictor({
  refs: () => activityPanelStore.getState().entries.keys(),
  evict: (ref) => {
    activityPanelStore.setState((state) => {
      if (!state.entries.has(ref)) return state;
      const entries = new Map(state.entries);
      entries.delete(ref);
      return { entries };
    });
  },
});

export function useActivityPanelStore(): ActivityPanelStoreState;
export function useActivityPanelStore<T>(selector: (state: ActivityPanelStoreState) => T): T;
export function useActivityPanelStore<T>(
  selector?: (state: ActivityPanelStoreState) => T,
): T | ActivityPanelStoreState {
  // Both branches call the same zustand hook; the default selector is identity.
  // biome-ignore lint/correctness/useHookAtTopLevel: both arms call useStore
  return selector ? useStore(activityPanelStore, selector) : useStore(activityPanelStore);
}

export function resetActivityPanelStoreForTests(): void {
  activityPanelStore.getState().resetForTests();
}
