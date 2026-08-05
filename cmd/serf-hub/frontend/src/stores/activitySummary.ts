import { useStore } from "zustand";
import { createStore } from "zustand/vanilla";
import type { ActivityCounts } from "../panes/session/chrome/activityData";
import { parseActivityTree } from "../panes/session/chrome/activityData";
import { isActionUnavailable, isThreadNotFound } from "../panes/session/chrome/sessionErrors";
import { errorText, sessionActionError, sessionActionHeadline } from "../protocol/errors";
import { type ActivityFetchResult, activityPanelStore } from "./activityPanel";
import { registerPanelStoreEvictor } from "./panelStoreEviction";

export interface ActivitySummaryEntry {
  counts?: ActivityCounts;
  established: boolean;
  mountedBodies: number;
  loading: boolean;
  lastFetchedBump?: number | null;
  requestID: number;
}

export interface ActivitySummaryStoreState {
  entries: Map<string, ActivitySummaryEntry>;
  mountBody(ref: string): void;
  unmountBody(ref: string): void;
  beginRootFetch(ref: string, bump: number | null, force?: boolean): number | null;
  refreshRoot(
    ref: string,
    bump: number | null,
    fetch: (ref: string) => Promise<unknown>,
    onFailure?: (sentence: string) => void,
    force?: boolean,
  ): number | null;
  publishRootFetch(ref: string, requestID: number, counts: ActivityCounts): void;
  failRootFetch(ref: string, requestID: number): void;
  resetForTests(): void;
}

export const EMPTY_ACTIVITY_SUMMARY_ENTRY: ActivitySummaryEntry = {
  counts: undefined,
  established: false,
  mountedBodies: 0,
  loading: false,
  lastFetchedBump: undefined,
  requestID: 0,
};

function entryFor(entries: Map<string, ActivitySummaryEntry>, ref: string): ActivitySummaryEntry {
  return entries.get(ref) ?? { ...EMPTY_ACTIVITY_SUMMARY_ENTRY };
}

// Monotonic across every entry and NOT stored per-entry: an entry recreated
// after eviction must never hand out an ID that a request begun in its
// previous life could still complete with (publish/fail match on requestID).
let nextRequestID = 0;

function failureFor(err: unknown): { headline: string; detail?: string; sentence: string } {
  const headline = sessionActionHeadline("Couldn't load activity", err);
  const sentence = sessionActionError("Couldn't load activity", err);
  const detail = errorText(err).trim();
  return detail ? { headline, detail, sentence } : { headline, sentence };
}

export const activitySummaryStore = createStore<ActivitySummaryStoreState>((set, get) => ({
  entries: new Map(),

  mountBody(ref) {
    set((state) => {
      const entries = new Map(state.entries);
      const entry = entryFor(state.entries, ref);
      entries.set(ref, { ...entry, mountedBodies: entry.mountedBodies + 1 });
      return { entries };
    });
  },

  unmountBody(ref) {
    set((state) => {
      const entry = state.entries.get(ref);
      if (!entry) return state;
      const entries = new Map(state.entries);
      entries.set(ref, { ...entry, mountedBodies: Math.max(0, entry.mountedBodies - 1) });
      return { entries };
    });
  },

  beginRootFetch(ref, bump, force = false) {
    let requestID: number | null = null;
    set((state) => {
      const entry = entryFor(state.entries, ref);
      if (entry.loading || (!force && entry.established && entry.lastFetchedBump === bump)) return state;
      requestID = ++nextRequestID;
      const entries = new Map(state.entries);
      entries.set(ref, {
        ...entry,
        established: true,
        loading: true,
        lastFetchedBump: bump,
        requestID,
      });
      return { entries };
    });
    return requestID;
  },

  refreshRoot(ref, bump, fetch, onFailure, force = false) {
    const requestID = get().beginRootFetch(ref, bump, force);
    if (requestID === null) return null;
    const panelRequestID = activityPanelStore.getState().beginFetch(ref);
    void fetch(ref)
      .then((data) => {
        const parsed = parseActivityTree(data);
        if (parsed === null) {
          get().failRootFetch(ref, requestID);
          activityPanelStore.getState().publishFetch(ref, panelRequestID, { kind: "unsupported" });
          return;
        }
        get().publishRootFetch(ref, requestID, parsed.root.counts);
        activityPanelStore.getState().publishFetch(ref, panelRequestID, { kind: "ready", tree: parsed });
      })
      .catch((err) => {
        const currentRequest = get().entries.get(ref)?.requestID === requestID;
        get().failRootFetch(ref, requestID);
        let result: ActivityFetchResult;
        if (isActionUnavailable(err)) result = { kind: "unsupported" };
        else if (isThreadNotFound(err)) result = { kind: "ended" };
        else {
          const failure = failureFor(err);
          result = { kind: "failed", error: failure };
          if (currentRequest) onFailure?.(failure.sentence);
        }
        activityPanelStore.getState().publishFetch(ref, panelRequestID, result);
      });
    return requestID;
  },

  publishRootFetch(ref, requestID, counts) {
    set((state) => {
      const entry = state.entries.get(ref);
      if (!entry || entry.requestID !== requestID) return state;
      const entries = new Map(state.entries);
      entries.set(ref, { ...entry, counts, loading: false });
      return { entries };
    });
  },

  failRootFetch(ref, requestID) {
    set((state) => {
      const entry = state.entries.get(ref);
      if (!entry || entry.requestID !== requestID) return state;
      const entries = new Map(state.entries);
      entries.set(ref, { ...entry, loading: false });
      return { entries };
    });
  },

  resetForTests() {
    nextRequestID = 0;
    set({ entries: new Map() });
  },
}));

registerPanelStoreEvictor({
  refs: () => activitySummaryStore.getState().entries.keys(),
  evict: (ref) => {
    activitySummaryStore.setState((state) => {
      if (!state.entries.has(ref)) return state;
      const entries = new Map(state.entries);
      entries.delete(ref);
      return { entries };
    });
  },
});

export function useActivitySummaryStore(): ActivitySummaryStoreState;
export function useActivitySummaryStore<T>(selector: (state: ActivitySummaryStoreState) => T): T;
export function useActivitySummaryStore<T>(
  selector?: (state: ActivitySummaryStoreState) => T,
): T | ActivitySummaryStoreState {
  // Both branches call the same zustand hook; the default selector is identity.
  // biome-ignore lint/correctness/useHookAtTopLevel: both arms call useStore
  return selector ? useStore(activitySummaryStore, selector) : useStore(activitySummaryStore);
}

export function resetActivitySummaryStoreForTests(): void {
  activitySummaryStore.getState().resetForTests();
}
