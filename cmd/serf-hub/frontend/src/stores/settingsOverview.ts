// settingsOverview.ts is a fetch-once-cache mirror of serf/settings/
// overview - the field bag behind Settings -> General/Hub/Storage (this
// stream's own sections) AND Agents/Codex launch/MCP-discovered (T2/T3's
// sections, in sibling worktrees). Its shape is PINNED across all three
// streams (wave-7 plan): `{ data, loading, error, fetch() }`, plus an
// additive `refresh()` - do not change this surface without checking both
// sibling streams' tests, which are written against it directly.
//
// Unlike threads.ts, this store never holds a persistent client
// subscription to go stale on reconnect: fetch()/refresh() each read
// connectionStore's CURRENT client fresh, at call time, so there is no
// wiring to move to a new client after a reconnect - simpler than
// threads.ts's own rewireClient machinery by construction, not an
// oversight. There is also no push-notification-driven invalidation (no
// serf/settings/* notification exists on the wire) - every section that
// wants fresh data calls fetch() (cached) or refresh() (forced) itself.
//
// "Caches" means exactly one thing: once `data` is non-null, fetch() is a
// no-op for the rest of this session. A failed fetch (data still null)
// is NOT cached - a caller that calls fetch() again after a failure gets a
// real retry, matching ordinary "the user navigated back to this section"
// expectations. refresh() always issues a fresh request regardless of
// `data`, but joins an already-in-flight request rather than firing a
// second concurrent one (same dedup queued fetch() calls share).

import { useStore } from "zustand";
import { createStore } from "zustand/vanilla";
import { errorText } from "../protocol/errors";
import type { AppwireClientLike } from "../protocol/testing/fakeClient";
import type { SettingsOverviewResponse } from "../protocol/types.gen";
import { connectionStore } from "./connection";

export interface SettingsOverviewStoreState {
  // null until the first successful load; a failed refresh leaves whatever
  // was last successfully loaded in place rather than blanking it (mirrors
  // stores/tree.ts's own TreeStoreState.tree doc comment) - a transient
  // fetch error must not flash an already-populated section back to empty.
  data: SettingsOverviewResponse | null;
  loading: boolean;
  error: string | null;
  fetch(): Promise<void>;
  refresh(): Promise<void>;
}

// requireClient reads the client connection.ts wired via
// useConnectionStore.getState().connect(client) - this store has no
// connect() of its own, same rationale as threads.ts's own requireClient.
function requireClient(): AppwireClientLike {
  const client = connectionStore.getState().client;
  if (!client) {
    throw new Error(
      "settingsOverview store: no client connected; call useConnectionStore.getState().connect(client) first",
    );
  }
  return client;
}

// inflight dedupes concurrent fetch()/refresh() callers onto one real
// request - module-private, mirroring threads.ts's own inflightHydrates
// bookkeeping, scaled down to the single-resource (not per-ref) case.
let inflight: Promise<void> | null = null;

async function runFetch(): Promise<void> {
  settingsOverviewStore.setState({ loading: true, error: null });
  try {
    const client = requireClient();
    const data = await client.request("serf/settings/overview", {});
    settingsOverviewStore.setState({ data, loading: false, error: null });
  } catch (err) {
    // `data` is deliberately left out of this patch - zustand's setState
    // shallow-merges, so whatever was there (null, or a previous successful
    // load) survives a failed request untouched.
    settingsOverviewStore.setState({ loading: false, error: errorText(err) });
  }
}

function ensureInflight(): Promise<void> {
  if (!inflight) {
    inflight = runFetch().finally(() => {
      inflight = null;
    });
  }
  return inflight;
}

export const settingsOverviewStore = createStore<SettingsOverviewStoreState>(() => ({
  data: null,
  loading: false,
  error: null,

  async fetch() {
    if (settingsOverviewStore.getState().data !== null) return; // cached: already loaded this session
    await ensureInflight();
  },

  async refresh() {
    await ensureInflight();
  },
}));

export function useSettingsOverviewStore(): SettingsOverviewStoreState;
export function useSettingsOverviewStore<T>(selector: (state: SettingsOverviewStoreState) => T): T;
export function useSettingsOverviewStore<T>(
  selector?: (state: SettingsOverviewStoreState) => T,
): T | SettingsOverviewStoreState {
  // Not a real conditional hook call - see stores/connection.ts's own
  // useConnectionStore for the full explanation (zustand's useStore has a
  // `selector = identity` JS default param, so both arms run identically).
  // biome-ignore lint/correctness/useHookAtTopLevel: same hook both arms, JS default param not a real conditional - see stores/connection.ts
  return selector ? useStore(settingsOverviewStore, selector) : useStore(settingsOverviewStore);
}

// resetSettingsOverviewStoreForTests resets the store to its initial state
// and clears the module-private inflight marker - settingsOverview.ts is a
// singleton store shared by the whole app, so settingsOverview.test.ts (and
// any section test that touches it) must reset it between tests to keep
// them isolated. No production code should ever call this (mirrors
// threads.ts's resetThreadsStoreForTests / tree.ts's
// resetTreeStoreForTests precedent).
export function resetSettingsOverviewStoreForTests(): void {
  inflight = null;
  settingsOverviewStore.setState({ data: null, loading: false, error: null });
}
