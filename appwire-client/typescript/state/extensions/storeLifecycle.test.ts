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
import { createFrameworkFreeStore, type FrameworkFreeStore } from "../../frameworkFreeStore";
import { deferFailure, deferRequest, FakeClient, failing } from "../../testing/fakeClient";
import type { LaunchConfigLayer, MarketplaceEntry, PluginEntry } from "../../types.gen";
import { createLaunchLayerStore, LAUNCH_LAYER_REFETCH_DEBOUNCE_MS, type LaunchLayerState } from "./launchLayer";
import { createMarketplacesStore, MARKETPLACE_REFETCH_DEBOUNCE_MS, type MarketplacesState } from "./marketplaces";
import { createPluginsStore, PLUGIN_REFETCH_DEBOUNCE_MS, type PluginsState } from "./plugins";
import { createStoreLifecycle, type StoreLifecycle } from "./storeLifecycle";

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
  /** Holds EVERY list request, one answerer per call, so two reads can be in
   * flight and be answered out of order. */
  gateList(fake: FakeClient): (() => void)[];
  deferMutation(fake: FakeClient): () => void;
  /** Scripts the mutation to fail. */
  failMutation(fake: FakeClient): void;
  /** Holds the mutation; the returned function fails it. */
  deferFailingMutation(fake: FakeClient): () => void;
  /** Holds EVERY mutation, one rejecter per call, so two failing writes can be
   * in flight and fail in either order. */
  gateFailingMutations(fake: FakeClient): (() => void)[];
  /** The list the store holds, null until something has read it. */
  list(state: S): unknown;
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
  gateList: (fake) => {
    const releases: (() => void)[] = [];
    fake.on(
      "evener/marketplace/list",
      () => new Promise((resolve) => releases.push(() => resolve({ marketplaces: [] }))) as never,
    );
    return releases;
  },
  deferList: (fake) => {
    const release = deferRequest<{ marketplaces: MarketplaceEntry[] }>(fake, "evener/marketplace/list");
    return () => release({ marketplaces: [] });
  },
  deferMutation: (fake) => {
    const release = deferRequest<{ marketplaces: MarketplaceEntry[] }>(fake, "evener/marketplace/remove");
    return () => release({ marketplaces: [] });
  },
  failMutation: (fake) => fake.on("evener/marketplace/remove", failing("write refused")),
  deferFailingMutation: (fake) => {
    const fail = deferFailure(fake, "evener/marketplace/remove");
    return () => fail(new Error("write refused"));
  },
  gateFailingMutations: (fake) => {
    const rejecters: (() => void)[] = [];
    fake.on(
      "evener/marketplace/remove",
      () => new Promise((_, reject) => rejecters.push(() => reject(new Error("write refused")))) as never,
    );
    return rejecters;
  },
  list: (state) => state.marketplaces,
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
  gateList: (fake) => {
    const releases: (() => void)[] = [];
    fake.on(
      "evener/plugin/list",
      () => new Promise((resolve) => releases.push(() => resolve({ plugins: [] }))) as never,
    );
    return releases;
  },
  deferList: (fake) => {
    const release = deferRequest<{ plugins: PluginEntry[] }>(fake, "evener/plugin/list");
    return () => release({ plugins: [] });
  },
  deferMutation: (fake) => {
    const release = deferRequest<{ plugins: PluginEntry[] }>(fake, "evener/plugin/remove");
    return () => release({ plugins: [] });
  },
  failMutation: (fake) => fake.on("evener/plugin/remove", failing("write refused")),
  deferFailingMutation: (fake) => {
    const fail = deferFailure(fake, "evener/plugin/remove");
    return () => fail(new Error("write refused"));
  },
  gateFailingMutations: (fake) => {
    const rejecters: (() => void)[] = [];
    fake.on(
      "evener/plugin/remove",
      () => new Promise((_, reject) => rejecters.push(() => reject(new Error("write refused")))) as never,
    );
    return rejecters;
  },
  list: (state) => state.plugins,
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
  gateList: (fake) => {
    const releases: (() => void)[] = [];
    fake.on("evener/launch/getLayer", () => new Promise((resolve) => releases.push(() => resolve({}))) as never);
    return releases;
  },
  deferList: (fake) => {
    const release = deferRequest<LaunchConfigLayer>(fake, "evener/launch/getLayer");
    return () => release({});
  },
  deferMutation: (fake) => {
    const release = deferRequest<{ effective: LaunchConfigLayer }>(fake, "evener/launch/setLayer");
    return () => release({ effective: {} });
  },
  failMutation: (fake) => fake.on("evener/launch/setLayer", failing("write refused")),
  deferFailingMutation: (fake) => {
    const fail = deferFailure(fake, "evener/launch/setLayer");
    return () => fail(new Error("write refused"));
  },
  gateFailingMutations: (fake) => {
    const rejecters: (() => void)[] = [];
    fake.on(
      "evener/launch/setLayer",
      () => new Promise((_, reject) => rejecters.push(() => reject(new Error("write refused")))) as never,
    );
    return rejecters;
  },
  list: (state) => state.launchLayer,
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

    test("an established list is read again when the connection is ready again", async () => {
      const { fake, store } = lifecycle.create();
      lifecycle.answerList(fake);
      await lifecycle.fetch(store.getState());
      expect(lifecycle.listCalls(fake)).toBe(1);

      // The hub broadcasts a change to every CONNECTED client, so a change
      // made while this one was away reaches it as nothing at all: the
      // notification the lifecycle follows is not a recovery path, and the
      // reconnect is.
      store.connectionChanged(fake, "reconnecting");
      lifecycle.notifyUpdated(fake);
      await vi.advanceTimersByTimeAsync(lifecycle.debounceMs);
      store.connectionChanged(fake, "ready");
      await vi.advanceTimersByTimeAsync(0);
      expect(lifecycle.listCalls(fake)).toBe(2);
    });

    test("a reconnect inside the debounce window reads once, not twice", async () => {
      const { fake, store } = lifecycle.create();
      store.connectionChanged(fake, "ready");
      lifecycle.answerList(fake);
      await lifecycle.fetch(store.getState());
      store.start();

      // The notification's read is scheduled, then the connection it would
      // have been sent on goes away. The recovery read replaces it: leaving
      // the timer up sends a second read for the same change, and a timer
      // that fires while the connection is down records an error the user
      // never has to see.
      lifecycle.notifyUpdated(fake);
      store.connectionChanged(fake, "reconnecting");
      store.connectionChanged(fake, "ready");
      await vi.advanceTimersByTimeAsync(lifecycle.debounceMs * 2);
      expect(lifecycle.listCalls(fake)).toBe(2);
    });

    test("a list nothing has read is not read by a reconnect", async () => {
      const { fake, store } = lifecycle.create();
      lifecycle.answerList(fake);

      store.connectionChanged(fake, "reconnecting");
      store.connectionChanged(fake, "ready");
      await vi.advanceTimersByTimeAsync(lifecycle.debounceMs);
      // No request, and the list is still unread. What the reconnection DOES
      // move is whatever a notification moves - a revision a host re-keys
      // host-scoped data on - which is not about this list and is asserted by
      // the stores that have such a thing.
      expect(fake.calls).toHaveLength(0);
      expect(lifecycle.list(store.getState())).toBeNull();
      expect(lifecycle.loading(store.getState())).toBe(false);
    });

    test("a replacement client that arrives ready reads an established list", async () => {
      const { fake, store } = lifecycle.create();
      store.connectionChanged(fake, "ready"); // the connection the host reports before anything reads
      lifecycle.answerList(fake);
      await lifecycle.fetch(store.getState());

      store.connectionChanged(fake, "ready");
      await vi.advanceTimersByTimeAsync(0);
      expect(lifecycle.listCalls(fake)).toBe(1); // the same connection, still ready: nothing to recover

      const replacement = lifecycle.create().fake;
      store.connectionChanged(replacement, "ready");
      await vi.advanceTimersByTimeAsync(0);
      expect(lifecycle.listCalls(fake)).toBe(2);
    });

    test("reset() after dispose() publishes nothing", async () => {
      const { fake, store } = lifecycle.create();
      lifecycle.answerList(fake);
      await lifecycle.fetch(store.getState());
      const before = store.getState();
      let notified = 0;
      store.subscribe(() => {
        notified += 1;
      });

      store.dispose();
      store.reset();

      expect(notified).toBe(0);
      expect(store.getState()).toBe(before);
    });

    test("a read outrun before it lands writes nothing, not even the flag it raised", async () => {
      const { fake, store } = lifecycle.create();
      const releases = lifecycle.gateList(fake);
      const older = lifecycle.fetch(store.getState());
      await Promise.resolve();
      const newer = lifecycle.fetch(store.getState());
      await Promise.resolve();
      const [answerOlder, answerNewer] = releases;
      expect(releases).toHaveLength(2);
      if (!answerOlder || !answerNewer) throw new Error("both reads must be in flight");

      // The older read answers first. The newer request is still pending and
      // it raised the flag: a response the store has already superseded may
      // not clear it, nor post its own error over a load still running.
      answerOlder();
      await older;
      expect(lifecycle.loading(store.getState())).toBe(true);

      answerNewer();
      await newer;
      expect(lifecycle.loading(store.getState())).toBe(false);
    });

    test("a write that publishes nothing hands the state back to the read it outran", async () => {
      const { fake, store } = lifecycle.create();
      const releases = lifecycle.gateList(fake);
      const reading = lifecycle.fetch(store.getState());
      await Promise.resolve();
      expect(lifecycle.loading(store.getState())).toBe(true);

      // A write issued while the read is in flight fences it - and then fails,
      // publishing nothing. It must not keep what it fenced: otherwise the
      // read lands superseded, writes none of its three fields, and the flag
      // it raised stays up with nothing left to lower it.
      lifecycle.failMutation(fake);
      await expect(lifecycle.mutate(store.getState())).rejects.toThrow("write refused");

      const answer = releases[0];
      if (!answer) throw new Error("the read must be in flight");
      answer();
      await reading;
      expect(lifecycle.loading(store.getState())).toBe(false);
    });

    test("a reply from the connection that was replaced publishes nothing", async () => {
      const { fake, store } = lifecycle.create();
      store.connectionChanged(fake, "ready");
      const releases = lifecycle.gateList(fake);
      const reading = lifecycle.fetch(store.getState());
      await Promise.resolve();

      // A different hub answers different questions, and this reply is the
      // previous one's answer: it describes a machine this store no longer
      // speaks to.
      store.connectionChanged(lifecycle.create().fake, "ready");
      const answer = releases[0];
      if (!answer) throw new Error("the read must be in flight");
      answer();
      await reading;

      expect(lifecycle.list(store.getState())).toBeNull();
      expect(lifecycle.loading(store.getState())).toBe(false);
    });

    test("a read that landed fenced is applied when the write that fenced it retracts", async () => {
      const { fake, store } = lifecycle.create();
      const releases = lifecycle.gateList(fake);
      const reading = lifecycle.fetch(store.getState());
      await Promise.resolve();
      const failWrite = lifecycle.deferFailingMutation(fake);
      const mutating = lifecycle.mutate(store.getState());
      await Promise.resolve();

      // The read answers first and is superseded, so it publishes nothing -
      // the flag it raised belongs to the write now.
      const answer = releases[0];
      if (!answer) throw new Error("the read must be in flight");
      answer();
      await reading;
      expect(lifecycle.loading(store.getState())).toBe(true);

      // Then the write fails, publishing nothing and giving the state back.
      // The read's answer is the newest one anybody has, and it is the last
      // one there will be: nothing else is coming to lower that flag.
      failWrite();
      await expect(mutating).rejects.toThrow("write refused");
      expect(lifecycle.loading(store.getState())).toBe(false);
      expect(lifecycle.list(store.getState())).not.toBeNull();
    });

    for (const [name, order] of [
      ["the older", [0, 1]],
      ["the newer", [1, 0]],
    ] as const) {
      test(`${name} of two failed writes failing first still lets the read it fenced land`, async () => {
        const { fake, store } = lifecycle.create();
        const reads = lifecycle.gateList(fake);
        const reading = lifecycle.fetch(store.getState());
        await Promise.resolve();
        const fails = lifecycle.gateFailingMutations(fake);
        const first = lifecycle.mutate(store.getState());
        await Promise.resolve();
        const second = lifecycle.mutate(store.getState());
        await Promise.resolve();
        expect(fails).toHaveLength(2);

        // Whichever order the two writes fail in, neither published anything,
        // so neither may keep the read's answer from landing - and that answer
        // is the only thing left that can lower the flag the read raised.
        for (const index of order) {
          const fail = fails[index];
          if (!fail) throw new Error("both writes must be in flight");
          fail();
        }
        await expect(first).rejects.toThrow("write refused");
        await expect(second).rejects.toThrow("write refused");

        const answer = reads[0];
        if (!answer) throw new Error("the read must be in flight");
        answer();
        await reading;

        expect(lifecycle.loading(store.getState())).toBe(false);
        expect(lifecycle.list(store.getState())).not.toBeNull();
      });
    }

    test("a connection update that changes nothing leaves a scheduled read alone", async () => {
      const { fake, store } = lifecycle.create();
      store.connectionChanged(fake, "ready");
      lifecycle.answerList(fake);
      await lifecycle.fetch(store.getState());
      store.start();

      // The host reports its connection on every change it publishes, and most
      // of those are metadata: the handshake's serverInfo and features land as
      // one, on the client and state the store already has. Cancelling the
      // read the notification scheduled would drop the change it was about,
      // with no recovery read to replace it - the recovery only runs on a
      // transition.
      lifecycle.notifyUpdated(fake);
      store.connectionChanged(fake, "ready");
      await vi.advanceTimersByTimeAsync(lifecycle.debounceMs);
      expect(lifecycle.listCalls(fake)).toBe(2);
    });

    test("a reconnect after dispose() reads nothing", async () => {
      const { fake, store } = lifecycle.create();
      lifecycle.answerList(fake);
      await lifecycle.fetch(store.getState());

      store.dispose();
      store.connectionChanged(fake, "reconnecting");
      store.connectionChanged(fake, "ready");
      await vi.advanceTimersByTimeAsync(lifecycle.debounceMs);
      expect(lifecycle.listCalls(fake)).toBe(1);
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

// dispose() unsubscribes, but unsubscribing is only the cooperative half: a
// dispatcher that snapshots its handler set - AppwireClient.setState does,
// and stores/connection.ts documents the same class for its own listener -
// still calls a handler removed during that dispatch. The stub below is that
// dispatcher, reduced to the one behaviour: it hands back an unsubscribe that
// does nothing, so the callback outlives the store it belonged to.
describe("a notification callback that outlives dispose()", () => {
  beforeEach(() => {
    vi.useFakeTimers();
  });
  afterEach(() => {
    vi.useRealTimers();
  });

  test("publishes nothing and schedules no read", async () => {
    let deliver!: (n: { method: string }) => void;
    const client = {
      onNotification(cb: (n: { method: string }) => void) {
        deliver = cb;
        return () => undefined; // the handler stays reachable
      },
    } as unknown as FakeClient;

    let reads = 0;
    let notified = 0;
    const lifecycle = createStoreLifecycle<{ marker: number }>(client, {
      method: "evener/plugin/updated",
      debounceMs: 250,
      store: () => store,
      refetch: () => {
        reads += 1;
      },
      onNotified: () => store.setState((s) => ({ marker: s.marker + 1 })),
      established: () => true,
    });
    const store = createFrameworkFreeStore<{ marker: number }>((publish) => {
      void lifecycle.guard(publish);
      return { marker: 0 };
    });
    lifecycle.start();
    store.subscribe(() => {
      notified += 1;
    });

    lifecycle.dispose();
    deliver({ method: "evener/plugin/updated" });
    await vi.advanceTimersByTimeAsync(250);

    expect(notified).toBe(0);
    expect(store.getState().marker).toBe(0);
    expect(reads).toBe(0);
  });
});
