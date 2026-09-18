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
import type { ConnectionState } from "../../client";
import { createFrameworkFreeStore, type FrameworkFreeStore } from "../../frameworkFreeStore";
import { answerRequests, callsTo, FakeClient, failRequests, gateSettlements } from "../../testing/fakeClient";
import type { MethodName } from "../../types.gen";
import { createLaunchLayerStore, LAUNCH_LAYER_REFETCH_DEBOUNCE_MS, type LaunchLayerState } from "./launchLayer";
import { createListRevision } from "./listRevision";
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
  /** The store's two wire methods and an answer to each. The suite scripts
   * them through testing/fakeClient's generic helpers, so a store describes
   * itself here as data rather than as a closure per script. */
  listMethod: MethodName;
  listResponse: unknown;
  mutationMethod: MethodName;
  mutationResponse: unknown;
  /** The list the store holds, null until something has read it. */
  list(state: S): unknown;
  fetch(state: S): Promise<void>;
  /** The mutation deferMutation holds. */
  mutate(state: S): Promise<void>;
  /** True while the list is loading - the field the store's own fetch sets. */
  loading(state: S): boolean;
  /** The list fields reset() must restore. */
  initial: Partial<S>;
  /** A piece of state that changes exactly when the lifecycle's onNotified
   * runs (a revision it bumps, a cache it retires) - undefined for a store
   * (launch layer) that passes the lifecycle no onNotified at all, so there
   * is nothing here to observe. */
  invalidationMarker?(state: S): unknown;
}

const MARKETPLACES: LifecycleCase<MarketplacesState> = {
  create: () => {
    const fake = new FakeClient("ready");
    return { fake, store: createMarketplacesStore(fake) };
  },
  debounceMs: MARKETPLACE_REFETCH_DEBOUNCE_MS,
  notifyUpdated: (fake) => fake.emitNotification({ method: "evener/marketplace/updated", params: {} }),
  notifyUnrelated: (fake) => fake.emitNotification({ method: "evener/plugin/updated", params: {} }),
  listMethod: "evener/marketplace/list",
  listResponse: { marketplaces: [] },
  mutationMethod: "evener/marketplace/remove",
  mutationResponse: { marketplaces: [] },
  fetch: (state) => state.fetchMarketplaces(),
  mutate: (state) => state.removeMarketplace("acme"),
  loading: (state) => state.marketplacesLoading,
  list: (state) => state.marketplaces,
  initial: { marketplaces: null, marketplacesLoading: false, marketplacesError: null },
  invalidationMarker: (state) => state.browseCatalogs,
};

const PLUGINS: LifecycleCase<PluginsState> = {
  create: () => {
    const fake = new FakeClient("ready");
    return { fake, store: createPluginsStore(fake) };
  },
  debounceMs: PLUGIN_REFETCH_DEBOUNCE_MS,
  notifyUpdated: (fake) => fake.emitNotification({ method: "evener/plugin/updated", params: {} }),
  notifyUnrelated: (fake) => fake.emitNotification({ method: "evener/marketplace/updated", params: {} }),
  listMethod: "evener/plugin/list",
  listResponse: { plugins: [] },
  mutationMethod: "evener/plugin/remove",
  mutationResponse: { plugins: [] },
  fetch: (state) => state.fetchPlugins(),
  mutate: (state) => state.removePlugin("linter", "acme"),
  loading: (state) => state.pluginsLoading,
  list: (state) => state.plugins,
  initial: { plugins: null, pluginsLoading: false, pluginsError: null, pluginRevision: 0 },
  invalidationMarker: (state) => state.pluginRevision,
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
  listMethod: "evener/launch/getLayer",
  listResponse: {},
  mutationMethod: "evener/launch/setLayer",
  mutationResponse: { effective: {} },
  fetch: (state) => state.fetchLaunchLayer(),
  mutate: (state) => state.setLaunchLayer({ pluginDirs: ["/opt/plugins"] }),
  loading: (state) => state.launchLayerLoading,
  list: (state) => state.launchLayer,
  initial: { launchLayer: null, launchLayerLoading: false, launchLayerError: null },
  // launchLayer's lifecycle passes no onNotified at all, so there is nothing
  // for invalidationMarker to read: see launchLayer.ts's createLaunchLayerStore,
  // whose createStoreLifecycle options omit onNotified.
};

/** The handful of things every ordering case in this suite needs from a fake
 * client and the store built over it: a read or a write that stays in flight
 * until the test settles it, a request that fails outright, and a replaced
 * connection. A new ordering case is written once against these instead of
 * teaching LifecycleCase another wire-level field - listMethod/listResponse/
 * mutationMethod/mutationResponse exist only so this kit can drive the fake
 * underneath a case's own fetch()/mutate(). */
interface LifecycleKit<S> {
  readonly fake: FakeClient;
  readonly store: LifecycleStore<S>;
  /** Issues fetch(), leaving its list request in flight; fails loudly if the
   * request never reached the fake. land() answers it, defaulting to the
   * case's own listResponse. */
  gatedRead(): Promise<{ promise: Promise<void>; land(response?: unknown): void }>;
  /** Issues mutate(), leaving its mutation in flight; fails loudly if the
   * request never reached the fake. land()/fail() settle it, defaulting to
   * the case's own mutationResponse and to "write refused". */
  gatedWrite(): Promise<{ promise: Promise<void>; land(response?: unknown): void; fail(message?: string): void }>;
  /** Issues mutate() against a mutation scripted to fail immediately with
   * `message` - no gate to release, since nothing needs to control when it
   * lands. */
  rejectWrite(message: string): Promise<void>;
  /** Replaces the connection with a fresh, unrelated client, driving it
   * through each of `states` in order (just "ready" if none are given) - the
   * fence every "replacement" ordering case turns on. */
  fence(...states: ConnectionState[]): FakeClient;
}

function createLifecycleKit<S>(lifecycle: LifecycleCase<S>): LifecycleKit<S> {
  const { fake, store } = lifecycle.create();
  return {
    fake,
    store,
    async gatedRead() {
      const settlements = gateSettlements(fake, lifecycle.listMethod);
      const promise = lifecycle.fetch(store.getState());
      await Promise.resolve();
      const settle = settlements[0];
      if (!settle) throw new Error("the read must be in flight");
      return { promise, land: (response: unknown = lifecycle.listResponse) => settle.resolve(response) };
    },
    async gatedWrite() {
      const settlements = gateSettlements(fake, lifecycle.mutationMethod);
      const promise = lifecycle.mutate(store.getState());
      await Promise.resolve();
      const settle = settlements[0];
      if (!settle) throw new Error("the write must be in flight");
      return {
        promise,
        land: (response: unknown = lifecycle.mutationResponse) => settle.resolve(response),
        fail: (message = "write refused") => settle.reject(new Error(message)),
      };
    },
    rejectWrite(message) {
      failRequests(fake, lifecycle.mutationMethod, message);
      return lifecycle.mutate(store.getState());
    },
    fence(...states: ConnectionState[]): FakeClient {
      const replacement = lifecycle.create().fake;
      for (const state of states.length ? states : (["ready"] as const)) store.connectionChanged(replacement, state);
      return replacement;
    },
  };
}

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
      answerRequests(fake, lifecycle.listMethod, lifecycle.listResponse);
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
      const { fake, store, gatedRead } = createLifecycleKit(lifecycle);
      store.start();
      answerRequests(fake, lifecycle.listMethod, lifecycle.listResponse);
      lifecycle.notifyUpdated(fake);
      const read = await gatedRead();
      const before = store.getState();

      store.dispose();
      await vi.advanceTimersByTimeAsync(lifecycle.debounceMs);
      lifecycle.notifyUpdated(fake);
      await vi.advanceTimersByTimeAsync(lifecycle.debounceMs);
      read.land();
      await read.promise;

      expect(store.getState()).toBe(before);
      expect(callsTo(fake, lifecycle.listMethod)).toBe(1);
      store.start(); // refused after dispose
      lifecycle.notifyUpdated(fake);
      await vi.advanceTimersByTimeAsync(lifecycle.debounceMs);
      expect(store.getState()).toBe(before);
      expect(callsTo(fake, lifecycle.listMethod)).toBe(1);
    });

    test("a mutation that resolves after dispose() publishes nothing", async () => {
      const { fake, store, gatedWrite } = createLifecycleKit(lifecycle);
      answerRequests(fake, lifecycle.listMethod, lifecycle.listResponse);
      await lifecycle.fetch(store.getState());
      const write = await gatedWrite();
      const before = store.getState();
      let notified = 0;
      store.subscribe(() => {
        notified += 1;
      });

      store.dispose();
      write.land();
      await write.promise;

      expect(notified).toBe(0);
      expect(store.getState()).toBe(before);
    });

    test("an established list is read again when the connection is ready again", async () => {
      const { fake, store } = lifecycle.create();
      answerRequests(fake, lifecycle.listMethod, lifecycle.listResponse);
      await lifecycle.fetch(store.getState());
      expect(callsTo(fake, lifecycle.listMethod)).toBe(1);

      // The hub broadcasts a change to every CONNECTED client, so a change
      // made while this one was away reaches it as nothing at all: the
      // notification the lifecycle follows is not a recovery path, and the
      // reconnect is.
      store.connectionChanged(fake, "reconnecting");
      lifecycle.notifyUpdated(fake);
      await vi.advanceTimersByTimeAsync(lifecycle.debounceMs);
      store.connectionChanged(fake, "ready");
      await vi.advanceTimersByTimeAsync(0);
      expect(callsTo(fake, lifecycle.listMethod)).toBe(2);
    });

    // The recovery read answers "does something still want this list", not
    // "was the prior state closed or reconnecting" - hasBeenReady only gates
    // the invalidation a notification would also apply (see plugins.test.ts's
    // reconnect describe), so closed recovers an established list exactly as
    // reconnecting does, and leaves an unread one alone exactly as reconnecting
    // does too.
    test("closed -> ready recovers an established list exactly as reconnecting does, and leaves an unread one alone", async () => {
      const { fake, store } = lifecycle.create();
      answerRequests(fake, lifecycle.listMethod, lifecycle.listResponse);
      await lifecycle.fetch(store.getState());

      store.connectionChanged(fake, "closed");
      store.connectionChanged(fake, "ready");
      await vi.advanceTimersByTimeAsync(0);
      expect(callsTo(fake, lifecycle.listMethod)).toBe(2);

      const { fake: fresh, store: unread } = lifecycle.create();
      answerRequests(fresh, lifecycle.listMethod, lifecycle.listResponse);
      unread.connectionChanged(fresh, "closed");
      unread.connectionChanged(fresh, "ready");
      await vi.advanceTimersByTimeAsync(lifecycle.debounceMs);
      expect(fresh.calls).toHaveLength(0);
      expect(lifecycle.list(unread.getState())).toBeNull();
    });

    const invalidationMarker = lifecycle.invalidationMarker;
    if (invalidationMarker) {
      // The case above is the recovery READ, gated by wantsList alone. A
      // store that was ready before "closed" must not be read as a first
      // connection: hasBeenReady already latched true, so the "ready" that
      // follows applies what a notification would have (see #1642) - the
      // only thing "closed" is not evidence of is a FIRST connection.
      test("closed -> ready invalidates an established list's derived data the same way a notification would", async () => {
        const { fake, store } = lifecycle.create();
        store.connectionChanged(fake, "ready");
        answerRequests(fake, lifecycle.listMethod, lifecycle.listResponse);
        await lifecycle.fetch(store.getState());
        const before = invalidationMarker(store.getState());

        store.connectionChanged(fake, "closed");
        store.connectionChanged(fake, "ready");
        await vi.advanceTimersByTimeAsync(0);

        expect(invalidationMarker(store.getState())).not.toBe(before);
      });
    }

    test("a reconnect inside the debounce window reads once, not twice", async () => {
      const { fake, store } = lifecycle.create();
      store.connectionChanged(fake, "ready");
      answerRequests(fake, lifecycle.listMethod, lifecycle.listResponse);
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
      expect(callsTo(fake, lifecycle.listMethod)).toBe(2);
    });

    test("a list nothing has read is not read by a reconnect", async () => {
      const { fake, store } = lifecycle.create();
      answerRequests(fake, lifecycle.listMethod, lifecycle.listResponse);

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

    // The converse of the case above: nothing has read the list, but a
    // mutation is in flight - so nothing in the store's OWN data (the fields
    // wantsList reads) marks the list as wanted, yet the intent is real. The
    // lifecycle reads it off the listRevision the store hands it (its revision
    // option): the seam readRevisioned and writeRevisioned share
    // (listRevision.ts's hasLive).
    test("a write issued before any read still recovers a replaced connection's list", async () => {
      const { fake, store, gatedWrite, fence } = createLifecycleKit(lifecycle);
      store.connectionChanged(fake, "ready");
      answerRequests(fake, lifecycle.listMethod, lifecycle.listResponse);
      const write = await gatedWrite();
      expect(lifecycle.list(store.getState())).toBeNull();

      fence();
      await vi.advanceTimersByTimeAsync(0);
      expect(callsTo(fake, lifecycle.listMethod)).toBe(1);
      expect(lifecycle.list(store.getState())).not.toBeNull();

      write.land();
      await write.promise;
    });

    test("a replacement client that arrives ready reads an established list", async () => {
      const { fake, store, fence } = createLifecycleKit(lifecycle);
      store.connectionChanged(fake, "ready"); // the connection the host reports before anything reads
      answerRequests(fake, lifecycle.listMethod, lifecycle.listResponse);
      await lifecycle.fetch(store.getState());

      store.connectionChanged(fake, "ready");
      await vi.advanceTimersByTimeAsync(0);
      expect(callsTo(fake, lifecycle.listMethod)).toBe(1); // the same connection, still ready: nothing to recover

      fence();
      await vi.advanceTimersByTimeAsync(0);
      expect(callsTo(fake, lifecycle.listMethod)).toBe(2);
    });

    test("reset() after dispose() publishes nothing", async () => {
      const { fake, store } = lifecycle.create();
      answerRequests(fake, lifecycle.listMethod, lifecycle.listResponse);
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
      const { fake, store, gatedRead } = createLifecycleKit(lifecycle);
      const older = await gatedRead();
      const newer = await gatedRead();
      expect(callsTo(fake, lifecycle.listMethod)).toBe(2);

      // The older read answers first. The newer request is still pending and
      // it raised the flag: a response the store has already superseded may
      // not clear it, nor post its own error over a load still running.
      older.land();
      await older.promise;
      expect(lifecycle.loading(store.getState())).toBe(true);

      newer.land();
      await newer.promise;
      expect(lifecycle.loading(store.getState())).toBe(false);
    });

    test("a write that publishes nothing hands the state back to the read it outran", async () => {
      const { store, gatedRead, rejectWrite } = createLifecycleKit(lifecycle);
      const read = await gatedRead();
      expect(lifecycle.loading(store.getState())).toBe(true);

      // A write issued while the read is in flight fences it - and then fails,
      // publishing nothing. It must not keep what it fenced: otherwise the
      // read lands superseded, writes none of its three fields, and the flag
      // it raised stays up with nothing left to lower it.
      await expect(rejectWrite("write refused")).rejects.toThrow("write refused");

      read.land();
      await read.promise;
      expect(lifecycle.loading(store.getState())).toBe(false);
    });

    test("a reply from the connection that was replaced publishes nothing", async () => {
      const { fake, store, gatedRead, fence } = createLifecycleKit(lifecycle);
      store.connectionChanged(fake, "ready");
      const read = await gatedRead();

      // A different hub answers different questions, and this reply is the
      // previous one's answer: it describes a machine this store no longer
      // speaks to.
      fence();
      read.land();
      await read.promise;

      expect(lifecycle.list(store.getState())).toBeNull();
      // And the store is asking again: something wanted this list and the
      // replacement is where it has to come from now.
      expect(lifecycle.loading(store.getState())).toBe(true);
    });

    test("a read that landed fenced is applied when the write that fenced it retracts", async () => {
      const { store, gatedRead, gatedWrite } = createLifecycleKit(lifecycle);
      const read = await gatedRead();
      const write = await gatedWrite();

      // The read answers first and is superseded, so it publishes nothing -
      // the flag it raised belongs to the write now.
      read.land();
      await read.promise;
      expect(lifecycle.loading(store.getState())).toBe(true);

      // Then the write fails, publishing nothing and giving the state back.
      // The read's answer is the newest one anybody has, and it is the last
      // one there will be: nothing else is coming to lower that flag.
      write.fail();
      await expect(write.promise).rejects.toThrow("write refused");
      expect(lifecycle.loading(store.getState())).toBe(false);
      expect(lifecycle.list(store.getState())).not.toBeNull();
    });

    for (const [name, order] of [
      ["the older", [0, 1]],
      ["the newer", [1, 0]],
    ] as const) {
      test(`${name} of two failed writes failing first still lets the read it fenced land`, async () => {
        const { fake, store, gatedRead, gatedWrite } = createLifecycleKit(lifecycle);
        const read = await gatedRead();
        const first = await gatedWrite();
        const second = await gatedWrite();
        const writes = [first, second] as const;
        expect(callsTo(fake, lifecycle.mutationMethod)).toBe(2);

        // Whichever order the two writes fail in, neither published anything,
        // so neither may keep the read's answer from landing - and that answer
        // is the only thing left that can lower the flag the read raised.
        for (const index of order) writes[index].fail();
        await expect(first.promise).rejects.toThrow("write refused");
        await expect(second.promise).rejects.toThrow("write refused");

        read.land();
        await read.promise;

        expect(lifecycle.loading(store.getState())).toBe(false);
        expect(lifecycle.list(store.getState())).not.toBeNull();
      });
    }

    test("a notification from the client that was replaced schedules no read", async () => {
      const { fake, store, fence } = createLifecycleKit(lifecycle);
      store.connectionChanged(fake, "ready");
      answerRequests(fake, lifecycle.listMethod, lifecycle.listResponse);
      await lifecycle.fetch(store.getState());
      store.start();

      fence();
      const reads = callsTo(fake, lifecycle.listMethod);

      lifecycle.notifyUpdated(fake);
      await vi.advanceTimersByTimeAsync(lifecycle.debounceMs);
      expect(callsTo(fake, lifecycle.listMethod)).toBe(reads);
    });

    test("reset() leaves the store listening to the connection it still has", async () => {
      const { fake, store } = lifecycle.create();
      store.connectionChanged(fake, "ready");
      answerRequests(fake, lifecycle.listMethod, lifecycle.listResponse);
      await lifecycle.fetch(store.getState());
      store.start();

      // reset() forgets what the store read, not which connection it is on -
      // and the host has reported nothing since, so there is no connection to
      // compare a notification against. The client is still live and its
      // changes must still arrive.
      store.reset();
      const reads = callsTo(fake, lifecycle.listMethod);
      lifecycle.notifyUpdated(fake);
      await vi.advanceTimersByTimeAsync(lifecycle.debounceMs);
      expect(callsTo(fake, lifecycle.listMethod)).toBe(reads + 1);
    });

    test("a read a replacement interrupted is issued again", async () => {
      const { fake, store, gatedRead, fence } = createLifecycleKit(lifecycle);
      store.connectionChanged(fake, "ready");
      const read = await gatedRead();
      expect(lifecycle.loading(store.getState())).toBe(true);
      expect(callsTo(fake, lifecycle.listMethod)).toBe(1);

      // The replacement fences the read and settles the flag it raised, which
      // is round 9's half. This is the other half: something asked for this
      // list and never got it, and that intent outlives the connection the
      // asking happened on - the host's own loader is a one-shot and will not
      // ask again.
      answerRequests(fake, lifecycle.listMethod, lifecycle.listResponse);
      fence();
      await vi.advanceTimersByTimeAsync(0);

      expect(callsTo(fake, lifecycle.listMethod)).toBe(2);
      expect(lifecycle.list(store.getState())).not.toBeNull();
      expect(lifecycle.loading(store.getState())).toBe(false);
      void read.promise;
    });

    test("a replacement named before it is ready still gets the read it interrupted", async () => {
      const { fake, store, gatedRead, fence } = createLifecycleKit(lifecycle);
      store.connectionChanged(fake, "ready");
      const read = await gatedRead();
      expect(lifecycle.loading(store.getState())).toBe(true);
      answerRequests(fake, lifecycle.listMethod, lifecycle.listResponse);

      // What a retry actually looks like: the host names its fresh client
      // before dialling it, so the store hears about the replacement while it
      // is still idle. That call fences the read and settles the flag, so by
      // the time ready arrives the state carries no trace of the read - the
      // intent has to outlive the call that observed it.
      fence("idle", "ready");
      await vi.advanceTimersByTimeAsync(0);

      expect(callsTo(fake, lifecycle.listMethod)).toBe(2);
      expect(lifecycle.list(store.getState())).not.toBeNull();
      expect(lifecycle.loading(store.getState())).toBe(false);
      void read.promise;
    });

    test("a connection update that changes nothing leaves a scheduled read alone", async () => {
      const { fake, store } = lifecycle.create();
      store.connectionChanged(fake, "ready");
      answerRequests(fake, lifecycle.listMethod, lifecycle.listResponse);
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
      expect(callsTo(fake, lifecycle.listMethod)).toBe(2);
    });

    test("a reconnect after dispose() reads nothing", async () => {
      const { fake, store } = lifecycle.create();
      answerRequests(fake, lifecycle.listMethod, lifecycle.listResponse);
      await lifecycle.fetch(store.getState());

      store.dispose();
      store.connectionChanged(fake, "reconnecting");
      store.connectionChanged(fake, "ready");
      await vi.advanceTimersByTimeAsync(lifecycle.debounceMs);
      expect(callsTo(fake, lifecycle.listMethod)).toBe(1);
    });

    test("reset() returns to the initial state and fences the list still in flight", async () => {
      const { fake, store, gatedRead } = createLifecycleKit(lifecycle);
      const read = await gatedRead();

      store.reset();
      expect(store.getState()).toMatchObject(lifecycle.initial);

      read.land();
      await read.promise;
      expect(store.getState()).toMatchObject(lifecycle.initial);

      // The store keeps working after a reset.
      answerRequests(fake, lifecycle.listMethod, lifecycle.listResponse);
      await lifecycle.fetch(store.getState());
      expect(lifecycle.loading(store.getState())).toBe(false);
      expect(callsTo(fake, lifecycle.listMethod)).toBe(2);
    });
  });
}

runLifecycleSuite("marketplaces", MARKETPLACES);
runLifecycleSuite("plugins", PLUGINS);
runLifecycleSuite("launch layer", LAUNCH_LAYER);

// The optional revision: when a store hands the lifecycle its listRevision,
// the lifecycle - not the store - owns the hasLive OR into wantsList and the
// fence. Both halves are asserted here on a lifecycle of its own, because a
// store's options no longer reach either by hand.
describe("a lifecycle given a list revision", () => {
  beforeEach(() => {
    vi.useFakeTimers();
  });
  afterEach(() => {
    vi.useRealTimers();
  });

  test("reads a live request into wantsList and fences the revision with everything else", async () => {
    const revisions = createListRevision();
    const client = new FakeClient("ready");
    let reads = 0;
    // The store's OWN wantsList says no throughout: only the revision can make
    // it want the list.
    const lifecycle = createStoreLifecycle<{ marker: number }>(client, {
      method: "evener/plugin/updated",
      debounceMs: 250,
      store: () => store,
      refetch: () => {
        reads += 1;
      },
      revision: revisions,
      wantsList: () => false,
    });
    const store = createFrameworkFreeStore<{ marker: number }>((publish) => {
      void lifecycle.guard(publish);
      return { marker: 0 };
    });

    // A request on the wire issues a live revision but writes no state field.
    // The recovery read a ready connection schedules is what proves the
    // lifecycle saw it.
    revisions.next();
    lifecycle.connectionChanged(client, "reconnecting");
    lifecycle.connectionChanged(client, "ready");
    await vi.advanceTimersByTimeAsync(0);
    expect(reads).toBe(1);

    // And a fence settles that revision with everything else it cancels.
    revisions.next();
    lifecycle.reset();
    expect(revisions.hasLive()).toBe(false);
  });
});

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
      wantsList: () => true,
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
