import { useStore } from "zustand";
import { createStore } from "zustand/vanilla";
import type { ActivityCounts } from "../protocol/activityData";
import { parseActivityTree } from "../protocol/activityData";
import {
  errorKind,
  errorText,
  friendlyErrorMessage,
  sessionActionError,
  sessionActionHeadline,
} from "../protocol/errors";
import { isActionUnavailable, isThreadNotFound } from "../protocol/sessionErrors";
import { type ActivityFetchResult, activityPanelStore, retainedActivityTree } from "./activityPanel";
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
  // Keep the request's ordering value when a continuation invalidates freshness.
  requestedBump?: number | null;
  // A published root or continuation satisfies this request generation.
  hasPublishedResult?: boolean;
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
  issuePendingRootFetch(ref: string): void;
  publishRootFetch(ref: string, requestID: number, counts: ActivityCounts): void;
  publishContinuationCounts(ref: string, requestID: number, counts: ActivityCounts): void;
  publishContinuationFailure(ref: string, requestID: number): void;
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

// A "hub-unreachable" rejection here is, with the caller-side ready-gate in
// stores/threads.ts (issue #195's RCA), either requireReadyClient's own
// ClientNotReadyError (waited out the bounded timeout - the hub is
// genuinely, not just momentarily, unreachable) or a residual
// CLIENT_UNREACHABLE_PATTERN rejection from a request that raced a state
// change right after the wait. Neither is meaningful as raw text - route it
// through friendlyErrorMessage the same way every other user-facing surface
// does, instead of showing "AppwireClient: cannot call ..." or "... timed
// out after 15000ms" verbatim. An ordinary reconnect never reaches this
// function at all: requireReadyClient makes the caller's promise wait
// rather than reject, so there is nothing to fail on - consistent with
// ConnectionBanner's own "reconnecting is silent, self-healing" design.
function failureFor(err: unknown): { headline: string; detail?: string; sentence: string } {
  if (errorKind(err) === "hub-unreachable") {
    const headline = "Couldn't load activity";
    const detail = friendlyErrorMessage(err);
    return { headline, detail, sentence: `${headline}: ${detail}` };
  }
  const headline = sessionActionHeadline("Couldn't load activity", err);
  const sentence = sessionActionError("Couldn't load activity", err);
  const detail = errorText(err).trim();
  return detail ? { headline, detail, sentence } : { headline, sentence };
}

// A continuation owns the panel's request ID until its page merges. Any root
// fetch started meanwhile replaces that ID, and publishFetch then drops the
// page the reader already asked for - so a refresh waits instead.
function continuationPending(ref: string): boolean {
  return activityPanelStore.getState().entries.get(ref)?.pending?.kind === "continuation";
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
        requestedBump: bump,
        hasPublishedResult: false,
        requestID,
        pendingBump: undefined,
      });
      return { entries };
    });
    return requestID;
  },

  refreshRoot(ref, bump, fetch, onFailure, force = false) {
    const deferred = continuationPending(ref);
    const requestID = deferred ? null : get().beginRootFetch(ref, bump, force);
    if (requestID === null) {
      // Refused because a fetch is in flight, or deferred behind a
      // continuation? Queue this call - with its own fetch/onFailure - for
      // re-issue on completion. Newest bump wins, both against the bump the
      // last root requested and anything already queued (bumps are
      // reducer-side Date.now() stamps, so larger is newer; a null bump
      // carries no ordering claim and yields to a number); force survives
      // whichever record wins. A failed continuation also permits a retry of
      // the same bump. Older non-forced calls queue nothing: reissuing one
      // would regress lastFetchedBump and could replace a good result.
      set((state) => {
        const entry = state.entries.get(ref);
        if (!entry || (!entry.loading && !deferred)) return state;
        const newerThanRequested =
          bump !== null &&
          (entry.requestedBump === null || entry.requestedBump === undefined || bump > entry.requestedBump);
        const retryInvalidated = entry.lastFetchedBump === undefined && bump === entry.requestedBump;
        if (!force && !newerThanRequested && !retryInvalidated) return state;
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
    // unless a continuation now owns the panel. The panel store drains the
    // queue once that continuation settles and merges.
    const issuePendingBump = () => {
      if (continuationPending(ref)) return;
      get().issuePendingRootFetch(ref);
    };
    const ownsPanel = () => activityPanelStore.getState().entries.get(ref)?.requestID === panelRequestID;
    const settleSupersededRoot = () => {
      // The continuation owns freshness: failure permits another root,
      // while success must retain loaded pages when the panel closes.
      get().failRootFetch(ref, requestID);
      issuePendingBump();
    };
    void fetch(ref)
      .then((data) => {
        if (!ownsPanel()) {
          settleSupersededRoot();
          return;
        }
        const parsed = parseActivityTree(data);
        if (parsed === null) {
          get().failRootFetch(ref, requestID);
          activityPanelStore.getState().publishFetch(ref, panelRequestID, { kind: "unsupported" });
          issuePendingBump();
          return;
        }
        activityPanelStore.getState().publishFetch(ref, panelRequestID, { kind: "ready", tree: parsed });
        // A bounded refresh does not re-list the pages already loaded, and the
        // panel keeps them, so the badge counts the tree that is on screen
        // rather than the page as it was sent.
        const published = retainedActivityTree(activityPanelStore.getState().entries.get(ref)) ?? parsed;
        get().publishRootFetch(ref, requestID, published.root.counts);
        issuePendingBump();
      })
      .catch((err) => {
        const currentRequest = get().entries.get(ref)?.requestID === requestID && ownsPanel();
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

  // Re-issues a queued refresh through the queued caller's own fetch/onFailure.
  issuePendingRootFetch(ref) {
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
  },

  publishRootFetch(ref, requestID, counts) {
    set((state) => {
      const entry = state.entries.get(ref);
      if (!entry || entry.requestID !== requestID) return state;
      const entries = new Map(state.entries);
      entries.set(ref, { ...entry, counts, loading: false, hasPublishedResult: true });
      return { entries };
    });
  },

  publishContinuationCounts(ref, requestID, counts) {
    set((state) => {
      const entry = state.entries.get(ref);
      if (!entry || entry.requestID !== requestID) return state;
      const entries = new Map(state.entries);
      entries.set(ref, { ...entry, counts, lastFetchedBump: entry.requestedBump, hasPublishedResult: true });
      return { entries };
    });
  },

  publishContinuationFailure(ref, requestID) {
    set((state) => {
      const entry = state.entries.get(ref);
      if (!entry || entry.requestID !== requestID || entry.hasPublishedResult) return state;
      const entries = new Map(state.entries);
      entries.set(ref, { ...entry, lastFetchedBump: undefined });
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
