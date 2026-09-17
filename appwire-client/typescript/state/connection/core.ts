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
}

// connect is a sibling of the triple, not a state key (state/navigation/store.ts's
// `init`/`awaitConvergence`/`reset`, state/credentials/instances.ts's
// `connectionChanged` are the same shape): a partial setState can only ever
// replace state keys, so keeping connect out of state is what makes it
// impossible for one to accidentally clobber the other.
export interface ConnectionStore extends FrameworkFreeStore<ConnectionStoreState> {
  // connect wires this store's `state` to the client's own ConnectionState
  // transitions, capturing whatever state the client is already in.
  // Idempotent: calling it again with the same client instance no-ops,
  // rather than attaching a second onStateChange listener.
  //
  // Handshake metadata is populated by the caller that drives connect(), from
  // that one InitializeResponse. This function only mirrors client state and
  // deliberately remains safe to call before a handshake has started.
  connect(client: AppwireClientLike): void;
}

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
  // Bumped on every client wiring - connect() or a setState-driven swap,
  // both go through wireClient below - and captured as that call's own
  // generation. "Am I still the frame that owns unwireStateChange" cannot be
  // answered by comparing `client` identity alone: a finite re-entrant swap
  // cycle (connect(a) re-enters with connect(b), which re-enters back with
  // connect(a)) has an OUTER and an INNER frame that both target the same
  // client object, so the outer frame's post-setState identity check passes
  // even though the inner frame is the one that actually owns the slot -
  // measured on round 1's own reentrancy test, which happened to never
  // revisit a client and so never exercised this. The pre-D28 web store
  // (stores/connection.ts before this migration) carried the same identity-
  // only check and had the identical hole; this generation counter is new,
  // not an extension of anything that existed before this round.
  let connectGeneration = 0;

  // Attached onto the triple in place rather than `{ ...store, connect }`
  // (the spread every other core here uses, state/navigation/store.ts and
  // state/credentials/instances.ts included): a spread returns a NEW object,
  // so a caller instrumenting the returned store's `setState` (a spy, a
  // wrapping adapter) would never see a call this closure makes through its
  // own `store.setState` - two different objects that happen to start out
  // holding the same functions. Mutating `store` and returning that same
  // object keeps setState's identity exactly what every subscriber sees.
  const store = createFrameworkFreeStore<ConnectionStoreState>(() => ({
    state: "idle",
    serverInfo: undefined,
    features: undefined,
    client: null,
  })) as ConnectionStore;

  // Captured before `store.setState` is replaced below, and used only for
  // the two writes that must never re-enter that replacement: the listener's
  // own state-change publish would otherwise detect its own `client`-less
  // partial (harmless) but a client-carrying publish would detect itself and
  // recurse. The listener callback still calls the live `store.setState`
  // (round 2's fix, so a spy on it keeps seeing every state change) - only
  // wireClient's own client+related-fields write below bypasses it.
  const rawSetState = store.setState.bind(store);

  // wireClient is the one place a client transition is wired, whatever
  // triggers it - connect() or a caller replacing `client` directly through
  // setState (round 3 finding: `pendingTurnsStore.setState({ client, ... })`-
  // shaped calls, the pattern several existing test files already use, never
  // went through connect() at all, so the new client's own transitions never
  // reached the store). `publish` is the caller's own write of `client` plus
  // whatever else changed alongside it; wireClient's job around it is
  // exactly connect()'s old job: detach the outgoing listener, wire the
  // incoming one before publishing, and use the shared generation counter -
  // not a second one - to decide, after publishing (which can re-enter),
  // whether this frame still owns the slot.
  function wireClient(client: AppwireClientLike | null, publish: () => void): void {
    const generation = ++connectGeneration;
    unwireStateChange?.();
    unwireStateChange = null;
    // Register before publishing. setState dispatches to subscribers
    // synchronously, and the real client transitions synchronously too
    // (AppwireClient.connect enters "connecting", close() enters "closed",
    // both without awaiting), so publishing first leaves a window where a
    // transition has no listener and is lost until the client's next one.
    const unwire = client
      ? client.onStateChange((s) => {
          if (store.getState().client !== client) return;
          store.setState(s === "closed" ? { state: s, serverInfo: undefined, features: undefined } : { state: s });
        })
      : undefined;
    publish();
    // The synchronous publish above can re-enter wireClient (through
    // connect() or through setState) one or more times. Whichever frame ran
    // last owns the slot; every other frame - including this one, if a later
    // frame already ran and returned - must retire its own listener instead
    // of touching unwireStateChange. Client identity alone cannot tell "no
    // one has touched this since me" apart from "someone touched it and it
    // happens to match me again" (a re-entrant A -> B -> A cycle targets A
    // twice, as two different frames), so the generation counter is compared
    // instead: only the frame whose generation is still the latest one
    // issued may claim the slot.
    if (connectGeneration === generation) {
      if (unwire) unwireStateChange = unwire;
    } else {
      unwire?.();
    }
  }

  function connect(client: AppwireClientLike): void {
    if (store.getState().client === client) return;
    // Read client.state here, not before wireClient registers its listener:
    // a transition that landed during registration is already reflected in
    // it, and the listener could not have published it while this client was
    // still not the store's.
    wireClient(client, () => rawSetState({ client, state: client.state, serverInfo: undefined, features: undefined }));
  }

  // Replaces the triple's own setState so a caller that swaps or clears
  // `client` directly - not through connect() - gets the identical wiring:
  // without this, `store.setState({ client: fresh, ... })` changed who the
  // store SAYS is wired without ever subscribing to that client's own
  // transitions, so nothing published again until someone happened to call
  // connect() too. Every other partial (the overwhelming majority: no
  // `client` key at all) passes straight through, including the listener's
  // own state-change publishes above.
  store.setState = (partial) => {
    const current = store.getState();
    const resolved = typeof partial === "function" ? partial(current) : partial;
    if ("client" in resolved && resolved.client !== current.client) {
      wireClient(resolved.client ?? null, () => rawSetState(resolved));
      return;
    }
    rawSetState(resolved);
  };

  store.connect = connect;
  return store;
}

// Subscribes `handler` to the client `store` holds now and to every client it
// wires later - a caller that loads before the host's own connect() effect
// has no client to read once, so it reacts to the store instead. A replaced
// client is detached so it does not keep a live subscription for the rest of
// the store's life. Returns the disposer for both halves.
export function onConnectionNotification(store: ConnectionStore, handler: (n: AnyNotification) => void): () => void {
  let wired: AppwireClientLike | null = null;
  let unwire: (() => void) | undefined;
  const attach = (client: AppwireClientLike | null): void => {
    if (client === wired) return; // already wired to this exact client, including both null
    unwire?.();
    wired = client;
    // A cleared client (the store's `client` set to null - a disconnect, a
    // test reset) has no listener to attach; detaching the previous one
    // above is this call's whole job then, not a no-op skipped by the null
    // check that used to sit ahead of it.
    unwire = client ? client.onNotification(handler) : undefined;
  };
  const unsubscribe = store.subscribe((state) => attach(state.client));
  attach(store.getState().client);
  return () => {
    unsubscribe();
    unwire?.();
  };
}
