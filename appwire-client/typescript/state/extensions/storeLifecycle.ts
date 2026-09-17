// The lifecycle the extensions stores share: one hub notification saying the
// store's list changed, the debounced refetch it schedules, and the
// start/reset/dispose trio a host drives from the screen it mounts the store
// on. Each store wraps one of these the way both wrap createListRevision().
//
// What the lifecycle owns is the bookkeeping that outlives a single request:
// the subscription, the debounce timer, the disposed flag that every write is
// guarded by - a mutation's response is fenced by no revision, so a reply
// landing after the screen is gone must publish nothing - and the connection
// the list was read through.
//
// The connection is load-bearing because the hub broadcasts a change to every
// CONNECTED client: a change made while this one was away reaches it as
// nothing at all, so the notification is not a recovery path and the
// reconnect is. A list a host has already read is read again when the
// connection is ready again; a list nothing has read stays unread, because a
// store whose host never asked for the list must not start asking on its own.

import type { AppwireClient, ConnectionState } from "../../client";
import type { FrameworkFreeStore } from "../../frameworkFreeStore";

export interface StoreLifecycleOptions<S> {
  /** The notification that says the list changed. */
  method: string;
  /** How long the refetch coalesces a burst of notifications. */
  debounceMs: number;
  /** The store this lifecycle drives, read lazily: the store is built over
   * this lifecycle's own guard, so it does not exist yet when the lifecycle
   * is created. */
  store(): FrameworkFreeStore<S>;
  /** Refetches the list, once the debounce elapses. */
  refetch(state: S): unknown;
  /** Applied synchronously as the notification arrives, ahead of the
   * debounced refetch: what a host may know the moment the hub says the set
   * changed (a revision the host keys derived data on, a cache the change
   * retires). */
  onNotified?(): void;
  /** Fences whatever else the store has on the wire - its list revision, and
   * any per-key generation beside it - and settles what the fenced requests
   * would have answered. Nothing is on the wire once this returns, so a flag a
   * fenced request raised has nothing left to lower it: the store lowers it
   * here, through the guarded setter, which drops the write if the store has
   * been disposed (nobody is listening then). */
  onFence?(set: FrameworkFreeStore<S>["setState"]): void;
  /** Whether the list has been read: a read that landed, or one that failed
   * and left its error. Only an established list is recovered on reconnect. */
  established(state: S): boolean;
}

export interface StoreLifecycle<S> {
  /** Wraps the store's own publish: a write after dispose() is dropped. */
  guard(publish: FrameworkFreeStore<S>["setState"]): FrameworkFreeStore<S>["setState"];
  /** Subscribes to the notification. Idempotent, and refused after
   * dispose(). */
  start(): void;
  /** Back to the initial state; requests still in flight publish nothing when
   * they land. The notification subscription, if started, stays. */
  reset(): void;
  /** Tells the lifecycle which connection the list belongs to now, and what
   * state it is in - the host calls it for every transition its connection
   * reports. A connection that becomes ready, or a client that replaces the
   * one the list was read through, re-reads an established list. */
  connectionChanged(client: object | null, state: ConnectionState): void;
  /** Terminal: unsubscribes, cancels a pending refetch and drops every reply
   * still in flight, so subscribers hear nothing more. */
  dispose(): void;
}

export function createStoreLifecycle<S>(
  client: Pick<AppwireClient, "onNotification">,
  options: StoreLifecycleOptions<S>,
): StoreLifecycle<S> {
  let stopNotifications: (() => void) | undefined;
  let refetchTimer: ReturnType<typeof setTimeout> | undefined;
  let disposed = false;
  let connection: { client: object | null; state: ConnectionState } = { client: null, state: "idle" };
  // Whether this store has ever had a ready connection: what tells a
  // reconnection from a first connection.
  let hasBeenReady = false;
  // The store's own publish, guarded: kept so the fence can settle what the
  // requests it cancels would have answered.
  let guardedSet: FrameworkFreeStore<S>["setState"] | undefined;

  function fenceInFlight(): void {
    if (guardedSet) options.onFence?.(guardedSet);
    clearTimeout(refetchTimer);
    refetchTimer = undefined;
  }

  function handleNotification(n: { method: string }): void {
    // Unsubscribing is only the cooperative half of dispose: a dispatcher
    // that snapshots its handler set still calls a handler removed during
    // that dispatch (AppwireClient.setState does exactly this), so the flag
    // is what actually ends this store's interest in the notification.
    if (disposed || n.method !== options.method) return;
    // The notification names nothing, so a refetch of the whole list is the
    // only way to apply it.
    options.onNotified?.();
    clearTimeout(refetchTimer);
    refetchTimer = setTimeout(() => {
      void options.refetch(options.store().getState());
    }, options.debounceMs);
  }

  return {
    guard(publish) {
      guardedSet = (partial): void => {
        if (!disposed) publish(partial);
      };
      return guardedSet;
    },
    start() {
      if (disposed || stopNotifications) return;
      stopNotifications = client.onNotification(handleNotification);
    },
    reset() {
      if (disposed) return;
      // Total: a store that has forgotten what it read holds no data derived
      // from a connection either, so it has not "been away" from one - the
      // next ready connection is a first connection for it.
      connection = { client: null, state: "idle" };
      hasBeenReady = false;
      fenceInFlight();
      const store = options.store();
      store.setState(store.getInitialState());
    },
    connectionChanged(client, state) {
      const previous = connection;
      // Not a transition. A host publishes its connection on every change it
      // makes to it, and most of those are metadata - the handshake's
      // serverInfo and features land as one, on the client and state the
      // store already has. There is nothing to recover, and nothing may be
      // cancelled: the read a notification scheduled is about a change no
      // recovery read would replace.
      if (client === previous.client && state === previous.state) return;
      const replaced = client !== previous.client;
      connection = { client, state };
      // A read scheduled on the connection that is changing has nothing left
      // to say: on the way down it would fire against a socket that is gone
      // and record an error nobody has to see, and on the way up the recovery
      // read below replaces it - keeping it would send a second read for the
      // same change.
      clearTimeout(refetchTimer);
      refetchTimer = undefined;
      // A REPLACED client is a different hub, and everything the previous one
      // still owes describes a machine this store no longer speaks to: a list,
      // a mutation's answer, a browse. All of it is fenced, so none of it can
      // populate this store with the previous hub's data. A transition on the
      // SAME client is not that - its own replies reject when its socket
      // drops, and a flap leaves what it already answered as true as it was.
      if (replaced) fenceInFlight();
      // A client that is already reconnecting has been ready before: that is
      // what reconnecting means. A store built while its host's connection was
      // flapping would otherwise meet it mid-flap, call the ready that follows
      // its first, and invalidate nothing. "closed" is deliberately not
      // evidence - a first connection that failed ends there too.
      if (state === "reconnecting") hasBeenReady = true;
      if (disposed || state !== "ready") return;
      const away = hasBeenReady;
      hasBeenReady = true;
      // Becoming ready AGAIN says what a notification says, and less
      // precisely: everything the hub answers may have moved while this client
      // was away, with no notification left to say so. What a notification
      // applies is therefore applied here too, and BEFORE the established
      // check, because it is not about this store's own list - a revision a
      // host re-keys host-scoped data on, or a cache of answers to other
      // questions, is stale whether or not anything ever read the list. Only
      // the re-read waits on something having read it.
      //
      // A first connection is not a reconnection: nothing was missed, because
      // there was nothing to miss it with.
      if (away) options.onNotified?.();
      if (!options.established(options.store().getState())) return;
      void options.refetch(options.store().getState());
    },
    dispose() {
      // Before the unsubscribe, not after: see handleNotification.
      disposed = true;
      fenceInFlight();
      stopNotifications?.();
      stopNotifications = undefined;
    },
  };
}
