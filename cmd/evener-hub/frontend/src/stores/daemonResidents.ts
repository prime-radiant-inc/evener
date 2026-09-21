// daemonResidents.ts — polling store for the Hub's discovered resident-daemon
// inventory. Mirrors the settingsOverview.ts fetch/dedup/current-client style
// but refreshes on demand rather than caching forever (the component's own
// useEffect owns the polling interval; this store starts no permanent timer).
//
// Key design points:
//   • `pending` tracks in-progress actions keyed by residentRowKey — the acting
//     row's generation, falling back to ref — so individual rows can show a
//     disabled state while retire/forceStop is in flight without disabling a
//     sibling generation that legitimately shares the same ref.
//   • A latestGeneration counter lets the generation-check inside runRefresh
//     discard out-of-order responses when a newer request has already
//     published its result (e.g., a background refresh overtakes a slow one).
//   • On request failure, existing data is kept (not blanked); only `error`
//     is updated. This matches the "retain stale rows" requirement.

import type { DaemonIdentity, DaemonListResponse, DaemonRetireResponse } from "@evener/appwire-client";
import { errorText } from "@evener/appwire-client";
import { useStore } from "zustand";
import { createStore } from "zustand/vanilla";
import { connectedClientPort } from "./connection";

export interface DaemonResidentsStoreState {
  /** Latest successful list response; null until first successful load. */
  data: DaemonListResponse | null;
  loading: boolean;
  /** Non-null when the most-recent refresh failed. Stale data is kept. */
  error: string | null;
  /**
   * Row keys (see residentRowKey) of the identity rows with an action currently
   * in flight: `gen:<generation>`, or `ref:<ref>` when the row reports no
   * generation.
   */
  pending: Set<string>;
  refresh(): Promise<void>;
  retire(identity: DaemonIdentity): Promise<DaemonRetireResponse>;
  forceStop(identity: DaemonIdentity): Promise<void>;
}

// residentRowKey identifies one resident row for React keys and per-row state.
// listDaemons dedups by identity.generation, not identity.ref, so a replacement
// or a retire-vs-resume overlap yields two live rows sharing one ref. Keying by
// generation keeps each row's retire result, action error and pending entry on
// the row it belongs to; the ref is the fallback for a row that reports no
// generation. The namespaced form matches the <tr> key in hubResidents.tsx.
export function residentRowKey(identity: DaemonIdentity): string {
  return identity.generation ? `gen:${identity.generation}` : `ref:${identity.ref}`;
}

// requireClient resolves connectionStore's CURRENT client, labelled by this
// store - the shared port (stores/connection.ts), not a hand-rolled twin.
const { requireClient } = connectedClientPort("daemonResidents");

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

// refreshAfterInflight starts a genuinely new list fetch once any in-flight
// refresh settles. Joining the in-flight promise alone (ensureInflight) can
// return a poll that started before a mutation, so the row would keep showing
// the pre-mutation lifecycle until the next interval.
async function refreshAfterInflight(): Promise<void> {
  const current = inflight;
  if (current) {
    await current.catch(() => undefined);
  }
  await ensureInflight();
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
    const key = residentRowKey(identity);
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
      void refreshAfterInflight();
    }
  },

  async forceStop(identity: DaemonIdentity): Promise<void> {
    const client = requireClient();
    const key = residentRowKey(identity);
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
      void refreshAfterInflight();
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
