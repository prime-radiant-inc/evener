// connection.ts owns the single AppwireClientLike the app is connected
// through: it mirrors the client's ConnectionState reactively for the UI,
// and holds the client reference other stores (threads.ts) ride, since only
// this store's connect() ever receives one.

import { useStore } from "zustand";
import { createStore } from "zustand/vanilla";
import type { ConnectionState } from "../protocol/client";
import type { AppwireClientLike } from "../protocol/testing/fakeClient";
import type { ServerInfo } from "../protocol/types.gen";

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
  // serverInfo is part of the locked shape, but this function never
  // populates it: AppwireClientLike DOES expose a connect() that resolves
  // with the InitializeResponse (protocol/testing/fakeClient.ts) - but
  // connect(client) here only mirrors ConnectionState, so it stays safe to
  // call before any handshake has even started (a real client is typically
  // still "idle" the moment a caller wires it in). Instead, each caller that
  // actually drives a handshake (AppShell.tsx's initial boot;
  // ConnectionBanner.tsx's manual retry) sets serverInfo itself, directly,
  // via connectionStore.setState({serverInfo}) once its own client.connect()
  // promise resolves. Re-requesting "initialize" a second time to get the
  // same value some other way is rejected server-side once a connection is
  // already initialized (internal/appserver/server.go: "already
  // initialized"), so there isn't a second path for this function to use
  // instead.
  connect: (client: AppwireClientLike) => void;
}

// unwireStateChange detaches the outgoing client's connection-state listener
// when a replacement is wired in. Without it the old client keeps a live
// subscription for the rest of the page's life, and its eventual "closed"
// overwrites the state of a client that is perfectly healthy — the banner
// reports a dead connection while requests keep succeeding.
//
// Detaching is only the cooperative half of the fence. A client may already
// have captured the callback into an in-flight dispatch, where unsubscribing
// cannot un-invoke it, so the callback below re-checks ownership before it
// publishes. Either half alone leaves a window; both together do not.
//
// This is connection-state listener ownership only. It is deliberately
// separate from the notification/ready listener ownership threads.ts manages:
// those re-subscribe per ref and per ready generation and are torn down when a
// ref is released, whereas this is one connection-wide mirror that lives
// exactly as long as its client is the wired one.
let unwireStateChange: (() => void) | null = null;

export const connectionStore = createStore<ConnectionStoreState>(() => ({
  state: "idle",
  serverInfo: undefined,
  client: null,
  connect: (client) => {
    if (connectionStore.getState().client === client) return;
    unwireStateChange?.();
    unwireStateChange = null;
    connectionStore.setState({ client, state: client.state });
    unwireStateChange = client.onStateChange((s) => {
      if (connectionStore.getState().client !== client) return;
      connectionStore.setState({ state: s });
    });
  },
}));

export function useConnectionStore(): ConnectionStoreState;
export function useConnectionStore<T>(selector: (state: ConnectionStoreState) => T): T;
export function useConnectionStore<T>(selector?: (state: ConnectionStoreState) => T): T | ConnectionStoreState {
  // Not actually a conditional hook call: zustand's own useStore is
  // `function useStore(api, selector = identity)` (node_modules/zustand/
  // esm/react.mjs) - a JS default parameter, not internal branching - so
  // both ternary arms run the exact same useSyncExternalStore/useCallback
  // sequence regardless of which one a given render takes. TypeScript's
  // overloads for useStore don't have a variant accepting a possibly-
  // undefined selector, which is the only reason this is two call sites
  // instead of one (see this same pattern + comment in stores/threads.ts,
  // stores/tree.ts, shell/workspace.ts).
  // biome-ignore lint/correctness/useHookAtTopLevel: same hook both arms, JS default param not a real conditional - see above
  return selector ? useStore(connectionStore, selector) : useStore(connectionStore);
}
