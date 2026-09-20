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
//
// CONNECTION IDENTITY
//
// A replaced connection's in-flight replies, its notifications, and what a
// reset does to both are one question, so the answer is written out here and
// the predicate implemented from it.
//
// Two different things are easy to conflate:
//   - the SOURCE: the object frames arrive through, `notifications`, fixed for
//     the store's life.
//   - the IDENTITY: what the host says the store's data belongs to, reported
//     through connectionChanged and free to change.
// A host either reports its own source as the identity (a real client), or
// reports something else and subscribes through a port that follows the
// identity for it (the web: `hubClient` over onConnectionNotification, which
// re-wires onto each new client and unwires the previous one).
//
// INVARIANT
//   The store acts on a notification unless it can prove the frame came from a
//   connection it has left. It can prove that only when the host names its own
//   source as the identity; a host that names something else has delegated the
//   proof to its port.
//
// EVENTS
//
//   event                        | identity   | names source | a frame from the source is
//   -----------------------------|------------|--------------|---------------------------
//   construct                    | none       | unknown      | (not subscribed yet)
//   start()                      | unchanged  | unchanged    | acted on
//   connectionChanged(c) c===src  | c          | yes          | acted on
//   connectionChanged(c) c!==src  | c          | unchanged    | ignored once known to
//                                |            |              | name the source; acted on
//                                |            |              | for a port host
//   reset()                      | none       | unknown      | acted on - nothing has
//                                |            |              | been reported to compare
//   dispose()                    | unchanged  | unchanged    | ignored, always
//
// One row is about the requests rather than the frames, and it is the reason
// `wantsList` is asked BEFORE the fence:
//
//   replacement while a read is in flight | the read is fenced and the flag it
//     raised is settled (nothing on the wire will lower it), AND the intent
//     survives: something asked for this list and never got it, so the read is
//     issued again once the replacement is ready. The intent is not in the
//     store's data - a read that never landed left none - so it is read off the
//     state the fence is about to clear, and LATCHED.
//
//   replacement while a WRITE is in flight, none of the state a read would have
//   left | the same recovery, for a mutation issued before anything ever read
//     the list (a fresh screen's first install). `wantsList` alone would miss
//     it: nothing has set the loading flag or a prior list, so there is no
//     state field to read the intent off. A store that hands the lifecycle its
//     listRevision (the `revision` option) gets that revision's hasLive()
//     ORed in here - the seam readRevisioned and writeRevisioned share
//     (listRevision.ts), true for a write on the wire exactly as it already is
//     for a read - so the intent is latched here the same way either way, and
//     a new store cannot forget to ask.
//
//   the replacement is named before it is dialled | the same thing, in two
//     calls: a host connects its fresh client and only then awaits it, so the
//     store hears "idle" first. That call fences the read and returns, having
//     settled the flag; the "ready" that follows would find nothing left to
//     act on. The latch is what carries the intent between the two, and only
//     reset() clears it.
//
// Replacing the identity also fences every reply the previous connection still
// owes (see connectionChanged), so neither its answers nor its announcements
// reach the store.
//
// The one frame this cannot catch: a port host whose old client is dispatching
// at the instant the port unwires it. AppwireClient.setState dispatches over a
// snapshot of its handler set, so that frame still reaches the handler, and
// only the source could tell it apart - which the port type deliberately does
// not carry. Its cost is bounded and conservative: what a notification applies
// is monotonic (a cache retired, a revision advanced, both of which a host
// re-derives from the CURRENT connection), and the read it schedules goes
// through the port to the current client. So the worst case is one redundant
// read of the right hub, never a page of the wrong hub's data.

import type { AppwireClient, ConnectionState } from "../../client";
import type { FrameworkFreeStore } from "../../frameworkFreeStore";
import type { ListRevision } from "./listRevision";

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
  /** The store's list revision, when it has one. A write and a read both
   * issue through it (listRevision.ts), so a live revision is a request
   * something asked for even when the store's own state shows nothing - the
   * case in which `wantsList` alone would miss it. The lifecycle ORs its
   * hasLive() into `wantsList` and fences it with everything else a fence
   * settles, so neither half can be forgotten by a store that wraps it. */
  revision?: ListRevision;
  /** Applied synchronously as the notification arrives, ahead of the
   * debounced refetch: what a host may know the moment the hub says the set
   * changed (a revision the host keys derived data on, a cache the change
   * retires). */
  onNotified?(): void;
  /** Fences whatever else the store has on the wire - a per-key generation
   * beside its list revision, which the lifecycle fences itself - and settles
   * what the fenced requests would have answered. Nothing is on the wire once
   * this returns, so a flag a fenced request raised has nothing left to lower
   * it: the store lowers it here, through the guarded setter, which drops the
   * write if the store has been disposed (nobody is listening then). */
  onFence?(set: FrameworkFreeStore<S>["setState"]): void;
  /** State fields that survive reset without becoming a new publication. */
  resetState?(state: S): Partial<S>;
  /** Whether the store's OWN state wants this list: it has one, a read failed
   * and left its error, or a read is in flight. A list-producing write issued
   * before anything ever read the list touches none of those fields; that
   * intent comes from `revision` above, which the lifecycle ORs in. Only a
   * list something has asked for is recovered on reconnect - a store whose
   * host never asked must not start asking on its own - and asking counts
   * from the moment the request goes out, not from when it lands. */
  wantsList(state: S): boolean;
}

export interface StoreLifecycle<S> {
  /** Wraps the store's own publish: a write after dispose() is dropped. */
  guard(publish: FrameworkFreeStore<S>["setState"]): FrameworkFreeStore<S>["setState"];
  /** Subscribes to the notification. Idempotent, and refused after
   * dispose(). */
  start(): void;
  /** Back to the reset state; fields selected by resetState may survive while
   * requests still in flight publish nothing when they land. The notification
   * subscription, if started, stays. */
  reset(): void;
  /** Tells the lifecycle which connection the list belongs to now, and what
   * state it is in - the host calls it for every transition its connection
   * reports. A connection that becomes ready, or a client that replaces the
   * one the list was read through, re-reads a list something wants. */
  connectionChanged(client: object | null, state: ConnectionState): void;
  /** Terminal: unsubscribes, cancels a pending refetch and drops every reply
   * still in flight, so subscribers hear nothing more. */
  dispose(): void;
}

/**
 * `notifications` is where the store hears the hub. A host supplies either a
 * real client, which it then names in connectionChanged - so a notification
 * arriving through a client that is no longer the connection is stale, and
 * ignored - or a port that follows the connection for it and therefore never
 * appears in connectionChanged at all (the web's, which re-wires onto each new
 * client and drops the previous one's subscription). Both are safe; the first
 * is made safe here, the second by the port.
 */
export function createStoreLifecycle<S>(
  notifications: Pick<AppwireClient, "onNotification">,
  options: StoreLifecycleOptions<S>,
): StoreLifecycle<S> {
  let stopNotifications: (() => void) | undefined;
  let refetchTimer: ReturnType<typeof setTimeout> | undefined;
  let disposed = false;
  let connection: { client: object | null; state: ConnectionState } = { client: null, state: "idle" };
  // Whether this store has ever had a ready connection: what tells a
  // reconnection from a first connection.
  let hasBeenReady = false;
  // Whether anything has asked for the list. It is deliberately NOT read off
  // the store's data: a read that never landed leaves none, and the fence that
  // cancels such a read settles the flag it raised. So the intent is latched
  // here and lives until reset(), the way the credentials core keeps
  // requestedList outside its own listing state.
  let wanted = false;
  // Whether the host names the subscription as its connection; see the factory
  // doc. Set the first time connectionChanged mentions it, which a host using
  // a port never does.
  let subscriptionIsTheConnection = false;
  // The store's own publish, guarded: kept so the fence can settle what the
  // requests it cancels would have answered.
  let guardedSet: FrameworkFreeStore<S>["setState"] | undefined;

  function fenceInFlight(): void {
    options.revision?.fence();
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
    // A change announced by a client that is no longer the connection is about
    // a hub this store has stopped speaking to: acting on it would retire the
    // current hub's caches, move the revision its consumers key on, and read a
    // list from the connection that was replaced.
    if (subscriptionIsTheConnection && connection.client !== notifications) return;
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
      stopNotifications = notifications.onNotification(handleNotification);
    },
    reset() {
      if (disposed) return;
      // Total: a store that has forgotten what it read holds no data derived
      // from a connection either, so it has not "been away" from one - the
      // next ready connection is a first connection for it. The identity goes
      // with it, and so must what was inferred FROM it: with nothing reported,
      // there is nothing to compare a frame against, and the source's own
      // notifications must still arrive.
      connection = { client: null, state: "idle" };
      hasBeenReady = false;
      subscriptionIsTheConnection = false;
      wanted = false;
      fenceInFlight();
      const store = options.store();
      store.setState({ ...store.getInitialState(), ...options.resetState?.(store.getState()) });
    },
    connectionChanged(client, state) {
      const previous = connection;
      if (client === notifications) subscriptionIsTheConnection = true;
      // Not a transition. A host publishes its connection on every change it
      // makes to it, and most of those are metadata - the handshake's
      // serverInfo and features land as one, on the client and state the
      // store already has. There is nothing to recover, and nothing may be
      // cancelled: the read a notification scheduled is about a change no
      // recovery read would replace.
      if (client === previous.client && state === previous.state) return;
      const replaced = client !== previous.client;
      // Before the fence, which settles the flag a read in flight raised and
      // so erases the only evidence that one was asked for. Latched rather
      // than used here and forgotten: a host names its fresh client before it
      // dials it, so the call that sees the interrupted read is usually NOT
      // the call that can re-issue it.
      wanted = wanted || options.wantsList(options.store().getState()) || (options.revision?.hasLive() ?? false);
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
      // applies is therefore applied here too, and BEFORE the wantsList
      // check, because it is not about this store's own list - a revision a
      // host re-keys host-scoped data on, or a cache of answers to other
      // questions, is stale whether or not anything ever read the list. Only
      // the re-read waits on something having read it.
      //
      // A first connection is not a reconnection: nothing was missed, because
      // there was nothing to miss it with.
      if (away) options.onNotified?.();
      if (!wanted) return;
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

/**
 * The store a host drives: the store's own reactive triple plus everything of
 * the lifecycle except `guard`, which stays the store's own business.
 */
export function attachLifecycle<S, T extends FrameworkFreeStore<S>>(
  store: T,
  lifecycle: StoreLifecycle<S>,
): T & Omit<StoreLifecycle<S>, "guard"> {
  const { start, connectionChanged, reset, dispose } = lifecycle;
  return { ...store, start, connectionChanged, reset, dispose };
}
