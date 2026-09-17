import { describe, expect, test } from "vitest";
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

  test("calling connect() again with the same client instance no-ops", () => {
    const store = createConnectionStore();
    const client = new FakeClient("ready");
    const tracking = trackStateChangeWiring(client);
    store.connect(client);
    store.connect(client);
    expect(tracking.registrations).toBe(1);
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
});
