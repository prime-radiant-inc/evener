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
  /** Fences whatever else the store has on the wire, on reset and dispose:
   * its list revision, and any per-key generation beside it. */
  onFence?(): void;
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

  function fenceInFlight(): void {
    options.onFence?.();
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
    guard:
      (publish) =>
      (partial): void => {
        if (!disposed) publish(partial);
      },
    start() {
      if (disposed || stopNotifications) return;
      stopNotifications = client.onNotification(handleNotification);
    },
    reset() {
      fenceInFlight();
      const store = options.store();
      store.setState(store.getInitialState());
    },
    connectionChanged(client, state) {
      const previous = connection;
      connection = { client, state };
      if (disposed || state !== "ready") return;
      // Keyed on the client as well as the state: a replacement that arrives
      // already ready is a different hub's answer to the same question, and
      // the list this store holds was read through the one it replaced.
      if (client === previous.client && previous.state === "ready") return;
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
