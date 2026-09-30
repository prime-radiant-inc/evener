// The settings storage row for the session cache (web session-history cache
// plan, Task 11; spec, "The clear-cached-sessions setting"): the row's state
// machine — empty, cached, cleared, unavailable — plus the Clear action. The
// row keeps its own module-scoped store, mirroring the settingsOverview
// convention, because its state is this row's alone: the count is re-derived
// per render, so no other pane's state can hold it stale.
import { useEffect, useRef } from "react";
import { useStore } from "zustand";
import { createStore } from "zustand/vanilla";
import { clearCachedSessions, countCachedSessions, subscribeCacheWrites } from "../../../stores/threads";
import { Button } from "../../../widgets";
import { SettingsField } from "./settingsField";

export type SessionCacheRowStatus = "empty" | "cached" | "unavailable" | "cleared";

export interface SessionCacheRowState {
  status: SessionCacheRowStatus;
  /** True while the Clear action's transaction is in flight. */
  clearing: boolean;
  refresh: () => Promise<void>;
  clear: () => Promise<void>;
}

// Clear and test resets invalidate in-flight counts by bumping this counter.
// Every async action captures it before its await and
// drops its sets if the counter moved. A completion that lands after a reset
// belongs to a test that already ended - in the wedged-open test the count
// settles only after the test (the adapter's watchdog is the sole failure
// path), and writing its answer then would clobber the next test's state,
// which was the full-suite isolation flake. A count started before Clear also
// cannot overwrite the action's later result.
let storeGeneration = 0;

// sessionCacheRowStore's shape is PINNED by sessionCacheRow.test.tsx, which
// is written against it directly (the settingsOverview.ts convention): the
// four-state status plus clearing and the two actions. Do not change this
// surface without checking that test.
//
// sessionCacheRow's store: the row's state machine. The count runs per
// render through refresh(), so a cleared badge never outlives the next
// render (spec, "The clear-cached-sessions setting").
export const sessionCacheRowStore = createStore<SessionCacheRowState>((set, get) => ({
  // The pre-count state: no count has answered yet, and unknown renders as
  // unavailable — never empty (spec: unavailable is "never shown as empty,
  // so the privacy remedy cannot silently claim to have worked"). The first
  // count replaces it at once: zero records is empty, records are cached.
  status: "unavailable",
  clearing: false,
  refresh: async () => {
    if (get().clearing) return;
    const generation = storeGeneration;
    const count = await countCachedSessions();
    if (generation !== storeGeneration) return; // a reset retired this count: its answer must not write
    if (count === undefined) {
      set({ status: "unavailable" }); // never "empty": the remedy did not run
      return;
    }
    if (count > 0) {
      set({ status: "cached" }); // a refill — this tab's or a sibling's — yields the cleared badge
      return;
    }
    // count === 0: this tab's committed clear still tells the truth, so the
    // badge holds until a refill or a reload (a fresh module state answers
    // "empty"); anything else is the honest empty store.
    set((s) => ({ status: s.status === "cleared" ? "cleared" : "empty" }));
  },
  clear: async () => {
    const generation = ++storeGeneration;
    set({ clearing: true });
    const result = await clearCachedSessions();
    if (generation !== storeGeneration) return; // ditto: the reset restored clearing itself
    set({ status: result.committed ? "cleared" : "unavailable", clearing: false });
  },
}));

export function useSessionCacheRowStore(): SessionCacheRowState;
export function useSessionCacheRowStore<T>(selector: (state: SessionCacheRowState) => T): T;
export function useSessionCacheRowStore<T>(selector?: (state: SessionCacheRowState) => T): T | SessionCacheRowState {
  // Not a real conditional hook call - see stores/connection.ts's own
  // useConnectionStore for the full explanation (zustand's useStore has a
  // `selector = identity` JS default param, so both arms run identically).
  // biome-ignore lint/correctness/useHookAtTopLevel: same hook both arms, JS default param not a real conditional - see stores/connection.ts
  return selector ? useStore(sessionCacheRowStore, selector) : useStore(sessionCacheRowStore);
}

// resetSessionCacheRowStoreForTests returns the store to its initial state -
// sessionCacheRow.tsx is a singleton store shared by every StorageSection
// render, so sessionCacheRow.test.tsx must reset it between tests to keep
// them isolated. No production code should ever call this (mirrors
// settingsOverview.ts's resetSettingsOverviewStoreForTests).
export function resetSessionCacheRowStoreForTests(): void {
  storeGeneration += 1;
  sessionCacheRowStore.setState({ status: "unavailable", clearing: false });
}

/** The storage row's "Cached session content" entry: the cache's state word
 * (never a byte or session estimate - the round-2 cut) plus the Clear
 * action, the privacy remedy that removes the cached content from this
 * browser only. The count runs on EVERY render - the effect has no
 * dependency array - which is the spec's own staleness rule: a cleared badge
 * yields the moment the pane renders the row again and the fresh count finds
 * records, and an unavailable row recovers on the next render or Retry. */
export function SessionCacheRow() {
  const status = useSessionCacheRowStore((s) => s.status);
  const clearing = useSessionCacheRowStore((s) => s.clearing);
  const refresh = useSessionCacheRowStore((s) => s.refresh);
  const clear = useSessionCacheRowStore((s) => s.clear);
  const previousStatus = useRef(status);

  // This tab's next committed record ends the cleared badge even while the
  // row stays mounted and nothing else renders. Failed writes emit nothing.
  useEffect(
    () =>
      subscribeCacheWrites(() => {
        void refresh();
      }),
    [refresh],
  );

  // Recount on mount and unrelated renders, not the render caused by our own
  // count/action result. In particular an uncertain Clear must expose Retry,
  // not immediately auto-retry and replace unavailable with empty.
  useEffect(() => {
    const changed = previousStatus.current !== status;
    previousStatus.current = status;
    if (!clearing && !changed) void refresh();
  });

  return (
    <SettingsField
      label="Cached session content"
      value={
        <>
          {status}
          {status === "unavailable" && (
            <Button size="sm" variant="secondary" onClick={() => void refresh()}>
              Retry
            </Button>
          )}
          <Button
            size="sm"
            variant="secondary"
            disabled={clearing || status === "empty" || status === "unavailable"}
            onClick={() => void clear()}
          >
            Clear
          </Button>
        </>
      }
      help="Copies of this browser's session transcripts kept on disk, so reopening a session paints without a fetch. Clear removes them from this browser only."
    />
  );
}
