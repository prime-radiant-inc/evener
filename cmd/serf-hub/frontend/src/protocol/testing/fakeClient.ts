// FakeClient is an AppwireClientLike test double for the store test suite
// (../../stores/threads.test.ts). Unlike FakeSocket (./fakeSocket.ts), which
// fakes the raw WebSocket transport underneath a real AppwireClient,
// FakeClient fakes AppwireClient's own public surface directly — the seam
// the stores (src/stores/*) depend on, per AppwireClientLike below — with
// scripted per-method request handlers and manual notification/ready/
// state-change injection. No sockets, no timers.
import type { AppwireClient, ConnectionState } from "../client";
import type { AnyNotification, InitializeResponse, MethodName, MethodTypes } from "../types.gen";
import { METHOD_NAMES } from "../types.gen";

// The hub's real method catalog, as data. MethodName alone is a compile-time
// constraint, and a compile-time constraint is silent whenever it has been
// bypassed — a cast, an `any`, a name assembled at runtime. That silence is
// what let the serf/dirs/complete -> serf/paths/complete rename ship a broken
// picker past a green suite: the test scripted the old name, the component
// called the old name, and the two agreed with each other rather than with
// the hub. Checking against this set makes a nonexistent method a loud
// failure at the moment a test scripts or requests it.
const KNOWN_METHODS: ReadonlySet<string> = new Set(METHOD_NAMES);

function assertKnownMethod(method: string): void {
  if (!KNOWN_METHODS.has(method)) {
    throw new Error(
      `FakeClient: unknown method "${method}" — not in the hub's generated method catalog (METHOD_NAMES in protocol/types.gen.ts). ` +
        `Either the method was renamed or removed on the wire, or this is a typo; there is no production code path this name can reach.`,
    );
  }
}

export interface AppwireClientLike {
  connect: AppwireClient["connect"];
  request: AppwireClient["request"];
  onNotification: AppwireClient["onNotification"];
  onReady: AppwireClient["onReady"];
  onStateChange: AppwireClient["onStateChange"];
  retryNow: AppwireClient["retryNow"];
  get state(): ConnectionState;
}

export type RequestHandler<M extends MethodName> = (
  params: MethodTypes[M]["params"],
) => MethodTypes[M]["result"] | Promise<MethodTypes[M]["result"]>;

export type ConnectHandler = () => InitializeResponse | Promise<InitializeResponse>;

// A minimal but well-formed response - every field is required by
// InitializeResponse, so a FakeClient that never calls scriptConnect() still
// resolves connect() with something valid rather than forcing every caller
// to script one just to get past the handshake.
const DEFAULT_INITIALIZE_RESPONSE: InitializeResponse = {
  serverInfo: { name: "fake-serf-hub", version: "0.0.0" },
  protocolVersion: "0",
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
}

export class FakeClient implements AppwireClientLike {
  state: ConnectionState;
  readonly calls: RecordedCall[] = [];

  // Keyed by method name and erased to a common handler shape here — `on`
  // and `request` always agree on M for a given key — since a heterogeneous
  // map can't otherwise hold a different result/param type per entry; both
  // generic methods below restore full per-method typing at the boundary.
  private readonly handlers = new Map<MethodName, RequestHandler<MethodName>>();
  private readonly notificationHandlers = new Set<(n: AnyNotification) => void>();
  private readonly readyHandlers = new Set<() => void>();
  private readonly stateChangeHandlers = new Set<(s: ConnectionState) => void>();

  // Deliberately independent of `state`/emitStateChange/emitReady below:
  // resolving connect() does not itself change `state`, so every existing
  // test that only drives readiness via the constructor/emitStateChange
  // (i.e. all of them, before this field existed) is unaffected. A test
  // that wants a state transition alongside a scripted connect() still
  // drives that explicitly, exactly as before.
  private connectHandler: ConnectHandler = () => DEFAULT_INITIALIZE_RESPONSE;

  // Defaults to "ready": tests overwhelmingly want a client stores can
  // request() against immediately, without separately staging the
  // idle -> connecting -> ready sequence a real handshake goes through. Pass
  // a different initial state explicitly to test pre-ready behavior.
  constructor(initialState: ConnectionState = "ready") {
    this.state = initialState;
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
  // request() below.
  connect(): Promise<InitializeResponse> {
    return Promise.resolve().then(() => this.connectHandler());
  }

  request<M extends MethodName>(method: M, params: MethodTypes[M]["params"]): Promise<MethodTypes[M]["result"]> {
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
    this.calls.push({ method, params });
    const handler = this.handlers.get(method);
    if (!handler) {
      return Promise.reject(new Error(`FakeClient: no handler scripted for "${method}"`));
    }
    // Deferred through a microtask like a real RPC round-trip, and lets a
    // synchronously-thrown handler become a normal rejection.
    return Promise.resolve().then(() => handler(params) as MethodTypes[M]["result"]);
  }

  onNotification(cb: (n: AnyNotification) => void): () => void {
    this.notificationHandlers.add(cb);
    return () => this.notificationHandlers.delete(cb);
  }

  onReady(cb: () => void): () => void {
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
  // real class in protocol/reconnect.test.ts, so this fake doesn't attempt
  // to model them (no timers, no sockets, nothing to short-circuit).
  retryNowCalls = 0;
  retryNow(): void {
    this.retryNowCalls += 1;
  }

  // --- test-side injection: simulates the server/transport side ---

  // emitNotification simulates one incoming wire notification.
  emitNotification(n: AnyNotification): void {
    for (const cb of Array.from(this.notificationHandlers)) cb(n);
  }

  // emitStateChange simulates the client transitioning to a new
  // ConnectionState, mirroring AppwireClient.setState: state-change
  // subscribers always run, and — exactly like the real class — transitioning
  // *into* "ready" additionally fires every onReady subscriber in the same
  // call, since that is the only path AppwireClient ever reaches ready
  // through (dialAndHandshake, on both the first connect and every
  // reconnect).
  emitStateChange(next: ConnectionState): void {
    if (this.state === next) return;
    this.state = next;
    for (const cb of Array.from(this.stateChangeHandlers)) cb(next);
    if (next === "ready") {
      for (const cb of Array.from(this.readyHandlers)) cb();
    }
  }

  // emitReady simulates a (re)connect succeeding — the common case tests
  // reach for — as a shorthand for emitStateChange("ready").
  emitReady(): void {
    this.emitStateChange("ready");
  }
}
