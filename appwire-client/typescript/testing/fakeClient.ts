// FakeClient is an AppwireClientLike test double for the store test suite
// (../../stores/threads.test.ts). Unlike FakeSocket (./fakeSocket.ts), which
// fakes the raw WebSocket transport underneath a real AppwireClient,
// FakeClient fakes AppwireClient's own public surface directly — the seam
// the stores (src/stores/*) depend on, per AppwireClientLike (../clientLike.ts) — with
// scripted per-method request handlers and manual notification/ready/
// state-change injection. No sockets, no timers.
import { APPWIRE_PROTOCOL_VERSION, type AppwireClient, type ConnectionState, type TerminalReason } from "../client";
import type { AppwireClientLike } from "../clientLike";
import { ConnectionClosedError } from "../errors";
import type { AnyNotification, InitializeResponse, MethodName, MethodTypes } from "../types.gen";
import { METHOD_NAMES, NOTIFICATION_NAMES } from "../types.gen";

// The hub's real method catalog, as data. MethodName alone is a compile-time
// constraint, and a compile-time constraint is silent whenever it has been
// bypassed — a cast, an `any`, a name assembled at runtime. That silence is
// what let the evener/dirs/complete -> evener/paths/complete rename ship a broken
// picker past a green suite: the test scripted the old name, the component
// called the old name, and the two agreed with each other rather than with
// the hub. Checking against this set makes a nonexistent method a loud
// failure at the moment a test scripts or requests it.
const KNOWN_METHODS: ReadonlySet<string> = new Set(METHOD_NAMES);

function assertKnownMethod(method: string): void {
  if (!KNOWN_METHODS.has(method)) {
    throw new Error(
      `FakeClient: unknown method "${method}" — not in the hub's generated method catalog (METHOD_NAMES in appwire-client/typescript/types.gen.ts). ` +
        `Either the method was renamed or removed on the wire, or this is a typo; there is no production code path this name can reach.`,
    );
  }
}

// The hub's real notification catalog, as data. The compile-time union is
// even weaker here than on the request side: injecting a notification means
// hand-building its whole payload, so the suite reaches for
// `as AnyNotification` almost everywhere, and a cast silences the union's
// `method` check along with the payload's. A renamed notification would
// therefore satisfy neither tsc nor this fake — it would simply stop
// matching production and take every assertion about it down quietly.
const KNOWN_NOTIFICATIONS: ReadonlySet<string> = new Set(NOTIFICATION_NAMES);

export type RequestHandler<M extends MethodName> = (
  params: MethodTypes[M]["params"],
) => MethodTypes[M]["result"] | Promise<MethodTypes[M]["result"]>;

export type ConnectHandler = () => InitializeResponse | Promise<InitializeResponse>;

// A minimal but well-formed response - every field is required by
// InitializeResponse, so a FakeClient that never calls scriptConnect() still
// resolves connect() with something valid rather than forcing every caller
// to script one just to get past the handshake.
const DEFAULT_INITIALIZE_RESPONSE: InitializeResponse = {
  serverInfo: { name: "fake-evener-hub", version: "0.0.0" },
  protocolVersion: APPWIRE_PROTOCOL_VERSION,
  sourceId: "fake",
  features: {
    threadList: false,
    threadTurnsList: false,
    turnStart: false,
    turnSteer: false,
    threadClear: false,
    threadShutdown: false,
    forkFromTurn: false,
    tasks: false,
    transcriptList: false,
    modelList: false,
    directoryComplete: false,
    auth: false,
  },
};

export interface RecordedCall {
  method: MethodName;
  params: unknown;
  opts?: { timeoutMs?: number };
}

// The message every close-induced rejection carries, in this fake's own shape
// (AppwireClient's is "AppwireClient: closed"). The error TYPE is
// ConnectionClosedError, matching the real client, so a consumer that branches
// on the type takes the same path under this fake as in production.
const CLOSED_MESSAGE = "FakeClient: closed";

// The error a terminal transition injected through emitStateChange("closed")
// fails pending work with. ConnectionClosedError is reserved for a
// caller-initiated close() (AppwireClient.close's failAllPending); an injected
// "closed" models a transport/server drop, which production's handleSocketLoss
// fails with a plain Error.
const TRANSPORT_CLOSED_MESSAGE = "FakeClient: socket closed";

// isThenable distinguishes a handler's plain result from a promise it returned,
// so defer() can settle a plain result in the same microtask the old
// `Promise.resolve().then(invoke)` did and only adopt a returned promise.
function isThenable<T>(value: T | Promise<T>): value is Promise<T> {
  return value !== null && value !== undefined && typeof (value as { then?: unknown }).then === "function";
}

export class FakeClient implements AppwireClientLike {
  state: ConnectionState;
  // Mirrors the real client's terminal-reason surface so a banner test can
  // stage a protocol close, which is the one close a retry cannot resolve.
  terminalReason: TerminalReason = null;
  readonly calls: RecordedCall[] = [];

  // Keyed by method name and erased to a common handler shape here — `on`
  // and `request` always agree on M for a given key — since a heterogeneous
  // map can't otherwise hold a different result/param type per entry; both
  // generic methods below restore full per-method typing at the boundary.
  private readonly handlers = new Map<MethodName, RequestHandler<MethodName>>();
  private readonly notificationHandlers = new Set<(n: AnyNotification) => void>();
  private readonly readyHandlers = new Set<(initialize: InitializeResponse) => void>();
  private readonly stateChangeHandlers = new Set<(s: ConnectionState) => void>();

  // Requests and connects still in flight, each with the reject that settles
  // it. AppwireClient.close() fails every pending request (failAllPending) and
  // aborts an in-flight handshake, so the fake keeps the same handles to fail
  // on close() rather than leaving a deferred handler free to resolve after
  // the client is closed - a teardown test would otherwise never see the
  // lifecycle bug it exists to catch.
  private readonly pending = new Set<(err: Error) => void>();

  // Deliberately independent of `state`/emitStateChange/emitReady below:
  // resolving connect() does not itself change `state`, so every existing
  // test that only drives readiness via the constructor/emitStateChange
  // (i.e. all of them, before this field existed) is unaffected. A test
  // that wants a state transition alongside a scripted connect() still
  // drives that explicitly, exactly as before.
  private connectHandler: ConnectHandler = () => DEFAULT_INITIALIZE_RESPONSE;
  private latestInitialize: InitializeResponse = DEFAULT_INITIALIZE_RESPONSE;

  // Defaults to "ready": tests overwhelmingly want a client stores can
  // request() against immediately, without separately staging the
  // idle -> connecting -> ready sequence a real handshake goes through. Pass
  // a different initial state explicitly to test pre-ready behavior.
  constructor(initialState: ConnectionState = "ready") {
    this.state = initialState;
  }

  // The number of listeners currently registered across onNotification,
  // onReady and onStateChange. It lets a teardown/unmount test assert a
  // subscription was actually released - the leak this fake previously could
  // only be probed for by whether a callback still fired, which a listener
  // that was never registered in the first place also satisfies.
  get listenerCount(): number {
    return this.notificationHandlers.size + this.readyHandlers.size + this.stateChangeHandlers.size;
  }

  // Models AppwireClient.close's observable outcome: the client lands in
  // "closed" and every onStateChange listener is notified synchronously
  // (client.ts's close() ends in setState("closed")). Work still in flight is
  // failed first - with ConnectionClosedError, because that is what the real
  // close()'s failAllPending rejects with - matching the real method's
  // fail-then-transition order, so a deferred handler released after the close
  // cannot settle it. It deliberately does NOT drop the registered listeners -
  // the real close() tears down the transport, not the subscription list, so
  // callers' own unsubscribers still own that - and it leaves terminalReason
  // untouched, since that mirrors a protocol terminal close, not a
  // caller-initiated one. A second close() is a no-op rather than a second
  // transition, exactly like the real method.
  close(): void {
    this.failPending(new ConnectionClosedError(CLOSED_MESSAGE));
    this.emitStateChange("closed");
  }

  // trackPending registers a reject for an in-flight operation and returns a
  // function that clears it. That function returns false when the terminal
  // transition already failed the operation (it removed the entry first), so
  // its settle callback skips a resolve/reject the transition already owns.
  private trackPending(reject: (err: Error) => void): () => boolean {
    this.pending.add(reject);
    return () => this.pending.delete(reject);
  }

  private failPending(err: Error): void {
    for (const reject of Array.from(this.pending)) reject(err);
    this.pending.clear();
  }

  // defer runs `invoke` one microtask later - the fake's single deferral, which
  // lets a synchronously-thrown handler become a normal rejection and which the
  // store suites' `await Promise.resolve()` counts already depend on - and
  // settles the returned promise with its result. Invoking `invoke` directly
  // inside that one microtask, instead of chaining a second `then` onto it,
  // keeps a plain result on that same single hop. The promise's reject is
  // registered as pending so close() fails it if it is still in flight:
  // settling after close() would let a teardown test observe a response the
  // real client can never deliver. `onResult` runs only when this settle is the
  // one that wins.
  private defer<T>(invoke: () => T | Promise<T>, onResult: (value: T) => void = () => {}): Promise<T> {
    return new Promise<T>((resolve, reject) => {
      const settle = this.trackPending(reject);
      Promise.resolve().then(() => {
        let result: T | Promise<T>;
        try {
          result = invoke();
        } catch (err) {
          if (settle()) reject(err);
          return;
        }
        // Inspecting and adopting a thenable is itself hostile-input
        // territory: a getter on `then` (or a `then` method) can throw, and
        // letting that escape this scheduled callback would leave the outer
        // promise pending forever instead of rejecting it.
        try {
          if (isThenable(result)) {
            result.then(
              (value) => {
                if (settle()) {
                  onResult(value);
                  resolve(value);
                }
              },
              (err: unknown) => {
                if (settle()) reject(err);
              },
            );
          } else if (settle()) {
            onResult(result);
            resolve(result);
          }
        } catch (err) {
          if (settle()) reject(err);
        }
      });
    });
  }

  // on scripts the response for every request() call to `method`. The
  // handler may throw (or return a rejected promise) to script a failure —
  // request() propagates it as a rejection either way, preserving whatever
  // error value the handler threw (e.g. a WireError instance).
  on<M extends MethodName>(method: M, handler: RequestHandler<M>): void {
    assertKnownMethod(method);
    this.handlers.set(method, handler as unknown as RequestHandler<MethodName>);
  }

  // scriptConnect scripts connect()'s resolved value, mirroring on()'s
  // shape: the handler may throw (or return a rejected promise) to script a
  // handshake failure.
  scriptConnect(handler: ConnectHandler): void {
    this.connectHandler = handler;
  }

  // Deferred through a microtask like a real handshake, and lets a
  // synchronously-thrown handler become a normal rejection - same idiom as
  // request() below. A connect() on an already-closed client rejects
  // immediately (AppwireClient.connect's isClosed() check), and one still in
  // flight when close() runs is failed by close() instead of settling later.
  connect(): Promise<InitializeResponse> {
    if (this.state === "closed") return Promise.reject(new ConnectionClosedError(CLOSED_MESSAGE));
    return this.defer(
      () => this.connectHandler(),
      (initialize) => {
        this.latestInitialize = initialize;
      },
    );
  }

  request<M extends MethodName>(
    method: M,
    params: MethodTypes[M]["params"],
    opts?: { timeoutMs?: number },
  ): Promise<MethodTypes[M]["result"]> {
    // Checked before the ready-gate below: a method the hub does not serve is
    // a bug regardless of connection state, and reporting "not ready" for it
    // would hide the real defect behind a plausible-looking one.
    try {
      assertKnownMethod(method);
    } catch (err) {
      return Promise.reject(err);
    }
    // Fidelity with AppwireClient.request's own ready-gate (client.ts):
    // a store (or a future test) calling request() while not ready should
    // see the same rejection shape from this fake as it would from the real
    // client — not a scripted response that could never actually arrive
    // over the wire in that state, and no frame recorded as "sent" (the
    // real client never reaches socket.send() in this case either).
    if (this.state !== "ready") {
      return Promise.reject(new Error(`FakeClient: cannot call "${method}" while state is "${this.state}"`));
    }
    if (opts === undefined) {
      this.calls.push({ method, params });
    } else {
      this.calls.push({ method, params, opts });
    }
    const handler = this.handlers.get(method);
    if (!handler) {
      return Promise.reject(new Error(`FakeClient: no handler scripted for "${method}"`));
    }
    // Deferred through a microtask like a real RPC round-trip, and lets a
    // synchronously-thrown handler become a normal rejection. Registered as
    // pending so close() fails it if it is still in flight: settling after
    // close() would let a teardown test observe a response the real client can
    // never deliver. The cast restores this call site's M-specific result at
    // the boundary: `handler` is erased to RequestHandler<MethodName>, whose
    // result is the union of every method's, not this M's.
    return this.defer<MethodTypes[M]["result"]>(() => handler(params) as MethodTypes[M]["result"]);
  }

  onNotification(cb: (n: AnyNotification) => void): () => void {
    this.notificationHandlers.add(cb);
    return () => this.notificationHandlers.delete(cb);
  }

  onReady(cb: (initialize: InitializeResponse) => void): () => void {
    this.readyHandlers.add(cb);
    return () => this.readyHandlers.delete(cb);
  }

  onStateChange(cb: (s: ConnectionState) => void): () => void {
    this.stateChangeHandlers.add(cb);
    return () => this.stateChangeHandlers.delete(cb);
  }

  // retryNow records that it was called, for a consumer's wiring test
  // (ConnectionBanner.test.tsx) to assert against - AppwireClient's own
  // backoff/in-flight-dial semantics are exhaustively covered against the
  // real class in appwire-client/typescript/reconnect.test.ts, so this fake doesn't attempt
  // to model them (no timers, no sockets, nothing to short-circuit).
  retryNowCalls = 0;
  retryNow(): void {
    this.retryNowCalls += 1;
  }

  // Mirrors AppwireClient.resumeThread's ordering, which is what makes
  // "Stop during the resume's reconnect" a testable state: the real class
  // forces a reconnect, awaits it, and only then runs beforeRequest,
  // synchronously before the resume RPC (../client.ts). A Stop that lands
  // inside that await is therefore seen by the guard and cancels the resume.
  //
  // This fake owns no socket, so there is nothing to reconnect and no
  // `connected` promise to await; "after reconnect settles" here means one
  // microtask hop, which reproduces the only property of that await a caller
  // can observe: control returns to the caller BEFORE the guard runs, so a
  // cancellation staged in the same turn is one the guard has to see. It is
  // deliberately a hop and never a timer - the fake has no clock, and no test
  // should have to advance one to reach this ordering. Running the guard
  // synchronously instead (as this fake used to) let a test stage a Stop
  // "during the reconnect" that the guard had already passed, proving nothing
  // about production; a test that needs the full reconnect race still belongs
  // on the real client with FakeSocket (client.test.ts, reconnect.test.ts).
  async resumeThread(ref: string, options?: { beforeRequest?: () => void }): ReturnType<AppwireClient["resumeThread"]> {
    // AppwireClient.resumeThread's own precondition (client.ts): a client that
    // is not ready at call time - including one already closed - throws this
    // plain Error, not ConnectionClosedError. Reporting "closed" here would
    // misclassify an already-closed call as hub-unreachable in callers that
    // branch on the error type.
    // Read through a local so the not-ready narrowing applies to the call-time
    // value alone; the closure check below is a fresh read after the await.
    const stateAtCall = this.state;
    if (stateAtCall !== "ready") throw new Error("Connect to the hub before resuming this session");
    await Promise.resolve();
    // AppwireClient.resumeThread's reconnect await rejects with
    // ConnectionClosedError when the client closes during it, and beforeRequest
    // never runs. The fake's one-microtask wait observes the same closure: a
    // close() staged in the same turn must reject here, not fall through to a
    // guard run followed by a generic closed-state request error.
    if (this.state === "closed") throw new ConnectionClosedError(CLOSED_MESSAGE);
    options?.beforeRequest?.();
    return this.request("thread/resume", { ref });
  }

  async forceStop(ref: string): Promise<void> {
    // AppwireClient.forceStop rejects with ConnectionClosedError on a closed
    // client before doing anything else, the type its isClosed() check uses.
    if (this.state === "closed") throw new ConnectionClosedError(CLOSED_MESSAGE);
    await this.request("evener/thread/forceStop", { ref });
  }

  // --- test-side injection: simulates the server/transport side ---

  // emitNotification simulates one incoming wire notification. The method
  // must be one the hub actually sends; to exercise handling of a name the
  // hub does not send, say so explicitly via emitUnknownNotification below.
  emitNotification(n: AnyNotification): void {
    if (!KNOWN_NOTIFICATIONS.has(n.method)) {
      throw new Error(
        `FakeClient: unknown notification "${n.method}" — not in the hub's generated notification catalog (NOTIFICATION_NAMES in appwire-client/typescript/types.gen.ts). ` +
          `Either the notification was renamed or removed on the wire, or this is a typo; no production listener can ever see this name. ` +
          `If the test means to inject an unrecognized notification, call emitUnknownNotification instead.`,
      );
    }
    this.deliverNotification(n);
  }

  // emitUnknownNotification simulates a notification whose method is NOT in
  // the catalog — what a client sees when a newer hub sends something this
  // build predates. It is the narrow opt-out from emitNotification's guard,
  // for the handful of tests that exercise unrecognized-notification
  // handling (e.g. the reducer's `default:` case), and it refuses a name the
  // hub really does send so it cannot double as a quiet way to keep a
  // renamed notification green.
  emitUnknownNotification(n: { method: string; params: unknown }): void {
    if (KNOWN_NOTIFICATIONS.has(n.method)) {
      throw new Error(
        `FakeClient: "${n.method}" is a real notification in the hub's catalog — use emitNotification, which type-checks its payload.`,
      );
    }
    this.deliverNotification(n as AnyNotification);
  }

  private deliverNotification(n: AnyNotification): void {
    for (const cb of Array.from(this.notificationHandlers)) {
      try {
        cb(n);
      } catch {
        // A misbehaving subscriber must not stop dispatch to the rest, or
        // abort the emitter (AppwireClient's own notification dispatch).
      }
    }
  }

  // emitStateChange simulates the client transitioning to a new
  // ConnectionState, mirroring AppwireClient.setState: state-change
  // subscribers always run, and — exactly like the real class — transitioning
  // *into* "ready" additionally fires every onReady subscriber in the same
  // call, since that is the only path AppwireClient ever reaches ready
  // through (dialAndHandshake, on both the first connect and every
  // reconnect). "closed" is terminal, as it is for the real client (whose
  // private setState is only ever reached through isClosed()-guarded paths):
  // once closed, a later injection is ignored rather than resurrecting a
  // client production would keep unusable.
  emitStateChange(next: ConnectionState, initialize: InitializeResponse = this.latestInitialize): void {
    if (this.state === "closed") return;
    if (this.state === next) return;
    // Every real path into "closed" fails pending work before transitioning
    // (close(), performHandshake's catch, handleSocketLoss), so the injection
    // seam does too: a request or connect still in flight rejects rather than a
    // late handler settling a response production can never deliver. The error
    // is a plain transport Error, matching handleSocketLoss - ConnectionClosedError
    // is close()'s alone (see TRANSPORT_CLOSED_MESSAGE). The terminal guard
    // above means a later close() cannot repair the pending set, so failing here
    // is the only chance.
    if (next === "closed") this.failPending(new Error(TRANSPORT_CLOSED_MESSAGE));
    this.state = next;
    for (const cb of Array.from(this.stateChangeHandlers)) {
      try {
        cb(next);
      } catch {
        // A misbehaving subscriber must not corrupt the state machine or abort
        // whatever operation triggered this transition (e.g. close()), exactly
        // as AppwireClient.setState isolates its dispatch.
      }
    }
    if (next === "ready") {
      this.latestInitialize = initialize;
      for (const cb of Array.from(this.readyHandlers)) {
        try {
          cb(initialize);
        } catch {
          // See above.
        }
      }
    }
  }

  // emitReady simulates a (re)connect succeeding — the common case tests
  // reach for — as a shorthand for emitStateChange("ready").
  emitReady(initialize: InitializeResponse = this.latestInitialize): void {
    this.emitStateChange("ready", initialize);
  }
}

/** A request handler that throws `message`: scripts a method to fail. */
export function failing(message: string): () => never {
  return () => {
    throw new Error(message);
  };
}

/** Scripts `method` to hang and hands back the resolver of the request in
 * flight. FakeClient.request() defers the handler by one microtask, so the
 * resolver exists only after that has flushed; callers await a microtask
 * before releasing. */
export function deferRequest<T>(fake: FakeClient, method: MethodName): (value: T) => void {
  let release!: (value: T) => void;
  fake.on(
    method,
    () =>
      new Promise<T>((resolve) => {
        release = resolve;
      }) as never,
  );
  return (value: T) => release(value);
}

// The generic request scripts a parametrized suite needs: one store's method
// names are data to it, and fake.on/emitNotification are typed per method, so
// the cast lives here once instead of in a closure per store per script.

/** Scripts `method` to answer with `response`. */
export function answerRequests(fake: FakeClient, method: MethodName, response: unknown): void {
  fake.on(method, (() => response) as never);
}

/** Scripts `method` to reject with `message`. */
export function failRequests(fake: FakeClient, method: MethodName, message: string): void {
  fake.on(method, failing(message) as never);
}

/** One in-flight request's settlement, either way: resolve() answers it,
 * reject() fails it. */
export interface Settlement {
  resolve(value: unknown): void;
  reject(error: Error): void;
}

/** Scripts `method` to hang and hands back one settlement per call, so several
 * requests to it can be in flight and be settled — either way — out of order.
 * deferRequest's multi-call reverse: that one keeps a single resolver, which is
 * only ever the last call's, and can only answer. */
export function gateSettlements(fake: FakeClient, method: MethodName): Settlement[] {
  const settlements: Settlement[] = [];
  fake.on(
    method,
    () =>
      new Promise((resolve, reject) => {
        settlements.push({ resolve, reject });
      }) as never,
  );
  return settlements;
}

/** How many requests `method` has received. */
export function callsTo(fake: FakeClient, method: MethodName): number {
  return fake.calls.filter((call) => call.method === method).length;
}
