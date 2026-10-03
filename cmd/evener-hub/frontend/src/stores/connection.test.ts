// @vitest-environment node

import { answerRequests, callsTo, FakeClient } from "@evener/appwire-client/testing/fakeClient";
import { afterEach, beforeEach, describe, expect, test } from "vitest";
import {
  type ConnectionStoreState,
  connectedClientPort,
  connectionStore,
  readyConnectionTransition,
} from "./connection";

describe("connection handshake metadata", () => {
  beforeEach(() => {
    connectionStore.setState({ state: "idle", serverInfo: undefined, features: undefined, client: null });
  });

  afterEach(() => {
    connectionStore.setState({ state: "idle", serverInfo: undefined, features: undefined, client: null });
  });

  test("attaches with serverInfo and features from the cached initialize response", async () => {
    const client = new FakeClient("ready");
    const response = await client.connect();
    connectionStore.getState().connect(client);
    expect(connectionStore.getState().serverInfo).toEqual(response.serverInfo);
    expect(connectionStore.getState().features).toEqual(response.features);
  });

  test("replaces stale feature metadata with an already-ready client's cached result", async () => {
    const first = new FakeClient("ready");
    connectionStore.getState().connect(first);
    connectionStore.setState({ features: { ...(await first.connect()).features, transcriptDisplaySettings: true } });
    const second = new FakeClient("ready");
    connectionStore.getState().connect(second);
    expect(connectionStore.getState().features).toEqual(second.initializeResult?.features);
    expect(connectionStore.getState().features?.transcriptDisplaySettings).toBeUndefined();
  });

  test("clears handshake metadata when the active client closes", async () => {
    const client = new FakeClient("ready");
    connectionStore.getState().connect(client);
    expect(connectionStore.getState().features).toEqual((await client.connect()).features);
    client.emitStateChange("closed");
    expect(connectionStore.getState().serverInfo).toBeUndefined();
    expect(connectionStore.getState().features).toBeUndefined();
  });
});

describe("connectionStore.setState", () => {
  afterEach(() => {
    connectionStore.setState({ state: "idle", serverInfo: undefined, features: undefined, client: null });
  });

  test("an updater callback receives connect, exactly like a direct getState() read", () => {
    const client = new FakeClient("ready");
    connectionStore.getState().connect(client);
    let sawConnect: unknown;
    connectionStore.setState((state) => {
      sawConnect = state.connect;
      return { serverInfo: { name: "hub", version: "1.0.0" } };
    });
    expect(sawConnect).toBe(connectionStore.getState().connect);
    expect(connectionStore.getState().serverInfo).toEqual({ name: "hub", version: "1.0.0" });
  });

  test("connect is never written into the core's own state, even if an updater's return value includes it", () => {
    connectionStore.setState((state) => ({ connect: state.connect, serverInfo: { name: "hub", version: "1.0.0" } }));
    // If `connect` had actually reached the core's state, it would still be
    // there (nothing else touches this key) - the assertion that matters is
    // that the field, present or not, is always the live method, since a
    // stale copy pulled off one snapshot must never mask it.
    const client = new FakeClient("ready");
    connectionStore.getState().connect(client);
    expect(connectionStore.getState().client).toBe(client);
  });
});

describe("connectedClientPort", () => {
  const reset = () =>
    connectionStore.setState({ state: "idle", serverInfo: undefined, features: undefined, client: null });
  beforeEach(reset);
  afterEach(reset);

  test("requireClient names the calling store when no client is wired", () => {
    const port = connectedClientPort("widget");
    // The recovery instruction must name something callable: `connectionStore`
    // has `getState().connect`, while `useConnectionStore` is a bare hook
    // function with no `getState` at all.
    expect(() => port.requireClient()).toThrow(
      "widget store: no client connected; call connectionStore.getState().connect(client) first",
    );
  });

  test("request rejects and onNotification throws before a client is wired", async () => {
    const port = connectedClientPort("widget");
    await expect(port.request("thread/resume", { ref: "r" })).rejects.toThrow(/no client connected/);
    expect(() => port.onNotification(() => {})).toThrow(/no client connected/);
  });

  test("resolves the client connectionStore holds now, on every call", () => {
    const port = connectedClientPort("widget");
    const first = new FakeClient("ready");
    connectionStore.getState().connect(first);
    expect(port.requireClient()).toBe(first);
    // A reconnect swaps in a fresh client; the port must follow it rather than
    // keep the one captured at call time.
    const second = new FakeClient("ready");
    connectionStore.getState().connect(second);
    expect(port.requireClient()).toBe(second);
  });

  test("request forwards to the wired client", async () => {
    const port = connectedClientPort("widget");
    const client = new FakeClient("ready");
    answerRequests(client, "thread/resume", { ref: "r" });
    connectionStore.getState().connect(client);
    await expect(port.request("thread/resume", { ref: "r" })).resolves.toEqual({ ref: "r" });
    expect(callsTo(client, "thread/resume")).toBe(1);
  });

  test("onNotification follows the wired client and returns a working unsubscribe", () => {
    const port = connectedClientPort("widget");
    const client = new FakeClient("ready");
    connectionStore.getState().connect(client);
    const seen: string[] = [];
    const unwire = port.onNotification((n) => seen.push(n.method));
    client.emitUnknownNotification({ method: "x/unknown", params: {} });
    expect(seen).toEqual(["x/unknown"]);
    unwire();
    client.emitUnknownNotification({ method: "x/unknown", params: {} });
    expect(seen).toEqual(["x/unknown"]);
  });
});

describe("readyConnectionTransition", () => {
  beforeEach(() => {
    connectionStore.setState({ state: "idle", serverInfo: undefined, features: undefined, client: null });
  });

  afterEach(() => {
    connectionStore.setState({ state: "idle", serverInfo: undefined, features: undefined, client: null });
  });

  test("fires on first connect, recovery, and replacement; not on metadata republishes or clearing", () => {
    const seen: Array<[ConnectionStoreState, ConnectionStoreState]> = [];
    const unsubscribe = connectionStore.subscribe((state, previous) => {
      seen.push([state, previous]);
    });
    try {
      const first = new FakeClient("ready");
      connectionStore.getState().connect(first);
      connectionStore.setState({ serverInfo: { name: "fake", version: "1" } });
      first.emitStateChange("reconnecting");
      first.emitStateChange("ready");
      connectionStore.getState().connect(new FakeClient("ready"));
      connectionStore.setState({ client: null });
      expect(seen.map(([state, previous]) => readyConnectionTransition(state, previous))).toEqual([
        true, // first connect
        false, // metadata republish over the same ready client
        false, // leaving ready
        true, // recovery back to ready
        true, // a different client wired while ready
        false, // client cleared
      ]);
    } finally {
      unsubscribe();
    }
  });
});
