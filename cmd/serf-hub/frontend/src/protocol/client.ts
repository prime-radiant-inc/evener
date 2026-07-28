// AppwireClient owns the websocket connection to the hub's /rpc endpoint: the
// initialize/initialized handshake, typed request/response correlation,
// notification fan-out, an application-level heartbeat, and automatic
// reconnect with backoff. It mirrors the message-handling, heartbeat, and
// reconnect semantics of the legacy cmd/serf-hub/assets/appwire.js
// (sendHeartbeat / ensureHeartbeat), but — unlike that fixed-250ms retry —
// backs off exponentially up to a cap.

import { ConnectionClosedError, RequestTimeoutError, WireError } from "./errors";
import type { WebSocketLike } from "./transport";
import type { AnyNotification, InitializeResponse, MethodName, MethodTypes } from "./types.gen";

export type { AnyNotification };

export type ConnectionState = "idle" | "connecting" | "ready" | "reconnecting" | "closed";

export interface AppwireClientOptions {
  url: string;
  socketFactory?: (url: string) => WebSocketLike; // default: real WebSocket
  now?: () => number; // default Date.now, tests inject
  clientInfo?: { name: string; version: string };
}

const DEFAULT_REQUEST_TIMEOUT_MS = 30_000;
const DEFAULT_CLIENT_INFO = { name: "serf-web", version: "0.1.0" };
const DEFAULT_CAPABILITIES = { experimentalApi: false };

// Same values as legacy appwire.js. Browsers can't send WebSocket ping
// frames from JS, so a silently-dropped connection leaves readyState OPEN
// forever with no notifications flowing; the heartbeat sends a cheap app-level
// `ping` on an interval and force-closes the socket if it goes unanswered.
export const HEARTBEAT_INTERVAL_MS = 20_000;
export const HEARTBEAT_TIMEOUT_MS = 10_000;

// Reconnect backoff: doubles from the base up to the cap, then holds there
// until a successful re-handshake. Unlike legacy appwire.js's fixed 250ms
// retry (renderer.js scheduleAppwireReconnect), this backs off so a
// prolonged outage doesn't hammer the hub with reconnect attempts.
export const RECONNECT_BASE_MS = 250;
export const RECONNECT_MAX_MS = 5_000;

// Methods allowed before the client reaches "ready": initialize is how it
// gets there, and ping is an app-level liveness probe the heartbeat needs to
// send even while connecting/reconnecting.
const READY_EXEMPT_METHODS: ReadonlySet<MethodName> = new Set<MethodName>(["initialize", "ping"]);

function defaultSocketFactory(url: string): WebSocketLike {
  // The DOM WebSocket type is structurally richer than WebSocketLike (its
  // event objects carry many more fields), so TS can't verify the assignment
  // even though every field WebSocketLike declares behaves compatibly at
  // runtime: send/close, and settable onopen/onmessage/onclose/onerror.
  return new WebSocket(url) as unknown as WebSocketLike;
}

interface WireErrorPayload {
  code: number;
  message: string;
  data?: unknown;
}

interface WireMessage {
  id?: number;
  method?: string;
  params?: unknown;
  result?: unknown;
  error?: WireErrorPayload;
}

interface PendingRequest {
  resolve: (result: unknown) => void;
  reject: (err: Error) => void;
  timer: ReturnType<typeof setTimeout>;
}

export class AppwireClient {
  private readonly url: string;
  private readonly socketFactory: (url: string) => WebSocketLike;
  // Heartbeat/reconnect scheduling here is interval-based (setInterval /
  // setTimeout), not timestamp-based, so `now` is unused; kept for callers
  // that want a controllable clock for other purposes (e.g. future
  // timestamp-stamped telemetry) without changing this constructor's shape.
  private readonly now: () => number;
  private readonly clientInfo: { name: string; version: string };

  private socket: WebSocketLike | null = null;
  private connectionState: ConnectionState = "idle";
  private nextId = 1;
  private readonly pending = new Map<number, PendingRequest>();
  // Set only while waitForOpen() is in flight (before the socket has opened
  // or failed). Lets close() abort a handshake that's stuck waiting for a
  // socket event, the same way `pending` lets it abort in-flight RPCs. Reused
  // for reconnect attempts (attemptReconnect), which dial and wait for open
  // exactly like the initial connect does.
  private handshakeReject: ((err: Error) => void) | null = null;
  private readonly notificationHandlers = new Set<(n: AnyNotification) => void>();
  private readonly stateChangeHandlers = new Set<(s: ConnectionState) => void>();
  private readonly readyHandlers = new Set<() => void>();
  private connectPromise: Promise<InitializeResponse> | null = null;

  // Heartbeat: one interval timer, armed on entering "ready" and disarmed on
  // leaving it (drop or close()). Its ping rides the same request()/pending
  // machinery as any other call, so the ping's own timeout self-cleans; no
  // separate "pong wait" timer is needed.
  private heartbeatIntervalTimer: ReturnType<typeof setInterval> | null = null;

  // Reconnect: one backoff timer at a time, armed by scheduleReconnect() and
  // disarmed the moment it fires (or by close()). reconnectAttempts counts
  // consecutive failures since the last time "ready" was reached, driving
  // the doubling delay; it resets to 0 on every successful re-handshake.
  private reconnectTimer: ReturnType<typeof setTimeout> | null = null;
  private reconnectAttempts = 0;
  // True for the duration of one attemptReconnect() call (dial through
  // settle). Only ever one attempt is normally in flight at a time - the
  // timer callback that starts one always nulls reconnectTimer first, and
  // scheduleReconnect() is only ever called after the previous attempt has
  // already settled - but retryNow() is a SECOND caller of attemptReconnect,
  // so this guards against a caller invoking it again while one it already
  // started is still waiting on its socket to open or its handshake to
  // finish.
  private reconnectInFlight = false;

  constructor(opts: AppwireClientOptions) {
    this.url = opts.url;
    this.socketFactory = opts.socketFactory ?? defaultSocketFactory;
    this.now = opts.now ?? Date.now;
    this.clientInfo = opts.clientInfo ?? DEFAULT_CLIENT_INFO;
  }

  get state(): ConnectionState {
    return this.connectionState;
  }

  // connect is idempotent: concurrent/repeated calls share the single
  // initialize+initialized handshake and its result.
  connect(): Promise<InitializeResponse> {
    if (!this.connectPromise) {
      if (this.isClosed()) {
        return Promise.reject(new ConnectionClosedError("AppwireClient: closed"));
      }
      this.connectPromise = this.performHandshake();
    }
    return this.connectPromise;
  }

  close(): void {
    if (this.connectionState === "closed") return;
    const socket = this.socket;
    this.socket = null;
    // Abort a handshake stuck in waitForOpen() before nulling the socket's
    // handlers below: those handlers are the only thing that would otherwise
    // ever settle that promise, so clearing them first would hang connect()
    // forever instead of rejecting it. Covers both the initial connect() and
    // a reconnect attempt, since attemptReconnect reuses waitForOpen.
    const abortHandshake = this.handshakeReject;
    this.handshakeReject = null;
    if (abortHandshake) {
      abortHandshake(new ConnectionClosedError("AppwireClient: closed"));
    }
    if (socket) {
      this.detachSocketHandlers(socket);
      try {
        socket.close();
      } catch {
        // Best-effort: the socket may already be closing.
      }
    }
    this.disarmHeartbeat();
    this.disarmReconnect();
    this.failAllPending(new ConnectionClosedError("AppwireClient: closed"));
    this.setState("closed");
  }

  request<M extends MethodName>(
    method: M,
    params: MethodTypes[M]["params"],
    opts?: { timeoutMs?: number },
  ): Promise<MethodTypes[M]["result"]> {
    if (this.connectionState !== "ready" && !READY_EXEMPT_METHODS.has(method)) {
      return Promise.reject(
        new Error(`AppwireClient: cannot call "${method}" while state is "${this.connectionState}"`),
      );
    }
    const socket = this.socket;
    if (!socket) {
      return Promise.reject(new Error(`AppwireClient: cannot call "${method}"; not connected`));
    }
    const timeoutMs = opts?.timeoutMs ?? DEFAULT_REQUEST_TIMEOUT_MS;
    const id = this.nextId++;
    return new Promise<MethodTypes[M]["result"]>((resolve, reject) => {
      const timer = setTimeout(() => {
        this.pending.delete(id);
        reject(new RequestTimeoutError(`AppwireClient: "${method}" timed out after ${timeoutMs}ms`));
      }, timeoutMs);
      this.pending.set(id, { resolve: resolve as (result: unknown) => void, reject, timer });
      try {
        socket.send(JSON.stringify({ id, method, params }));
      } catch (err) {
        clearTimeout(timer);
        this.pending.delete(id);
        reject(err instanceof Error ? err : new Error(String(err)));
      }
    });
  }

  onNotification(cb: (n: AnyNotification) => void): () => void {
    this.notificationHandlers.add(cb);
    return () => {
      this.notificationHandlers.delete(cb);
    };
  }

  onStateChange(cb: (s: ConnectionState) => void): () => void {
    this.stateChangeHandlers.add(cb);
    return () => {
      this.stateChangeHandlers.delete(cb);
    };
  }

  // onReady fires on every transition into "ready", including future
  // reconnects.
  onReady(cb: () => void): () => void {
    this.readyHandlers.add(cb);
    return () => {
      this.readyHandlers.delete(cb);
    };
  }

  // retryNow lets a caller (ConnectionBanner's manual "Retry now"
  // affordance, shown only while "reconnecting") short-circuit the current
  // backoff wait and dial immediately, rather than starting a second,
  // independent reconnect mechanism. No-op unless a backoff is actually
  // pending (state isn't "reconnecting") or an attempt - from an earlier
  // retryNow() call or the backoff timer itself - is already in flight.
  // Deliberately does NOT reset reconnectAttempts: this is "try the next
  // attempt now instead of after the wait," not "start the whole backoff
  // sequence over" - if this attempt also fails, scheduleReconnect()
  // computes its delay from the same count it would have anyway.
  retryNow(): void {
    if (this.connectionState !== "reconnecting" || this.reconnectInFlight) return;
    this.disarmReconnect();
    void this.attemptReconnect();
  }

  private async performHandshake(): Promise<InitializeResponse> {
    this.setState("connecting");
    try {
      return await this.dialAndHandshake();
    } catch (err) {
      // Covers both a socket that never opened and a server-rejected
      // initialize: either way the FIRST-EVER handshake failed, so this is a
      // terminal failure — there is no prior "ready" to reconnect back to,
      // and connect() must reject rather than retry silently.
      this.teardownFailedSocket();
      this.failAllPending(err instanceof Error ? err : new Error(String(err)));
      this.setState("closed");
      throw err;
    }
  }

  // dialAndHandshake dials a fresh socket and runs it through
  // open -> initialize -> initialized -> ready, arming the heartbeat on
  // success. Shared by the initial connect() (performHandshake) and every
  // reconnect attempt (attemptReconnect); callers decide what a failure
  // means for connection lifecycle (terminal close vs. another backoff).
  //
  // Both setState() calls in here dispatch synchronously to
  // onStateChange/onReady subscribers, and a subscriber calling close()
  // reentrantly runs it to completion — tears down, disarms, and reaches
  // "closed" — *before* control returns to the line right after that
  // setState() call. The two isClosed() checks below are load-bearing, not
  // defensive: without the first, the caller (performHandshake, on the very
  // first connect) would still dial a socket that close() can never clean up
  // again; without the second, "ready" would still arm a heartbeat interval
  // on a client that just closed.
  private async dialAndHandshake(): Promise<InitializeResponse> {
    if (this.isClosed()) {
      throw new ConnectionClosedError("AppwireClient: closed");
    }
    const socket = this.socketFactory(this.url);
    this.socket = socket;
    socket.onmessage = (ev) => this.handleMessage(ev.data);

    await this.waitForOpen(socket);
    socket.onerror = () => this.handleSocketError();
    socket.onclose = (ev) => this.handleSocketLoss(socket, ev.code);

    const result = await this.request("initialize", {
      clientInfo: this.clientInfo,
      capabilities: DEFAULT_CAPABILITIES,
    });
    this.sendFrame({ method: "initialized", params: {} });
    this.setState("ready");
    // The handshake itself genuinely succeeded, so this still resolves with
    // `result` even if a reentrant close() just ran: only the side effect
    // (arming a timer this client will never get to disarm again) is guarded.
    if (!this.isClosed()) this.armHeartbeat();
    return result;
  }

  // teardownFailedSocket clears a socket that dialAndHandshake failed to
  // bring up. Reads `this.socket` rather than a caller-supplied reference:
  // handshakes never overlap, so whatever dialAndHandshake most recently
  // assigned is the one that just failed (or, if close() already cleared it
  // first, there is nothing left to tear down).
  private teardownFailedSocket(): void {
    const socket = this.socket;
    this.socket = null;
    if (socket) {
      this.detachSocketHandlers(socket);
      try {
        socket.close();
      } catch {
        // Best-effort: the socket may already be closing.
      }
    }
  }

  // scheduleReconnect arms the next backoff timer and marks the connection
  // "reconnecting". Called once when a ready connection drops (from
  // handleSocketClose) and again after each failed attempt (from
  // attemptReconnect), so the delay it computes doubles attempt over
  // attempt, capped at RECONNECT_MAX_MS, until one finally succeeds.
  private scheduleReconnect(): void {
    if (this.isClosed()) return;
    const delay = Math.min(RECONNECT_BASE_MS * 2 ** this.reconnectAttempts, RECONNECT_MAX_MS);
    this.reconnectAttempts += 1;
    this.reconnectTimer = setTimeout(() => {
      this.reconnectTimer = null;
      void this.attemptReconnect();
    }, delay);
    // Publish only after reconnectTimer owns this attempt. State listeners
    // run synchronously and may call retryNow() or close(); both must be able
    // to disarm this timer before starting or ending connection work.
    this.setState("reconnecting");
  }

  // attemptReconnect re-dials and re-handshakes exactly like the initial
  // connect, via the same dialAndHandshake (so close() can abort it the same
  // way too, via handshakeReject). A failure here never surfaces to a
  // caller — there is no pending connect() promise to reject once already
  // past the first "ready" — it just schedules the next backoff attempt.
  // Two callers can reach this: the backoff timer firing, and retryNow() -
  // reconnectInFlight (set for this call's whole duration) is what keeps a
  // second, concurrent call from either caller from dialing a second socket
  // on top of this one.
  private async attemptReconnect(): Promise<void> {
    if (this.connectionState !== "reconnecting" || this.reconnectInFlight) return;
    this.reconnectInFlight = true;
    try {
      await this.dialAndHandshake();
      this.reconnectAttempts = 0;
    } catch {
      // close() already moved to "closed" and disarmed reconnection (it ran
      // synchronously before this rejection could be observed here): honor
      // that instead of scheduling another attempt on a closed client.
      if (this.isClosed()) return;
      this.teardownFailedSocket();
      this.scheduleReconnect();
    } finally {
      this.reconnectInFlight = false;
    }
  }

  // isClosed reads connectionState through a method call rather than
  // comparing the property directly. Two independent reasons make that the
  // right idiom everywhere this class re-checks "closed" after doing
  // something that could let close() run in between:
  //   - Across an await (attemptReconnect's catch): plain
  //     "this.connectionState === 'closed'" narrowing doesn't know close()
  //     can run, via an event handler, during the intervening await — it
  //     would otherwise consider "closed" impossible by the time the second
  //     check runs, which is a TypeScript false negative, not a real
  //     guarantee.
  //   - Across a synchronous setState() call (dialAndHandshake,
  //     scheduleReconnect): setState() dispatches to onStateChange/onReady
  //     subscribers *synchronously*, and a subscriber calling close()
  //     reentrantly runs it to completion before setState() returns. A
  //     direct property comparison would still be correct here (no TS
  //     narrowing hazard), but the method call keeps every one of these
  //     "did close() just happen underneath me" checks in one idiom.
  private isClosed(): boolean {
    return this.connectionState === "closed";
  }

  private armHeartbeat(): void {
    this.disarmHeartbeat();
    this.heartbeatIntervalTimer = setInterval(() => this.sendHeartbeat(), HEARTBEAT_INTERVAL_MS);
  }

  private disarmHeartbeat(): void {
    if (this.heartbeatIntervalTimer == null) return;
    clearInterval(this.heartbeatIntervalTimer);
    this.heartbeatIntervalTimer = null;
  }

  private disarmReconnect(): void {
    if (this.reconnectTimer == null) return;
    clearTimeout(this.reconnectTimer);
    this.reconnectTimer = null;
  }

  // sendHeartbeat sends one app-level ping with an explicit HEARTBEAT_TIMEOUT_MS
  // deadline (reusing request()'s own timeout machinery rather than a second,
  // separately-tracked timer). An open-but-unresponsive socket never recovers
  // on its own, so any failure to answer in time retires it immediately and
  // starts the same reconnect lifecycle as a server-initiated drop. close()
  // remains best-effort cleanup: a half-open transport may never emit onclose.
  private sendHeartbeat(): void {
    if (this.connectionState !== "ready") return;
    const socket = this.socket;
    if (!socket) return;
    this.request("ping", {}, { timeoutMs: HEARTBEAT_TIMEOUT_MS }).catch(() => {
      // Ignore rejections caused by an unrelated disconnect that already
      // moved the socket on: only retire it if this ping's own socket is still
      // the live one.
      if (this.socket !== socket || this.connectionState !== "ready") return;
      this.handleSocketLoss(socket, 1006);
      try {
        socket.close();
      } catch {
        // Best-effort: the socket may already be closing.
      }
    });
  }

  private waitForOpen(socket: WebSocketLike): Promise<void> {
    return new Promise<void>((resolve, reject) => {
      const settle = (err: Error | null) => {
        this.handshakeReject = null;
        if (err) reject(err);
        else resolve();
      };
      this.handshakeReject = (err) => settle(err);
      socket.onopen = () => settle(null);
      socket.onerror = () => settle(new Error("AppwireClient: socket error while connecting"));
      socket.onclose = (ev) => settle(new Error(`AppwireClient: socket closed while connecting (code ${ev.code})`));
    });
  }

  private handleSocketError(): void {
    // WebSocket errors are always followed by a close event per spec (and
    // FakeSocket has no separate error path to simulate), so this stays a
    // no-op: the close handler below performs the actual state transition,
    // pending cleanup, and — while ready — reconnect scheduling.
  }

  private handleSocketLoss(socket: WebSocketLike, code: number): void {
    // A client-retired socket may emit onclose after its replacement is
    // already live. Only the socket that still owns the connection can move
    // lifecycle state or reject pending requests.
    if (this.socket !== socket || this.connectionState === "closed") return;
    this.socket = null;
    this.detachSocketHandlers(socket);
    this.failAllPending(new Error(`AppwireClient: socket closed (code ${code})`));
    if (this.connectionState === "ready") {
      // A previously-healthy connection just dropped (server-initiated close,
      // or sendHeartbeat's retirement after an unanswered ping): try to get
      // back, rather than treating this as terminal.
      this.disarmHeartbeat();
      this.scheduleReconnect();
      return;
    }
    if (this.connectionState === "connecting") {
      // The FIRST-EVER handshake's socket dropped before ever reaching
      // ready: terminal, same as before reconnect existed. (A drop mid
      // *reconnect* attempt also passes through here with connectionState
      // still "reconnecting" — deliberately falls through to do nothing
      // beyond the failAllPending above, since attemptReconnect's own
      // try/catch is what schedules that case's next backoff.)
      this.setState("closed");
    }
  }

  private detachSocketHandlers(socket: WebSocketLike): void {
    socket.onopen = null;
    socket.onmessage = null;
    socket.onclose = null;
    socket.onerror = null;
  }

  private handleMessage(data: unknown): void {
    if (typeof data !== "string") return;
    let msg: WireMessage;
    try {
      msg = JSON.parse(data) as WireMessage;
    } catch {
      return;
    }
    if (msg.id != null) {
      const slot = this.pending.get(msg.id);
      if (!slot) return;
      this.pending.delete(msg.id);
      clearTimeout(slot.timer);
      if (msg.error) {
        slot.reject(new WireError(msg.error.message ?? "appwire error", msg.error.code, msg.error.data));
      } else {
        slot.resolve(msg.result ?? {});
      }
      return;
    }
    if (msg.method) {
      const notification = { method: msg.method, params: msg.params ?? {} } as AnyNotification;
      for (const handler of Array.from(this.notificationHandlers)) {
        try {
          handler(notification);
        } catch {
          // A misbehaving subscriber must not stop dispatch to the rest.
        }
      }
    }
  }

  private sendFrame(frame: { method: string; params: unknown }): void {
    this.socket?.send(JSON.stringify(frame));
  }

  private failAllPending(err: Error): void {
    for (const slot of this.pending.values()) {
      clearTimeout(slot.timer);
      slot.reject(err);
    }
    this.pending.clear();
  }

  private setState(next: ConnectionState): void {
    if (this.connectionState === next) return;
    this.connectionState = next;
    for (const cb of Array.from(this.stateChangeHandlers)) {
      try {
        cb(next);
      } catch {
        // A misbehaving subscriber must not corrupt the state machine or
        // abort whatever operation (e.g. a successful handshake, since
        // setState runs inside performHandshake's try) triggered this
        // transition.
      }
    }
    if (next === "ready") {
      for (const cb of Array.from(this.readyHandlers)) {
        try {
          cb();
        } catch {
          // See above.
        }
      }
    }
  }
}
