import { useStore } from "zustand";
import { createStore } from "zustand/vanilla";
import type { ActivityCounts } from "../panes/session/chrome/activityData";
import { parseActivityTree } from "../panes/session/chrome/activityData";
import { isActionUnavailable, isThreadNotFound } from "../panes/session/chrome/sessionErrors";
import { errorText, sessionActionError, sessionActionHeadline } from "../protocol/errors";
import { type ActivityFetchResult, activityPanelStore } from "./activityPanel";
import { registerPanelStoreEvictor } from "./panelStoreEviction";

// A refresh that arrived while a root fetch was in flight. Nothing re-runs
// the component effects when `loading` clears, so refreshRoot re-issues this
// once the active request completes - with the queued caller's own fetch and
// onFailure, since the superseded caller's failure guard (mount generation)
// may no longer describe who is on screen.
interface PendingRootFetch {
  bump: number | null;
  force: boolean;
  fetch: (ref: string) => Promise<unknown>;
  onFailure?: (sentence: string) => void;
}

export interface ActivitySummaryEntry {
  counts?: ActivityCounts;
  established: boolean;
  mountedBodies: number;
  loading: boolean;
  lastFetchedBump?: number | null;
  requestID: number;
  pendingBump?: PendingRootFetch;
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
        pendingBump: undefined,
      });
      return { entries };
    });
    return requestID;
  },

  refreshRoot(ref, bump, fetch, onFailure, force = false) {
    const requestID = get().beginRootFetch(ref, bump, force);
    if (requestID === null) {
      // Refused because a fetch is in flight? Queue this call - with its own
      // fetch/onFailure - for re-issue on completion. Newest bump wins, both
      // against the IN-FLIGHT bump and anything already queued (bumps are
      // reducer-side Date.now() stamps, so larger is newer; a null bump
      // carries no ordering claim and yields to a number); force survives
      // whichever record wins. A non-forced call that is not provably newer
      // than the fetch already running queues nothing - reissuing an older
      // bump would regress lastFetchedBump and could replace a good result.
      set((state) => {
        const entry = state.entries.get(ref);
        if (!entry?.loading) return state;
        const newerThanInFlight =
          bump !== null &&
          (entry.lastFetchedBump === null || entry.lastFetchedBump === undefined || bump > entry.lastFetchedBump);
        if (!force && !newerThanInFlight) return state;
        const incoming: PendingRootFetch = { bump, force, fetch, onFailure };
        const previous = entry.pendingBump;
        const incomingWins = !previous || previous.bump === null || (bump !== null && bump >= previous.bump);
        const winner = incomingWins ? incoming : previous;
        const pendingBump = { ...winner, force: force || (previous?.force ?? false) };
        const entries = new Map(state.entries);
        entries.set(ref, { ...entry, pendingBump });
        return { entries };
      });
      return null;
    }
    const panelRequestID = activityPanelStore.getState().beginFetch(ref);
    // Re-issues whatever refresh was queued while this request was in flight,
    // through the queued caller's own fetch/onFailure.
    const issuePendingBump = () => {
      let pending: PendingRootFetch | undefined;
      set((state) => {
        const entry = state.entries.get(ref);
        if (!entry?.pendingBump) return state;
        pending = entry.pendingBump;
        const entries = new Map(state.entries);
        entries.set(ref, { ...entry, pendingBump: undefined });
        return { entries };
      });
      if (pending) get().refreshRoot(ref, pending.bump, pending.fetch, pending.onFailure, pending.force);
    };
    void fetch(ref)
      .then((data) => {
        const parsed = parseActivityTree(data);
        if (parsed === null) {
          get().failRootFetch(ref, requestID);
          activityPanelStore.getState().publishFetch(ref, panelRequestID, { kind: "unsupported" });
          issuePendingBump();
          return;
        }
        get().publishRootFetch(ref, requestID, parsed.root.counts);
        activityPanelStore.getState().publishFetch(ref, panelRequestID, { kind: "ready", tree: parsed });
        issuePendingBump();
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
        issuePendingBump();
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
