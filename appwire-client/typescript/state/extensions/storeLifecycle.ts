// The lifecycle the extensions stores share: one hub notification saying the
// store's list changed, the debounced refetch it schedules, and the
// start/reset/dispose trio a host drives from the screen it mounts the store
// on. Each store wraps one of these the way both wrap createListRevision().
//
// What the lifecycle owns is the bookkeeping that outlives a single request:
// the subscription, the debounce timer, and the disposed flag that every
// write is guarded by - a mutation's response is fenced by no revision, so a
// reply landing after the screen is gone must publish nothing.

import type { AppwireClient } from "../../client";
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

  function fenceInFlight(): void {
    options.onFence?.();
    clearTimeout(refetchTimer);
    refetchTimer = undefined;
  }

  function handleNotification(n: { method: string }): void {
    if (n.method !== options.method) return;
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
    dispose() {
      fenceInFlight();
      stopNotifications?.();
      stopNotifications = undefined;
      disposed = true;
    },
  };
}
