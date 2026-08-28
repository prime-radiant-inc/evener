import { afterEach, beforeEach, describe, expect, test } from "vitest";
import { FakeClient } from "../protocol/testing/fakeClient";
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
