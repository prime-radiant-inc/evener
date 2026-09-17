import { describe, expect, test, vi } from "vitest";
import { FakeClient } from "../../testing/fakeClient";
import { createConnectionStore, onConnectionNotification } from "./core";

// Wraps a FakeClient's onStateChange so a test can count registrations and
// detachments without reaching into the fake's private handler set - the
// only way to prove a stale client's listener was actually removed, rather
// than merely rendered harmless by the identity check that guards it too.
function trackStateChangeWiring(client: FakeClient): { registrations: number; detachments: number } {
  const tracking = { registrations: 0, detachments: 0 };
  const original = client.onStateChange.bind(client);
  client.onStateChange = (cb) => {
    tracking.registrations += 1;
    const unwire = original(cb);
    return () => {
      tracking.detachments += 1;
      unwire();
    };
  };
  return tracking;
}

describe("createConnectionStore", () => {
  test("connect is a sibling of the store, not a state key", () => {
    const store = createConnectionStore();
    expect("connect" in store.getState()).toBe(false);
    expect(typeof store.connect).toBe("function");
  });

  test("connect() wires the client and mirrors its current state", () => {
    const store = createConnectionStore();
    const client = new FakeClient("ready");
    store.connect(client);
    expect(store.getState().client).toBe(client);
    expect(store.getState().state).toBe("ready");
    client.emitStateChange("reconnecting");
    expect(store.getState().state).toBe("reconnecting");
  });

  test("connect() is a call to the same observable setState, not a bypass of it", () => {
    const store = createConnectionStore();
    const client = new FakeClient("ready");
    const setStateSpy = vi.spyOn(store, "setState");
    store.connect(client);
    expect(setStateSpy).toHaveBeenCalledTimes(1);
    expect(setStateSpy).toHaveBeenCalledWith({ client });
  });

  test("calling connect() again with the same client instance no-ops", () => {
    const store = createConnectionStore();
    const client = new FakeClient("ready");
    const tracking = trackStateChangeWiring(client);
    store.connect(client);
    store.connect(client);
    expect(tracking.registrations).toBe(1);
  });

  test("calling connect() again with the same client instance does not notify subscribers", () => {
    const store = createConnectionStore();
    const client = new FakeClient("ready");
    store.connect(client);
    let notifications = 0;
    const stop = store.subscribe(() => {
      notifications += 1;
    });
    store.connect(client);
    stop();
    expect(notifications).toBe(0);
  });

  test("a client-changing write re-checks the client against a fresh read, not the snapshot the updater started from", () => {
    const store = createConnectionStore();
    const a = new FakeClient("ready");
    const b = new FakeClient("ready");
    const aTracking = trackStateChangeWiring(a);
    const bTracking = trackStateChangeWiring(b);
    store.connect(a);

    // The updater synchronously wires b (a nested setState publishes it for
    // real) and then returns the client its OWN parameter named - `a`, the
    // snapshot taken before it ran, not what the store now actually holds.
    // Deciding "did client change" against that stale snapshot would agree
    // with the updater's own return value and skip rewiring, leaving the
    // store reporting `a` while only b's listener is attached.
    store.setState((state) => {
      store.connect(b);
      return { client: state.client };
    });

    expect(store.getState().client).toBe(a);
    // a's first listener (from the top-level connect) was detached by the
    // nested connect(b); this write's own correction re-registers a and
    // retires b, so exactly one listener - a's second - ends up live.
    expect(aTracking.registrations).toBe(2);
    expect(aTracking.detachments).toBe(1);
    expect(bTracking.registrations).toBe(1);
    expect(bTracking.detachments).toBe(1);

    // Proof by live event: only the published client's listener can reach
    // the store.
    a.emitStateChange("closed");
    expect(store.getState().state).toBe("closed");
  });

  test("detaches the stale client's state-change listener when a different client is wired", () => {
    const store = createConnectionStore();
    const first = new FakeClient("ready");
    const second = new FakeClient("ready");
    const firstTracking = trackStateChangeWiring(first);

    store.connect(first);
    expect(firstTracking.registrations).toBe(1);

    store.connect(second);
    expect(firstTracking.detachments).toBe(1);
    expect(store.getState().client).toBe(second);

    // first's own transitions no longer reach the store - its listener is
    // gone, not merely guarded by the identity check.
    first.emitStateChange("closed");
    expect(store.getState().client).toBe(second);
    expect(store.getState().state).toBe("ready");
  });

  test("a re-entrant connect() during a state-change dispatch does not leak or double-wire", () => {
    const store = createConnectionStore();
    const a = new FakeClient("ready");
    const b = new FakeClient("ready");
    const aTracking = trackStateChangeWiring(a);
    const bTracking = trackStateChangeWiring(b);

    // A store subscriber (any consumer of this instance's subscribe(), the
    // way a host's other stores react to a connection-store swap) that
    // reconnects to a different client the instant it sees `a` wired -
    // synchronously, inside connect(a)'s own setState dispatch.
    const unsubscribe = store.subscribe((state, previous) => {
      if (state.client === a && previous.client !== a) {
        store.connect(b);
      }
    });

    store.connect(a);
    unsubscribe();

    // b's connect() ran to completion first and owns the slot; a's frame,
    // resuming afterward, must retire its own listener rather than clobber
    // b's registration.
    expect(store.getState().client).toBe(b);
    expect(aTracking.registrations).toBe(1);
    expect(aTracking.detachments).toBe(1);
    expect(bTracking.registrations).toBe(1);
    expect(bTracking.detachments).toBe(0);

    // a is fully detached: its later transitions cannot resurrect stale state.
    a.emitStateChange("closed");
    expect(store.getState().client).toBe(b);
    expect(store.getState().state).toBe("ready");
  });

  test("a finite A -> B -> A re-entrant swap cycle leaves exactly one live listener on A", () => {
    const store = createConnectionStore();
    const a = new FakeClient("ready");
    const b = new FakeClient("ready");
    const aTracking = trackStateChangeWiring(a);
    const bTracking = trackStateChangeWiring(b);

    // The instant A is wired (synchronously, inside connect(a)'s own
    // setState dispatch), a subscriber swaps to B and then immediately back
    // to A - both re-entrant calls completing before connect(a)'s own frame
    // resumes. `reentered` bounds this to firing once: without it, the
    // second connect(a) below would trigger this same branch again forever.
    let reentered = false;
    const unsubscribe = store.subscribe((state, previous) => {
      if (!reentered && state.client === a && previous.client !== a) {
        reentered = true;
        store.connect(b);
        store.connect(a);
      }
    });

    store.connect(a);
    unsubscribe();

    expect(store.getState().client).toBe(a);
    // Two connect(a) calls ran (the outer one and the re-entrant second one),
    // so two listeners were registered; only the second is live - the
    // outer's own frame, resuming after both re-entrant calls completed,
    // must retire its now-stale listener rather than overwrite the tracked
    // disposer with it.
    expect(aTracking.registrations).toBe(2);
    expect(aTracking.detachments).toBe(1);
    expect(bTracking.registrations).toBe(1);
    expect(bTracking.detachments).toBe(1);

    // Exactly one live listener on A: one emitted state change publishes
    // exactly once. A leaked outer listener would double-publish here.
    let publishes = 0;
    const stopCounting = store.subscribe(() => {
      publishes += 1;
    });
    a.emitStateChange("closed");
    stopCounting();
    expect(publishes).toBe(1);
  });

  test("connect() clears handshake metadata when swapping clients or closing", () => {
    const store = createConnectionStore();
    const first = new FakeClient("ready");
    store.connect(first);
    store.setState({ serverInfo: { name: "hub", version: "1.0.0" }, features: undefined });

    const second = new FakeClient("ready");
    store.connect(second);
    expect(store.getState().serverInfo).toBeUndefined();

    store.setState({ serverInfo: { name: "hub", version: "1.0.0" }, features: undefined });
    second.emitStateChange("closed");
    expect(store.getState().serverInfo).toBeUndefined();
  });

  test("a client swapped in through setState directly (bypassing connect()) still gets its own listener", () => {
    const store = createConnectionStore();
    const first = new FakeClient("ready");
    const firstTracking = trackStateChangeWiring(first);
    store.connect(first);

    // setState is the same write connect() makes, so a caller that replaces
    // `client` through it directly is wired exactly the same way.
    const second = new FakeClient("ready");
    store.setState({ client: second, state: second.state });
    expect(firstTracking.detachments).toBe(1);

    // (a) the new client's own transitions must reach the store - nothing
    // else will ever wire a listener to it otherwise.
    second.emitStateChange("closed");
    expect(store.getState().state).toBe("closed");

    // (b) first is detached, not merely guarded: its later transitions
    // cannot resurrect stale state. The identity check inside its own
    // listener closure is the backstop for the dispatch-in-flight case
    // (see the module comment), not the reason this passes.
    first.emitStateChange("reconnecting");
    expect(store.getState().state).toBe("closed");
  });

  test("a client swap through setState defaults state to the incoming client's own state and clears stale metadata", () => {
    const store = createConnectionStore();
    const first = new FakeClient("ready");
    store.connect(first);
    store.setState({ serverInfo: { name: "hub", version: "1.0.0" } });

    const second = new FakeClient("connecting");
    store.setState({ client: second });

    expect(store.getState().state).toBe("connecting");
    expect(store.getState().serverInfo).toBeUndefined();
  });

  test("a client swap through setState still honors state/serverInfo the same partial names", () => {
    const store = createConnectionStore();
    const client = new FakeClient("connecting");
    store.setState({ client, state: "ready", serverInfo: { name: "hub", version: "1.0.0" } });

    expect(store.getState().state).toBe("ready");
    expect(store.getState().serverInfo).toEqual({ name: "hub", version: "1.0.0" });
  });

  test("clearing the client to null through setState detaches the currently wired listener and resets to idle", () => {
    const store = createConnectionStore();
    const client = new FakeClient("ready");
    const tracking = trackStateChangeWiring(client);

    store.connect(client);
    expect(tracking.registrations).toBe(1);
    expect(tracking.detachments).toBe(0);

    store.setState({ client: null });
    expect(tracking.detachments).toBe(1);
    expect(store.getState().state).toBe("idle");

    // Detached, not merely guarded: the listener itself is gone, so a later
    // transition on the cleared client cannot even attempt to publish.
    client.emitStateChange("closed");
    expect(store.getState().client).toBeNull();
    expect(store.getState().state).toBe("idle");
  });

  test("a client written as undefined is stored as null, never left as undefined", () => {
    const store = createConnectionStore();
    const client = new FakeClient("ready");
    store.connect(client);

    store.setState({ client: undefined as unknown as null });
    expect(store.getState().client).toBeNull();
  });
});

describe("onConnectionNotification", () => {
  test("follows whichever client the store holds, detaching a replaced client", () => {
    const store = createConnectionStore();
    const first = new FakeClient("ready");
    const second = new FakeClient("ready");
    const seen: unknown[] = [];

    const stop = onConnectionNotification(store, (n) => seen.push(n));

    store.connect(first);
    first.emitNotification({ method: "evener/plugin/updated", params: {} });
    expect(seen).toHaveLength(1);

    store.connect(second);
    first.emitNotification({ method: "evener/plugin/updated", params: {} });
    expect(seen).toHaveLength(1); // first is detached; its notification is dropped

    second.emitNotification({ method: "evener/plugin/updated", params: {} });
    expect(seen).toHaveLength(2);

    stop();
    second.emitNotification({ method: "evener/plugin/updated", params: {} });
    expect(seen).toHaveLength(2);
  });

  test("attaches immediately to a client the store already holds", () => {
    const store = createConnectionStore();
    const client = new FakeClient("ready");
    store.connect(client);

    const seen: unknown[] = [];
    onConnectionNotification(store, (n) => seen.push(n));
    client.emitNotification({ method: "evener/plugin/updated", params: {} });
    expect(seen).toHaveLength(1);
  });

  test("clearing the store's client to null detaches the currently wired listener", () => {
    const store = createConnectionStore();
    const client = new FakeClient("ready");
    store.connect(client);

    const seen: unknown[] = [];
    onConnectionNotification(store, (n) => seen.push(n));
    client.emitNotification({ method: "evener/plugin/updated", params: {} });
    expect(seen).toHaveLength(1);

    store.setState({ client: null });
    client.emitNotification({ method: "evener/plugin/updated", params: {} });
    // Still detached: a client that outlives the store's own reference to it
    // (a disconnect, a test reset) must not keep delivering notifications.
    expect(seen).toHaveLength(1);
  });
});
