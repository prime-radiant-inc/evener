// The connection lifecycle's client-swap safety, framework-free: which
// AppwireClientLike a host is wired to right now, the ConnectionState mirror
// that follows it, and onConnectionNotification, which follows whichever
// client the store holds across a swap. serverInfo/features are plain
// settable fields the host writes after its own handshake read (the
// InitializeResponse) - this module only knows client identity and wire
// state, never the handshake itself.
//
// The invariant both the connection-state mirror and onConnectionNotification
// keep: whatever the `client` key reads, exactly one listener is wired to it,
// and an event from any other client reaches nothing.
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
  // Settable directly through setState, not only through connect() - both
  // are the same write, below.
  client: AppwireClientLike | null;
}

// connect is a sibling of the triple, not a state key (state/navigation/store.ts's
// `init`/`awaitConvergence`/`reset`, state/credentials/instances.ts's
// `connectionChanged` are the same shape): a partial setState can only ever
// replace state keys, so keeping connect out of state is what makes it
// impossible for one to accidentally clobber the other.
export interface ConnectionStore extends FrameworkFreeStore<ConnectionStoreState> {
  // connect wires this store's `state` to the client's own ConnectionState
  // transitions, capturing whatever state the client is already in. A thin
  // wrapper over setState, which does the actual wiring.
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
  // is still invoked for that dispatch. The listener therefore re-checks
  // that it is still the store's client before publishing.
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
  // A generation counter, not a boolean: "am I still the write that owns
  // unwireStateChange" cannot be answered by comparing `client` identity
  // alone, because a setState call publishing a client swap can itself be
  // re-entered (a subscriber calling setState/connect again, synchronously,
  // inside the publish below) one or more times before control returns to
  // the outer call - including back to the SAME client, which an identity
  // check cannot tell apart from "no one has touched this since me". Bumped
  // on every wiring decision and captured as that write's own generation;
  // only the write whose generation is still the latest one issued may claim
  // the slot once its own publish returns.
  let connectGeneration = 0;

  // Mutated in place and returned as this same object rather than spread
  // into a copy: a caller instrumenting the returned store's `setState` (a
  // spy, a wrapping adapter) has to see every write this module makes
  // through that exact property, including the ones the listener below
  // makes on its own - a spread would hand such a caller a different object
  // whose `setState` those internal writes never touch.
  const store = createFrameworkFreeStore<ConnectionStoreState>(() => ({
    state: "idle",
    serverInfo: undefined,
    features: undefined,
    client: null,
  })) as ConnectionStore;

  // The triple's own setState, captured once before the override below
  // replaces `store.setState` - every write inside that override goes
  // through this, since `store.setState` is the override itself by the time
  // any of them run.
  const publish = store.setState.bind(store);

  // The single writer for this store's state, and the one place a client
  // transition is wired - whether the caller went through connect() or
  // replaced `client` directly. A partial (or an updater's result) naming a
  // different `client` gets the listener wiring below; every other write
  // (the overwhelming majority: no `client` key at all, including the
  // listener's own state-change publishes) passes straight through.
  store.setState = (partial) => {
    const resolved = typeof partial === "function" ? partial(store.getState()) : partial;
    // Read fresh here, not reused from the snapshot the updater above ran
    // against: an updater can synchronously re-enter this same setState (or
    // connect()) while it runs, wiring a different client for real before
    // returning. Deciding "did client change" against the pre-call snapshot
    // would compare the updater's return value to a client this write's own
    // publish already made stale, missing a change that already happened.
    const current = store.getState();
    if (!("client" in resolved) || resolved.client === current.client) {
      publish(resolved);
      return;
    }
    const client = resolved.client ?? null;
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
    // A swap or clear defaults `state` to the incoming client's own state (or
    // "idle" with none - the store's own starting value, and the value every
    // existing `client: null` reset already pairs it with) and drops the
    // outgoing client's serverInfo/features, the same reset connect() always
    // applied - but a caller's own partial still wins where it names one, so
    // a swap that already knows its InitializeResponse can publish it in the
    // same write. `client` is written explicitly and normalized to `null`,
    // never left as whatever `resolved.client` happened to be (undefined is
    // not a valid `client` value, just an easy partial to write by mistake).
    publish({ state: client ? client.state : "idle", serverInfo: undefined, features: undefined, ...resolved, client });
    // See connectGeneration above: only claim the slot if this write is
    // still the latest one issued once its own publish returns.
    if (connectGeneration === generation) {
      if (unwire) unwireStateChange = unwire;
    } else {
      unwire?.();
    }
  };

  function connect(client: AppwireClientLike): void {
    // Unlike a direct setState({ client: same }) call - which still
    // notifies, per the framework-free store's own documented contract of
    // publishing every write even when nothing changed - a repeated
    // connect() with the identical client is a caller re-declaring what is
    // already true (an effect re-running under, say, React StrictMode's
    // double-invocation) and should cost nothing.
    if (store.getState().client === client) return;
    store.setState({ client });
  }

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
