// The connection lifecycle's client-swap safety, framework-free: which
// AppwireClientLike a host is wired to right now, the ConnectionState mirror
// that follows it, and onConnectionNotification, which follows whichever
// client the store holds across a swap. serverInfo/features are plain
// settable fields the host writes after its own handshake read (the
// InitializeResponse) - this module only knows client identity and wire
// state, never the handshake itself.
//
// No React, no zustand, no DOM. Each app binds its view layer to the
// getState/setState/subscribe triple.
import type { ConnectionState } from "../../client";
import type { AppwireClientLike } from "../../clientLike";
import { createFrameworkFreeStore, type FrameworkFreeStore } from "../../frameworkFreeStore";
import type { AnyNotification, FeatureSet, ServerInfo } from "../../types.gen";

export interface ConnectionStoreState {
  state: ConnectionState;
  serverInfo?: ServerInfo;
  features?: FeatureSet;
  // The wired client, for other stores (the web's threads.ts) to ride. The
  // only way a store without its own connect() can reach the client at all.
  client: AppwireClientLike | null;
  // connect wires this store's `state` to the client's own ConnectionState
  // transitions, capturing whatever state the client is already in.
  // Idempotent: calling it again with the same client instance no-ops,
  // rather than attaching a second onStateChange listener.
  //
  // Handshake metadata is populated by the caller that drives connect(), from
  // that one InitializeResponse. This function only mirrors client state and
  // deliberately remains safe to call before a handshake has started.
  connect: (client: AppwireClientLike) => void;
}

export type ConnectionStore = FrameworkFreeStore<ConnectionStoreState>;

// createConnectionStore builds one instance's client-swap safety. Each host
// (an app, a test) gets its own `unwireStateChange`, so instances never share
// wiring state.
export function createConnectionStore(): ConnectionStore {
  // unwireStateChange detaches the outgoing client's connection-state
  // listener when a replacement is wired in. Without it the old client keeps
  // a live subscription for the rest of the store's life, and its eventual
  // "closed" overwrites the state of a client that is perfectly healthy -
  // the banner reports a dead connection while requests keep succeeding.
  //
  // Detaching is only the cooperative half of the fence, and it is genuinely
  // insufficient: AppwireClient.setState dispatches over a snapshot of its
  // handler set (protocol/client.ts), so a handler unsubscribed mid-dispatch
  // is still invoked for that dispatch. The callback therefore re-checks that
  // it is still the store's client before publishing.
  //
  // The two halves cover different failures rather than backing each other
  // up. The identity check is what keeps state correct; the detach is what
  // keeps replaced clients from accumulating live subscriptions. Neither
  // substitutes for the other, and each is covered by its own test
  // (core.test.ts).
  //
  // This is connection-state listener ownership only. It is deliberately
  // separate from the notification/ready listener ownership a host's other
  // stores manage: those re-subscribe per ref and per ready generation and
  // are torn down when a ref is released, whereas this is one connection-wide
  // mirror that lives exactly as long as its client is the wired one.
  let unwireStateChange: (() => void) | null = null;

  const store = createFrameworkFreeStore<ConnectionStoreState>(() => ({
    state: "idle",
    serverInfo: undefined,
    features: undefined,
    client: null,
    connect: (client) => {
      if (store.getState().client === client) return;
      unwireStateChange?.();
      unwireStateChange = null;
      // Register before publishing. setState dispatches to subscribers
      // synchronously, and the real client transitions synchronously too
      // (AppwireClient.connect enters "connecting", close() enters "closed",
      // both without awaiting), so publishing first leaves a window where a
      // transition has no listener and is lost until the client's next one.
      const unwire = client.onStateChange((s) => {
        if (store.getState().client !== client) return;
        store.setState(s === "closed" ? { state: s, serverInfo: undefined, features: undefined } : { state: s });
      });
      // Read client.state here, not before registering: a transition that
      // landed during registration is already reflected in it, and the
      // callback above could not have published it while this client was
      // still not the store's.
      store.setState({ client, state: client.state, serverInfo: undefined, features: undefined });
      // The synchronous dispatch above can re-enter connect() with a
      // different client. That frame completed and owns the slot, so this
      // one is stale: retire its own listener instead of clobbering the
      // newer entry, which would leak the newer client's subscription for
      // the life of the store.
      if (store.getState().client === client) {
        unwireStateChange = unwire;
      } else {
        unwire();
      }
    },
  }));
  return store;
}

// Subscribes `handler` to the client `store` holds now and to every client it
// wires later - a caller that loads before the host's own connect() effect
// has no client to read once, so it reacts to the store instead. A replaced
// client is detached so it does not keep a live subscription for the rest of
// the store's life. Returns the disposer for both halves.
export function onConnectionNotification(
  store: ConnectionStore,
  handler: (n: AnyNotification) => void,
): () => void {
  let wired: AppwireClientLike | null = null;
  let unwire: (() => void) | undefined;
  const attach = (client: AppwireClientLike | null): void => {
    if (!client || client === wired) return; // already wired to this exact client
    unwire?.();
    wired = client;
    unwire = client.onNotification(handler);
  };
  const unsubscribe = store.subscribe((state) => attach(state.client));
  attach(store.getState().client);
  return () => {
    unsubscribe();
    unwire?.();
  };
}
