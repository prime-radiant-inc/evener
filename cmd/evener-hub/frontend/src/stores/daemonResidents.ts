// daemonResidents.ts — polling store for the Hub's discovered resident-daemon
// inventory. Mirrors the settingsOverview.ts fetch/dedup/current-client style
// but refreshes on demand rather than caching forever (the component's own
// useEffect owns the polling interval; this store starts no permanent timer).
//
// Key design points:
//   • `pending` tracks in-progress actions keyed by identity.generation so
//     individual rows can show a disabled state while retire/forceStop is
//     in flight.
//   • A latestGeneration counter lets the generation-check inside runRefresh
//     discard out-of-order responses when a newer request has already
//     published its result (e.g., a background refresh overtakes a slow one).
//   • On request failure, existing data is kept (not blanked); only `error`
//     is updated. This matches the "retain stale rows" requirement.

import { useStore } from "zustand";
import { createStore } from "zustand/vanilla";
import { errorText } from "../protocol/errors";
import type { DaemonIdentity, DaemonListResponse, DaemonRetireResponse } from "../protocol/types.gen";
import { connectionStore } from "./connection";

export interface DaemonResidentsStoreState {
  /** Latest successful list response; null until first successful load. */
  data: DaemonListResponse | null;
  loading: boolean;
  /** Non-null when the most-recent refresh failed. Stale data is kept. */
  error: string | null;
  /** generation strings of identity rows with an action currently in flight. */
  pending: Set<string>;
  refresh(): Promise<void>;
  retire(identity: DaemonIdentity): Promise<DaemonRetireResponse>;
  forceStop(identity: DaemonIdentity): Promise<void>;
}

// requireClient reads the currently wired client from connectionStore — the
// same pattern as settingsOverview.ts and threads.ts.
function requireClient() {
  const client = connectionStore.getState().client;
  if (!client) {
    throw new Error(
      "daemonResidents store: no client connected; call connectionStore.getState().connect(client) first",
    );
  }
  return client;
}

// Module-private inflight dedup: at most one refresh request runs at a time;
// concurrent callers join the same promise.
let inflight: Promise<void> | null = null;

// latestGeneration increments with every runRefresh invocation so that a
// slow response from an earlier call is silently discarded once a later one
// has already published its result.
let latestGeneration = 0;

async function runRefresh(): Promise<void> {
  const generation = ++latestGeneration;
  daemonResidentsStore.setState({ loading: true, error: null });
  try {
    const client = requireClient();
    const data = await client.request("evener/daemon/list", {});
    // Discard this response if a newer request has already published.
    if (generation === latestGeneration) {
      daemonResidentsStore.setState({ data, loading: false, error: null });
    }
  } catch (err) {
    // Keep existing data (do not blank) — a transient network blip must not
    // wipe an already-populated table back to empty.
    if (generation === latestGeneration) {
      daemonResidentsStore.setState({ loading: false, error: errorText(err) });
    }
  }
}

function ensureInflight(): Promise<void> {
  if (!inflight) {
    inflight = runRefresh().finally(() => {
      inflight = null;
    });
  }
  return inflight;
}

export const daemonResidentsStore = createStore<DaemonResidentsStoreState>(() => ({
  data: null,
  loading: false,
  error: null,
  pending: new Set<string>(),

  async refresh() {
    await ensureInflight();
  },

  async retire(identity: DaemonIdentity): Promise<DaemonRetireResponse> {
    const client = requireClient();
    const key = identity.generation;
    daemonResidentsStore.setState((s) => ({ pending: new Set([...s.pending, key]) }));
    try {
      return await client.request("evener/daemon/retire", { identity });
    } finally {
      daemonResidentsStore.setState((s) => {
        const next = new Set(s.pending);
        next.delete(key);
        return { pending: next };
      });
      // Refresh after retire so the list reflects the updated lifecycle/phase.
      void ensureInflight();
    }
  },

  async forceStop(identity: DaemonIdentity): Promise<void> {
    const client = requireClient();
    const key = identity.generation;
    daemonResidentsStore.setState((s) => ({ pending: new Set([...s.pending, key]) }));
    try {
      await client.request("evener/thread/forceStop", {
        ref: identity.ref,
        expectedDaemon: identity,
      });
    } finally {
      daemonResidentsStore.setState((s) => {
        const next = new Set(s.pending);
        next.delete(key);
        return { pending: next };
      });
      // Refresh after force-stop so the row updates (or disappears).
      void ensureInflight();
    }
  },
}));

export function useDaemonResidentsStore(): DaemonResidentsStoreState;
export function useDaemonResidentsStore<T>(selector: (state: DaemonResidentsStoreState) => T): T;
export function useDaemonResidentsStore<T>(
  selector?: (state: DaemonResidentsStoreState) => T,
): T | DaemonResidentsStoreState {
  // Not a real conditional hook call — see stores/connection.ts's own
  // useConnectionStore for the full explanation (zustand's useStore has a
  // `selector = identity` JS default param, so both arms run identically).
  // biome-ignore lint/correctness/useHookAtTopLevel: same hook both arms, JS default param not a real conditional - see stores/connection.ts
  return selector ? useStore(daemonResidentsStore, selector) : useStore(daemonResidentsStore);
}

// resetDaemonResidentsStoreForTests resets store state and module-private
// tracking to their initial values between tests. Production code must never
// call this. Mirrors resetSettingsOverviewStoreForTests.
export function resetDaemonResidentsStoreForTests(): void {
  inflight = null;
  latestGeneration = 0;
  daemonResidentsStore.setState({
    data: null,
    loading: false,
    error: null,
    pending: new Set<string>(),
  });
}

// _clearDaemonResidentsInflightForTests clears the in-flight promise without
// resetting the latestGeneration counter. Used only for the "newest-client
// generation wins a late response" test, where we need to force a second
// independent request while a prior one is still in flight.
export function _clearDaemonResidentsInflightForTests(): void {
  inflight = null;
}
