// Shared connection-store fixtures for web tests: one way to wire a FakeClient
// into the connection store, so no test re-declares it.
import type { ConnectionState } from "@evener/appwire-client";
import { FakeClient } from "@evener/appwire-client/testing/fakeClient";
import { connectionStore } from "../stores/connection";

/** A fresh FakeClient in `state`, connected through the connection store's
 * locked connect(client) entry point, the same path threads.ts's requireClient()
 * rides to reach the client. `configure` registers request handlers before the
 * connect, for tests whose stores issue requests during the handshake. */
export function connectFakeClient(
  state: ConnectionState = "ready",
  configure?: (fake: FakeClient) => void,
): FakeClient {
  const fake = new FakeClient(state);
  configure?.(fake);
  connectionStore.getState().connect(fake);
  return fake;
}

/** connectFakeClient in the "ready" state. */
export function connectReadyFakeClient(configure?: (fake: FakeClient) => void): FakeClient {
  return connectFakeClient("ready", configure);
}
