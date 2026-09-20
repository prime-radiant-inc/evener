import {
  type ActivityCounts,
  type ActivityDisclosureState,
  type ActivityTree,
  defaultExpandedIDs,
  fenceRootSession,
  graftContinuationTree,
  type PanelLoadFailure,
  reconcileActivityState,
} from "@evener/appwire-client";
import { useStore } from "zustand";
import { createStore } from "zustand/vanilla";
import { registerPanelStoreEvictor } from "./panelStoreEviction";

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
  | { kind: "continuation-failed"; nodeID: string; message: string }
  // A continuation page minted against a different revision than the retained
  // tree. It is not a failure to show the reader: the page is discarded and the
  // consumer re-fetches a fresh root, so no continuation failure is recorded.
  | { kind: "continuation-discarded"; nodeID: string };

/** What a settled continuation page owes the summary store: the summary
 * generation it began under (absent when no summary entry existed then) and
 * the badge's due - the merged tree's counts, or a failure. No debt when the
 * page landed as a fresh root, since there was no tree to merge into. */
export interface ContinuationSettlement {
  summaryRequestID?: number;
  debt?: { kind: "failure" } | { kind: "counts"; counts: ActivityCounts };
}

/** The summary store's side of the seam. activitySummary.ts imports this
 * module and registers the link at load; the panel never imports back. */
export interface ActivitySummaryLink {
  summaryGeneration(ref: string): number | undefined;
  onContinuationSettled(ref: string, settlement: ContinuationSettlement): void;
}

let summaryLink: ActivitySummaryLink | undefined;

/** Returns the unlink. Production registers once for the app's lifetime and
 * drops it; a test that needs the unregistered state calls it. */
export function linkActivitySummary(link: ActivitySummaryLink): () => void {
  summaryLink = link;
  return () => {
    if (summaryLink === link) summaryLink = undefined;
  };
}

// A continuation page cannot run unlinked: it would record an undefined
// generation and its settlement would reach nobody, leaving the badge unpaid
// and a queued root refresh asleep. That is a wiring mistake, so it says so.
function requireSummaryLink(): ActivitySummaryLink {
  if (!summaryLink) {
    throw new Error("activityPanel store: no summary link registered; call initActivitySummary() first");
  }
  return summaryLink;
}

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
    // Read before the updater runs, so an unlinked continuation throws with
    // the entry untouched rather than half-begun.
    const summaryRequestID = continuation ? requireSummaryLink().summaryGeneration(ref) : undefined;
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
    // A continuation page settles with the summary store, so its link is
    // resolved before the updater commits: an unregistered link throws with
    // the page still pending rather than after the entry has been cleared.
    // The same predicate decides the settlement inside the updater.
    const settling = get().entries.get(ref);
    const link =
      settling?.requestID === requestID && settling.pending?.kind === "continuation" ? requireSummaryLink() : undefined;
    // What this page owes the summary store is decided inside the updater and
    // reported after it, so the updater stays a pure function of panel state.
    let settlement: ContinuationSettlement | undefined;
    set((state) => {
      const current = state.entries.get(ref);
      if (!current || current.requestID !== requestID) return state;
      const pending = current.pending;
      if (!pending) return state;
      let next = current;

      if (pending.kind === "continuation") {
        let debt: ContinuationSettlement["debt"];
        if (result.kind === "continuation-failed") {
          debt = { kind: "failure" };
          next = {
            ...current,
            continuationLoadingID: undefined,
            continuationFailures: { ...current.continuationFailures, [result.nodeID]: result.message },
            pending: undefined,
          };
        } else if (result.kind === "continuation-discarded") {
          // The page belongs to another revision: leave the retained tree and
          // the badge alone (no debt) and let the caller's fresh root fetch
          // re-anchor pagination. Clearing any prior failure for this node keeps
          // the discard from reading as a branch error.
          const continuationFailures = { ...current.continuationFailures };
          delete continuationFailures[result.nodeID];
          next = {
            ...current,
            continuationLoadingID: undefined,
            continuationFailures,
            pending: undefined,
          };
        } else if (result.kind === "ready") {
          const previousTree = retainedTree(current.load);
          if (previousTree && result.tree.revision !== previousTree.revision) {
            // A page from another revision is not graftable. graftContinuationTree
            // would leave the retained tree unchanged, so recording this as a
            // merge would claim the unchanged tree's counts as the page's result
            // and clear any prior failure. Discard it exactly like an explicit
            // continuation-discarded result instead; the caller owns the root
            // refetch (ActivityPanel starts one before publishing this).
            const continuationFailures = { ...current.continuationFailures };
            delete continuationFailures[pending.nodeID];
            next = {
              ...current,
              continuationLoadingID: undefined,
              continuationFailures,
              pending: undefined,
            };
          } else if (previousTree) {
            const tree = graftContinuationTree(previousTree, pending.nodeID, result.tree);
            debt = { kind: "counts", counts: tree.root.counts };
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
            // The page landed as a fresh root: no tree to merge into, so no debt.
            next = readyRoot(current, result.tree);
          }
        } else if (result.kind === "failed") {
          debt = { kind: "failure" };
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
          debt = { kind: "failure" };
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
        settlement = { summaryRequestID: pending.summaryRequestID, debt };
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
    if (settlement && link) link.onContinuationSettled(ref, settlement);
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
    summaryLink = undefined;
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
