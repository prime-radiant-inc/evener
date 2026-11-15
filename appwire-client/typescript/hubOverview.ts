// The hub overview store: a fetch-once cache of evener/settings/overview, the
// field bag behind the web's General, Hub, Storage, Agents and MCP-discovered
// settings sections and the native hub settings screen. createHubOverviewStore
// is a factory - each app builds the one instance it wires up, and tests build
// their own - over the package's framework-free store, with reset() and
// dispose() as store-bound extras.
//
// "Caches" means exactly one thing: once `data` is non-null, fetch() is a
// no-op. A failed request (data still null) is NOT cached - the next fetch()
// is a real retry. refresh() always issues a request regardless of `data`;
// while one is already in flight it waits for that one and then issues a
// genuinely new request, because an in-flight read that started before the
// update causing the refresh would answer with stale data. Concurrent
// fetches join the in-flight request; concurrent refreshes coalesce into
// one follow-up. There is no push-driven invalidation (no
// evener/settings/* notification exists on the wire), so callers decide
// when to fetch() or refresh().
//
// The client is a port, read at request time, so a host whose connection can
// be replaced (the web's connection store) hands in an object that resolves
// the current client on each call.

import type { AppwireClient } from "./client";
import { friendlyErrorMessage } from "./errors";
import { createFrameworkFreeStore, type FrameworkFreeStore, type StoreListener } from "./frameworkFreeStore";
import type { SettingsOverviewResponse } from "./types.gen";

export type HubOverviewClient = Pick<AppwireClient, "request">;

export interface HubOverviewState {
  /** null until the first successful load; a failed refresh keeps whatever
   * was last loaded rather than blanking an already-populated screen. */
  data: SettingsOverviewResponse | null;
  loading: boolean;
  /** The failed request's friendlyErrorMessage, translated while the error
   * still has its type so a hub's own WireError explanation survives; render
   * it as-is. null once a request succeeds. */
  error: string | null;
  /** Loads once: a no-op while `data` is already populated. */
  fetch(): Promise<void>;
  /** Always requests. When a request is already in flight, waits for it and
   * then issues a genuinely new one — an in-flight request that started
   * before the update causing this refresh must not answer for it. */
  refresh(): Promise<void>;
}

export type HubOverviewListener = StoreListener<HubOverviewState>;

export interface HubOverviewStore extends FrameworkFreeStore<HubOverviewState> {
  /** Back to the initial state; a request still in flight publishes nothing
   * when it lands, and the next fetch() requests again. */
  reset(): void;
  /** Terminal: drops the in-flight result and turns every later
   * fetch()/refresh() into a no-op, so subscribers hear nothing more - for a
   * host whose screen unmounts. */
  dispose(): void;
}

export function createHubOverviewStore(client: HubOverviewClient): HubOverviewStore {
  let inflight: Promise<void> | null = null;
  // Bumped by reset() and dispose(); a request that started under an older
  // generation publishes nothing when it lands.
  let generation = 0;
  let disposed = false;

  const store = createFrameworkFreeStore<HubOverviewState>((_set, get) => ({
    data: null,
    loading: false,
    error: null,
    async fetch() {
      if (get().data !== null) return;
      await ensureInflight();
    },
    async refresh() {
      // Never let the in-flight request stand in for this one: it started
      // before the update that caused the refresh, so joining it would
      // publish pre-update data and leave the caller stale until the next
      // update. Wait for it (swallowing its failure; this call's own outcome
      // is the follow-up's) and then request again — concurrent refreshes
      // coalesce into that single follow-up.
      const current = inflight;
      if (current) {
        await current.catch(() => undefined);
      }
      await ensureInflight();
    },
  }));

  async function runRequest(): Promise<void> {
    const started = generation;
    const publish = (partial: Partial<HubOverviewState>) => {
      if (started === generation) store.setState(partial);
    };
    publish({ loading: true, error: null });
    try {
      const data = await client.request("evener/settings/overview", {});
      publish({ data, loading: false, error: null });
    } catch (err) {
      // `data` is deliberately left out: the shallow merge keeps whatever was
      // there (null, or the last successful load) through a failed request.
      publish({ loading: false, error: friendlyErrorMessage(err) });
    }
  }

  function ensureInflight(): Promise<void> {
    if (disposed) return Promise.resolve();
    if (!inflight) {
      // Cleared by identity: a reset() may have started a newer request by the
      // time this one lands, and that one keeps its dedup slot.
      const request = runRequest().finally(() => {
        if (inflight === request) inflight = null;
      });
      inflight = request;
    }
    return inflight;
  }

  const reset = () => {
    generation += 1;
    inflight = null;
    store.setState(store.getInitialState());
  };

  return {
    ...store,
    reset,
    dispose() {
      disposed = true;
      reset();
    },
  };
}
