// The lifecycle contract createStoreLifecycle gives every extensions store,
// run once against each store that wraps it: the subscription start() opens,
// the debounce window the refetch coalesces on, and what reset() and dispose()
// do to the replies still on the wire. What a store does with its OWN
// notification the moment it arrives - a revision it moves, a cache it retires
// - is that store's own contract and stays in its own test file, together with
// everything its list, mutations and caches do.
//
// Each store describes itself to the suite as the calls it makes rather than
// as the names it makes them under, so every request and notification here is
// typed by FakeClient exactly as it is in the store's own tests.

import { afterEach, beforeEach, describe, expect, test, vi } from "vitest";
import type { FrameworkFreeStore } from "../../frameworkFreeStore";
import { deferRequest, FakeClient } from "../../testing/fakeClient";
import type { LaunchConfigLayer, MarketplaceEntry, PluginEntry } from "../../types.gen";
import { createLaunchLayerStore, LAUNCH_LAYER_REFETCH_DEBOUNCE_MS, type LaunchLayerState } from "./launchLayer";
import { createMarketplacesStore, MARKETPLACE_REFETCH_DEBOUNCE_MS, type MarketplacesState } from "./marketplaces";
import { createPluginsStore, PLUGIN_REFETCH_DEBOUNCE_MS, type PluginsState } from "./plugins";
import type { StoreLifecycle } from "./storeLifecycle";

type LifecycleStore<S> = FrameworkFreeStore<S> & Omit<StoreLifecycle<S>, "guard">;

interface LifecycleCase<S> {
  /** A store over a fake of its own. */
  create(): { fake: FakeClient; store: LifecycleStore<S> };
  debounceMs: number;
  /** The notification the store follows, and one it does not own. */
  notifyUpdated(fake: FakeClient): void;
  notifyUnrelated(fake: FakeClient): void;
  /** Answers the list request; listCalls counts the ones that were sent. */
  answerList(fake: FakeClient): void;
  listCalls(fake: FakeClient): number;
  /** Holds the next list request (or the mutation's); the returned function
   * answers it. */
  deferList(fake: FakeClient): () => void;
  deferMutation(fake: FakeClient): () => void;
  fetch(state: S): Promise<void>;
  /** The mutation deferMutation holds. */
  mutate(state: S): Promise<void>;
  /** True while the list is loading - the field the store's own fetch sets. */
  loading(state: S): boolean;
  /** The list fields reset() must restore. */
  initial: Partial<S>;
}

const MARKETPLACES: LifecycleCase<MarketplacesState> = {
  create: () => {
    const fake = new FakeClient("ready");
    return { fake, store: createMarketplacesStore(fake) };
  },
  debounceMs: MARKETPLACE_REFETCH_DEBOUNCE_MS,
  notifyUpdated: (fake) => fake.emitNotification({ method: "evener/marketplace/updated", params: {} }),
  notifyUnrelated: (fake) => fake.emitNotification({ method: "evener/plugin/updated", params: {} }),
  answerList: (fake) => fake.on("evener/marketplace/list", () => ({ marketplaces: [] })),
  listCalls: (fake) => fake.calls.filter((c) => c.method === "evener/marketplace/list").length,
  deferList: (fake) => {
    const release = deferRequest<{ marketplaces: MarketplaceEntry[] }>(fake, "evener/marketplace/list");
    return () => release({ marketplaces: [] });
  },
  deferMutation: (fake) => {
    const release = deferRequest<{ marketplaces: MarketplaceEntry[] }>(fake, "evener/marketplace/remove");
    return () => release({ marketplaces: [] });
  },
  fetch: (state) => state.fetchMarketplaces(),
  mutate: (state) => state.removeMarketplace("acme"),
  loading: (state) => state.marketplacesLoading,
  initial: { marketplaces: null, marketplacesLoading: false, marketplacesError: null },
};

const PLUGINS: LifecycleCase<PluginsState> = {
  create: () => {
    const fake = new FakeClient("ready");
    return { fake, store: createPluginsStore(fake) };
  },
  debounceMs: PLUGIN_REFETCH_DEBOUNCE_MS,
  notifyUpdated: (fake) => fake.emitNotification({ method: "evener/plugin/updated", params: {} }),
  notifyUnrelated: (fake) => fake.emitNotification({ method: "evener/marketplace/updated", params: {} }),
  answerList: (fake) => fake.on("evener/plugin/list", () => ({ plugins: [] })),
  listCalls: (fake) => fake.calls.filter((c) => c.method === "evener/plugin/list").length,
  deferList: (fake) => {
    const release = deferRequest<{ plugins: PluginEntry[] }>(fake, "evener/plugin/list");
    return () => release({ plugins: [] });
  },
  deferMutation: (fake) => {
    const release = deferRequest<{ plugins: PluginEntry[] }>(fake, "evener/plugin/remove");
    return () => release({ plugins: [] });
  },
  fetch: (state) => state.fetchPlugins(),
  mutate: (state) => state.removePlugin("linter", "acme"),
  loading: (state) => state.pluginsLoading,
  initial: { plugins: null, pluginsLoading: false, pluginsError: null, pluginRevision: 0 },
};

const LAUNCH_LAYER: LifecycleCase<LaunchLayerState> = {
  create: () => {
    const fake = new FakeClient("ready");
    return { fake, store: createLaunchLayerStore(fake) };
  },
  debounceMs: LAUNCH_LAYER_REFETCH_DEBOUNCE_MS,
  notifyUpdated: (fake) =>
    fake.emitNotification({ method: "evener/launch/updated", params: { cwd: "/", layer: "global" } }),
  notifyUnrelated: (fake) => fake.emitNotification({ method: "evener/plugin/updated", params: {} }),
  answerList: (fake) => fake.on("evener/launch/getLayer", () => ({})),
  listCalls: (fake) => fake.calls.filter((c) => c.method === "evener/launch/getLayer").length,
  deferList: (fake) => {
    const release = deferRequest<LaunchConfigLayer>(fake, "evener/launch/getLayer");
    return () => release({});
  },
  deferMutation: (fake) => {
    const release = deferRequest<{ effective: LaunchConfigLayer }>(fake, "evener/launch/setLayer");
    return () => release({ effective: {} });
  },
  fetch: (state) => state.fetchLaunchLayer(),
  mutate: (state) => state.setLaunchLayer({ pluginDirs: ["/opt/plugins"] }),
  loading: (state) => state.launchLayerLoading,
  initial: { launchLayer: null, launchLayerLoading: false, launchLayerError: null },
};

function runLifecycleSuite<S>(name: string, lifecycle: LifecycleCase<S>): void {
  describe(`${name} store lifecycle`, () => {
    beforeEach(() => {
      vi.useFakeTimers();
    });
    afterEach(() => {
      vi.useRealTimers();
    });

    test("getInitialState is the state the store was created with, and setState notifies with new and previous", () => {
      const { store } = lifecycle.create();
      const initial = store.getInitialState();
      const seen: Array<[boolean, boolean]> = [];
      store.subscribe((state, previous) => seen.push([lifecycle.loading(state), lifecycle.loading(previous)]));

      store.setState(lifecycle.initial);
      expect(seen).toEqual([[false, false]]);
      expect(store.getInitialState()).toBe(initial);
      expect(initial).toMatchObject(lifecycle.initial);
    });

    test("a store that never started ignores the notification", async () => {
      const { fake, store } = lifecycle.create();
      lifecycle.answerList(fake);
      lifecycle.notifyUpdated(fake);
      await vi.advanceTimersByTimeAsync(lifecycle.debounceMs);
      expect(fake.calls).toHaveLength(0);
      expect(store.getState()).toMatchObject(lifecycle.initial);
    });

    test("an unrelated notification moves nothing", async () => {
      const { fake, store } = lifecycle.create();
      store.start();
      lifecycle.notifyUnrelated(fake);
      await vi.advanceTimersByTimeAsync(lifecycle.debounceMs);
      expect(fake.calls).toHaveLength(0);
      expect(store.getState()).toMatchObject(lifecycle.initial);
    });

    test("dispose() unsubscribes, cancels a pending refetch and fences replies in flight", async () => {
      const { fake, store } = lifecycle.create();
      store.start();
      lifecycle.answerList(fake);
      lifecycle.notifyUpdated(fake);
      const release = lifecycle.deferList(fake);
      const fetching = lifecycle.fetch(store.getState());
      await Promise.resolve();
      const before = store.getState();

      store.dispose();
      await vi.advanceTimersByTimeAsync(lifecycle.debounceMs);
      lifecycle.notifyUpdated(fake);
      await vi.advanceTimersByTimeAsync(lifecycle.debounceMs);
      release();
      await fetching;

      expect(store.getState()).toBe(before);
      expect(lifecycle.listCalls(fake)).toBe(1);
      store.start(); // refused after dispose
      lifecycle.notifyUpdated(fake);
      await vi.advanceTimersByTimeAsync(lifecycle.debounceMs);
      expect(store.getState()).toBe(before);
      expect(lifecycle.listCalls(fake)).toBe(1);
    });

    test("a mutation that resolves after dispose() publishes nothing", async () => {
      const { fake, store } = lifecycle.create();
      lifecycle.answerList(fake);
      await lifecycle.fetch(store.getState());
      const release = lifecycle.deferMutation(fake);
      const mutating = lifecycle.mutate(store.getState());
      await Promise.resolve();
      const before = store.getState();
      let notified = 0;
      store.subscribe(() => {
        notified += 1;
      });

      store.dispose();
      release();
      await mutating;

      expect(notified).toBe(0);
      expect(store.getState()).toBe(before);
    });

    test("reset() returns to the initial state and fences the list still in flight", async () => {
      const { fake, store } = lifecycle.create();
      const release = lifecycle.deferList(fake);
      const fetching = lifecycle.fetch(store.getState());
      await Promise.resolve();

      store.reset();
      expect(store.getState()).toMatchObject(lifecycle.initial);

      release();
      await fetching;
      expect(store.getState()).toMatchObject(lifecycle.initial);

      // The store keeps working after a reset.
      lifecycle.answerList(fake);
      await lifecycle.fetch(store.getState());
      expect(lifecycle.loading(store.getState())).toBe(false);
      expect(lifecycle.listCalls(fake)).toBe(2);
    });
  });
}

runLifecycleSuite("marketplaces", MARKETPLACES);
runLifecycleSuite("plugins", PLUGINS);
runLifecycleSuite("launch layer", LAUNCH_LAYER);
