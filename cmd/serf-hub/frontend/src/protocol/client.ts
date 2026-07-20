// AppwireClient owns the websocket connection to the hub's /rpc endpoint: the
// initialize/initialized handshake, typed request/response correlation, and
// notification fan-out. It mirrors the message-handling semantics of the
// legacy cmd/serf-hub/assets/appwire.js. Heartbeat and reconnect are added on
// top of this in a later change; this client only defines the state machine
// and close handling those need, without any timers beyond request timeouts.
import type { WebSocketLike } from "./transport";
import { ConnectionClosedError, RequestTimeoutError, WireError } from "./errors";
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
  // Reserved for heartbeat/reconnect timestamps in a later change; unused
  // until then.
  private readonly now: () => number;
  private readonly clientInfo: { name: string; version: string };

  private socket: WebSocketLike | null = null;
  private connectionState: ConnectionState = "idle";
  private nextId = 1;
  private readonly pending = new Map<number, PendingRequest>();
  // Set only while waitForOpen() is in flight (before the socket has opened
  // or failed). Lets close() abort a handshake that's stuck waiting for a
  // socket event, the same way `pending` lets it abort in-flight RPCs.
  private handshakeReject: ((err: Error) => void) | null = null;
  private readonly notificationHandlers = new Set<(n: AnyNotification) => void>();
  private readonly stateChangeHandlers = new Set<(s: ConnectionState) => void>();
  private readonly readyHandlers = new Set<() => void>();
  private connectPromise: Promise<InitializeResponse> | null = null;

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
    // forever instead of rejecting it.
    const abortHandshake = this.handshakeReject;
    this.handshakeReject = null;
    if (abortHandshake) {
      abortHandshake(new ConnectionClosedError("AppwireClient: closed"));
    }
    if (socket) {
      socket.onopen = null;
      socket.onmessage = null;
      socket.onclose = null;
      socket.onerror = null;
      try {
        socket.close();
      } catch {
        // Best-effort: the socket may already be closing.
      }
    }
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

  private async performHandshake(): Promise<InitializeResponse> {
    this.setState("connecting");
    const socket = this.socketFactory(this.url);
    this.socket = socket;
    socket.onmessage = (ev) => this.handleMessage(ev.data);

    try {
      await this.waitForOpen(socket);
      socket.onerror = () => this.handleSocketError();
      socket.onclose = (ev) => this.handleSocketClose(ev.code);

      const result = await this.request("initialize", {
        clientInfo: this.clientInfo,
        capabilities: DEFAULT_CAPABILITIES,
      });
      this.sendFrame({ method: "initialized", params: {} });
      this.setState("ready");
      return result;
    } catch (err) {
      // Covers both a socket that never opened and a server-rejected
      // initialize: either way the handshake failed, so tear down the
      // (possibly still-open) socket and fail anything left pending on it.
      this.socket = null;
      try {
        socket.close();
      } catch {
        // Best-effort: the socket may already be closing.
      }
      this.failAllPending(err instanceof Error ? err : new Error(String(err)));
      this.setState("closed");
      throw err;
    }
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
      socket.onclose = (ev) =>
        settle(new Error(`AppwireClient: socket closed while connecting (code ${ev.code})`));
    });
  }

  private handleSocketError(): void {
    // WebSocket errors are always followed by a close event per spec; the
    // close handler performs the actual state transition and pending cleanup.
  }

  private handleSocketClose(code: number): void {
    if (this.connectionState === "closed") return;
    this.socket = null;
    this.failAllPending(new Error(`AppwireClient: socket closed (code ${code})`));
    this.setState("closed");
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
