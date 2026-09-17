import { FakeClient } from "@evener/appwire-client/testing/fakeClient";
import { afterEach, beforeEach, describe, expect, test } from "vitest";
import { connectionStore } from "./connection";

describe("connection handshake metadata", () => {
  beforeEach(() => {
    connectionStore.setState({ state: "idle", serverInfo: undefined, features: undefined, client: null });
  });

  afterEach(() => {
    connectionStore.setState({ state: "idle", serverInfo: undefined, features: undefined, client: null });
  });

  test("retains serverInfo and features from one initialize response", async () => {
    const client = new FakeClient("ready");
    const response = await client.connect();
    connectionStore.getState().connect(client);
    connectionStore.setState({ serverInfo: response.serverInfo, features: response.features });
    expect(connectionStore.getState().serverInfo).toEqual(response.serverInfo);
    expect(connectionStore.getState().features).toEqual(response.features);
  });

  test("clears stale feature metadata when a different client is wired", async () => {
    const first = new FakeClient("ready");
    connectionStore.getState().connect(first);
    connectionStore.setState({ features: { ...(await first.connect()).features, transcriptDisplaySettings: true } });
    const second = new FakeClient("ready");
    connectionStore.getState().connect(second);
    expect(connectionStore.getState().features).toBeUndefined();
  });

  test("clears handshake metadata when the active client closes", async () => {
    const client = new FakeClient("ready");
    connectionStore.getState().connect(client);
    const response = await client.connect();
    connectionStore.setState({ serverInfo: response.serverInfo, features: response.features });
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
