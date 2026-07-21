// connection.ts owns the single AppwireClientLike the app is connected
// through: it mirrors the client's ConnectionState reactively for the UI,
// and holds the client reference other stores (threads.ts) ride, since only
// this store's connect() ever receives one.
import { createStore } from "zustand/vanilla";
import { useStore } from "zustand";
import type { ConnectionState } from "../protocol/client";
import type { ServerInfo } from "../protocol/types.gen";
import type { AppwireClientLike } from "../protocol/testing/fakeClient";

export interface ConnectionStoreState {
  state: ConnectionState;
  serverInfo?: ServerInfo;
  // The wired client, for other stores (threads.ts) to ride. Not part of
  // Task 7's locked shape, but the only way a store without its own
  // connect() (threads.ts has none) can reach the client at all.
  client: AppwireClientLike | null;
  // connect wires this store's `state` to the client's own ConnectionState
  // transitions, capturing whatever state the client is already in.
  // Idempotent: calling it again with the same client instance no-ops,
  // rather than attaching a second onStateChange listener.
  //
  // serverInfo is part of the locked shape but has no path to a value here:
  // AppwireClientLike exposes no way to read back the InitializeResponse a
  // successful handshake already produced (AppwireClient doesn't retain it
  // beyond the resolved connect() promise), and re-requesting "initialize"
  // to get it again is rejected server-side once a connection is already
  // initialized (internal/appserver/server.go: "already initialized"). It
  // stays undefined here; populating it needs a path this task's locked
  // interface doesn't provide — e.g. whoever owns the real client.connect()
  // promise setting it directly via connectionStore.setState({serverInfo}).
  connect: (client: AppwireClientLike) => void;
}

export const connectionStore = createStore<ConnectionStoreState>(() => ({
  state: "idle",
  serverInfo: undefined,
  client: null,
  connect: (client) => {
    if (connectionStore.getState().client === client) return;
    connectionStore.setState({ client, state: client.state });
    client.onStateChange((s) => connectionStore.setState({ state: s }));
  },
}));

export function useConnectionStore(): ConnectionStoreState;
export function useConnectionStore<T>(selector: (state: ConnectionStoreState) => T): T;
export function useConnectionStore<T>(selector?: (state: ConnectionStoreState) => T): T | ConnectionStoreState {
  return selector ? useStore(connectionStore, selector) : useStore(connectionStore);
}
