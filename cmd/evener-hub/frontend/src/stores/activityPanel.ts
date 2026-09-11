import { fenceRootSession, graftContinuationTree } from "../protocol/activityMerge";

export { graftContinuationTree } from "../protocol/activityMerge";

import { useStore } from "zustand";
import { createStore } from "zustand/vanilla";
import {
  type ActivityDisclosureState,
  type ActivityTree,
  defaultExpandedIDs,
  reconcileActivityState,
} from "../protocol/activityData";
import { activitySummaryStore } from "./activitySummary";
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
  // summaryRequestID is absent when no summary entry existed as the page
  // began: there is no generation to fence freshness against, and 0 would be
  // a sentinel that a freshly mounted entry could match by accident.
  pending?: { kind: "root" } | { kind: "continuation"; nodeID: string; summaryRequestID?: number };
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
  beginContinuationFetch(ref: string, nodeID: string): number | null;
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

export const activityPanelStore = createStore<ActivityPanelStoreState>((set, get) => ({
  entries: new Map(),

  beginFetch(ref, continuation) {
    let requestID = 0;
    set((state) => {
      const current = entryFor(state.entries, ref);
      requestID = ++nextRequestID;
      const tree = retainedTree(current.load);
      const summaryRequestID = activitySummaryStore.getState().entries.get(ref)?.requestID;
      const next: ActivityPanelEntry = continuation
        ? {
            ...current,
            continuationLoadingID: continuation.nodeID,
            continuationFailures: { ...current.continuationFailures, [continuation.nodeID]: undefined },
            requestID,
            pending: { kind: "continuation", nodeID: continuation.nodeID, summaryRequestID },
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

  // The caller-facing way to start a page, and the mirror of activitySummary's
  // continuationPending guard: one activity request per ref, whichever started
  // first. An entry holds a single pending request, so a page started now would
  // take the panel's request ID and whatever is already out would be dropped on
  // arrival - the root's own snapshot, with this page's counts then marking its
  // bump fresh against a tree that never received it, or another branch's page,
  // discarded without even a failure to show for it. Null means the caller must
  // not issue the request: a refreshed tree arrives with its own token, and a
  // branch whose turn has not come keeps the one it already has.
  beginContinuationFetch(ref, nodeID) {
    if (get().entries.get(ref)?.pending) return null;
    return get().beginFetch(ref, { nodeID });
  },

  publishFetch(ref, requestID, result) {
    let settledContinuation = false;
    set((state) => {
      const current = state.entries.get(ref);
      if (!current || current.requestID !== requestID) return state;
      const pending = current.pending;
      if (!pending) return state;
      let next = current;

      if (pending.kind === "continuation") {
        settledContinuation = true;
        const summaryRequestID = pending.summaryRequestID;
        if (result.kind !== "ready" && summaryRequestID !== undefined)
          activitySummaryStore.getState().publishContinuationFailure(ref, summaryRequestID);
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
            if (summaryRequestID !== undefined)
              activitySummaryStore.getState().publishContinuationCounts(ref, summaryRequestID, tree.root.counts);
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
    // A root refresh queued while this continuation was in flight waited for
    // the merge above rather than replacing the panel tree mid-page.
    if (settledContinuation) activitySummaryStore.getState().issuePendingRootFetch(ref);
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
