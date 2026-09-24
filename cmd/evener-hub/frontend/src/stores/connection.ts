// The web's one connection store: the package's createConnectionStore with
// zustand's useStore for the reactive read. The client-swap safety and
// notification-following are the package's; serverInfo/features stay plain
// fields the web writes from AppShell's and ConnectionBanner's own handshake
// read (the one InitializeResponse), exactly as before.
//
// The core keeps `connect` as a sibling of the triple, not a state key (a
// partial setState can only ever replace state keys, so keeping it out of
// state is what makes it impossible for one to accidentally clobber the
// other). Every one of this store's ~36 importers calls
// `connectionStore.getState().connect(client)`, so the web's own exposed
// state puts `connect` back as a field - delegating to the core's method -
// rather than changing every call site to `connectionStore.connect(client)`.
//
// `connectionStore` IS the core object (not a separate wrapper): only
// `getState`/`getInitialState`/`subscribe` are overridden in place, so
// `setState` keeps the exact identity `connect()`'s own closure calls it
// through. A separate wrapper's `setState` would just forward to the core's,
// which is a different function slot than the one `connect()` actually
// calls - invisible to anything that spies on or wraps it, which is exactly
// how this broke threads.test.ts's `vi.spyOn(connectionStore, "setState")`
// idempotency check the first time this was tried.
import type { AnyNotification, AppwireClientLike, FrameworkFreeStore } from "@evener/appwire-client";
import {
  type ConnectionStoreState as CoreConnectionStoreState,
  onConnectionNotification as coreOnConnectionNotification,
  createConnectionStore,
} from "@evener/appwire-client/state/connection";
import { useStore } from "zustand";

const core = createConnectionStore();

export interface ConnectionStoreState extends CoreConnectionStoreState {
  connect: (client: Parameters<typeof core.connect>[0]) => void;
}

type ConnectionStore = FrameworkFreeStore<ConnectionStoreState>;

// Memoized per core state object (a fresh object on every setState call,
// frameworkFreeStore.ts, including a same-value one) so the same core state
// always maps to the same wrapped object - required for zustand's `useStore`
// with no selector (useProviderSetup.ts destructures the whole state):
// useSyncExternalStore needs a stable snapshot when nothing changed, and a
// fresh `{ ...state, connect }` object on every call would never look stable.
// A WeakMap rather than a single memo slot: a subscribe listener receives
// both `state` and `previous` in one call, and a single-slot cache keyed by
// "the last state seen" would thrash between the two on every notification.
const wrapped = new WeakMap<CoreConnectionStoreState, ConnectionStoreState>();
function withConnect(state: CoreConnectionStoreState): ConnectionStoreState {
  let entry = wrapped.get(state);
  if (!entry) {
    entry = { ...state, connect: core.connect };
    wrapped.set(state, entry);
  }
  return entry;
}

const originalGetState = core.getState.bind(core);
const originalGetInitialState = core.getInitialState.bind(core);
const originalSubscribe = core.subscribe.bind(core);
const originalSetState = core.setState.bind(core);
// The whole triple is replaced, in place, on the object `connectionStore`
// below re-exposes: `setState`'s updater form promises its callback a
// `ConnectionStoreState` (this file's exported type, `connect` included),
// same as `getState`, so it has to fold `connect` in too - a raw core state
// with no `connect` field would silently break that promise for any updater
// that reads it. `connect` is never a state key on the core (round 1), so it
// is stripped back out of whatever an updater returns before forwarding:
// a stale `connect` pulled off a snapshot could never mask the live one
// anyway (the core has no such key to write it into), but forwarding it
// unstripped would still leave one sitting in the core's state object.
const mutableCore = core as unknown as Pick<ConnectionStore, "getState" | "getInitialState" | "setState" | "subscribe">;
mutableCore.getState = () => withConnect(originalGetState());
mutableCore.getInitialState = () => withConnect(originalGetInitialState());
mutableCore.setState = (partial) => {
  originalSetState((state) => {
    const updates = typeof partial === "function" ? partial(withConnect(state)) : partial;
    const { connect, ...rest } = updates;
    void connect;
    return rest;
  });
};
mutableCore.subscribe = (listener) =>
  originalSubscribe((state, previous) => listener(withConnect(state), withConnect(previous)));

export const connectionStore = core as unknown as ConnectionStore;

// The client port every web store that rides this one wiring point shares: the
// package's adapters all take a `Pick<AppwireClient, "request" | "onNotification">`
// (keybindingsStore.ts, launchConfig.ts, state/extensions/*), and each store
// used to spell out the same requireClient()/port pair over connectionStore.
// `requireClient` resolves connectionStore's CURRENT client at call time -
// never captured at module load, so a call before AppShell's connect() fails
// loudly (labelled by the calling store) and a reconnect is picked up without
// rebuilding the store - and the port forwards to it on every call.
export interface ConnectedClientPort {
  requireClient(): AppwireClientLike;
  request: AppwireClientLike["request"];
  onNotification: AppwireClientLike["onNotification"];
}

export function connectedClientPort(label: string): ConnectedClientPort {
  const requireClient = (): AppwireClientLike => {
    const client = connectionStore.getState().client;
    if (!client) {
      throw new Error(`${label} store: no client connected; call connectionStore.getState().connect(client) first`);
    }
    return client;
  };
  return {
    requireClient,
    request: async (method, params, opts) => requireClient().request(method, params, opts),
    onNotification: (cb) => requireClient().onNotification(cb),
  };
}

// Subscribes `handler` to the client this store holds now and to every client
// it wires later - a store module that loads before AppShell's own connect()
// effect has no client to read once, so it reacts to the store instead (the
// navigation store's original rationale). Thin wrapper over the package's
// onConnectionNotification, bound to the web's one core instance.
export function onConnectionNotification(handler: (n: AnyNotification) => void): () => void {
  return coreOnConnectionNotification(core, handler);
}

/**
 * onConnectionReplacedOrRecovered subscribes `handler` to the two transitions
 * that mean this browser's controller connection may have MISSED something -
 * the hub broadcasts a change to every CONNECTED client, so a change made while
 * this one was away reaches it as nothing at all:
 *
 *   - a REPLACED client: a different client object is wired. The data any read
 *     of the previous one produced describes a socket the browser has left.
 *   - a RECOVERY: the connection becomes ready again after being away. Anything
 *     that happened during the gap carried no notification this client saw.
 *
 * A FIRST connection is deliberately not a recovery - nothing was missed, because
 * there was nothing to miss it with - so a handler mounted before AppShell's own
 * connect() does not fire on that first ready. The `client` and `state` repeat
 * guard is the same one createStoreLifecycle uses: the store publishes on every
 * setState, including the handshake's own metadata writes, and none of those is
 * a transition.
 *
 * Nothing here reads or writes the connection: it is a subscription helper for
 * stores that follow the connection rather than owning it. */
export function onConnectionReplacedOrRecovered(handler: () => void): () => void {
  const initial = connectionStore.getState();
  let client: AppwireClientLike | null = initial.client;
  let state = initial.state;
  // Whether this connection has ever been ready: what tells a reconnection from
  // a first connection.
  let seenReady = initial.state === "ready";
  return connectionStore.subscribe((next) => {
    if (next.client === client && next.state === state) return;
    // A REPLACEMENT is a different client on both sides of the swap. A null
    // side is this browser's FIRST client (or a cleared one), not a replacement:
    // a first connect is the mount read's own trigger, not a recovery.
    const replaced = client !== null && next.client !== null && next.client !== client;
    client = next.client;
    state = next.state;
    // A client already reconnecting has been ready before - that is what
    // reconnecting means - so a store that meets the flap mid-way still treats
    // the ready that follows it as a recovery.
    if (next.state === "reconnecting") {
      seenReady = true;
      return;
    }
    if (next.state !== "ready" || next.client === null) return;
    if (replaced || seenReady) handler();
    seenReady = true;
  });
}

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
  // stores/navigation/store.ts, shell/workspace.ts).
  // biome-ignore lint/correctness/useHookAtTopLevel: same hook both arms, JS default param not a real conditional - see above
  return selector ? useStore(connectionStore, selector) : useStore(connectionStore);
}
