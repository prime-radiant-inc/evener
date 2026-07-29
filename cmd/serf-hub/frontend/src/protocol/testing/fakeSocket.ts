// FakeSocket is a WebSocketLike test double for client.test.ts. It records
// every frame the client sends and gives tests three ways to drive the
// client from the "server" side: open() to simulate the connection opening,
// receive(obj) to simulate an incoming frame, and closeFromServer() to
// simulate the server closing the connection unprompted.
import type { WebSocketLike } from "../transport";
import type { InitializeResponse } from "../types.gen";

// FAKE_INITIALIZE_RESULT is the canned success response FakeSocket sends
// when autoInitialize replies to an "initialize" request. Exported so tests
// can assert AppwireClient.connect() resolves with exactly this value
// without duplicating the literal.
export const FAKE_INITIALIZE_RESULT: InitializeResponse = {
  serverInfo: { name: "fake-serf-hub", version: "0.0.0-test" },
  protocolVersion: "serf-appwire-v2",
  sourceId: "fake-source",
  features: {
    threadList: true,
    threadTurnsList: true,
    turnStart: true,
    turnSteer: true,
    threadClear: true,
    threadShutdown: true,
    forkFromTurn: true,
    tasks: true,
    transcriptList: true,
    modelList: true,
    directoryComplete: true,
    auth: true,
  },
};

export interface FakeSocketOptions {
  autoInitialize?: boolean;
  emitCloseEventOnClientClose?: boolean;
}

export class FakeSocket implements WebSocketLike {
  autoInitialize: boolean;
  readonly sent: string[] = [];
  readonly closeRequests: Array<number | undefined> = [];
  onopen: (() => void) | null = null;
  onmessage: ((ev: { data: unknown }) => void) | null = null;
  onclose: ((ev: { code: number }) => void) | null = null;
  onerror: (() => void) | null = null;

  private isClosed = false;
  private clientClosePending = false;
  private readonly emitCloseEventOnClientClose: boolean;

  constructor(options: FakeSocketOptions = {}) {
    this.autoInitialize = options.autoInitialize ?? false;
    this.emitCloseEventOnClientClose = options.emitCloseEventOnClientClose ?? true;
  }

  send(data: string): void {
    this.sent.push(data);
    if (!this.autoInitialize) return;
    // Auto-reply to the two requests a client sends without an explicit
    // test-scripted response: the handshake's "initialize" and the
    // heartbeat's "ping" liveness probe.
    const msg = JSON.parse(data) as { id?: number; method?: string };
    if (msg.method === "initialize" && msg.id != null) {
      this.receive({ id: msg.id, result: FAKE_INITIALIZE_RESULT });
    } else if (msg.method === "ping" && msg.id != null) {
      this.receive({ id: msg.id, result: {} });
    }
  }

  close(code?: number): void {
    this.closeRequests.push(code);
    if (!this.emitCloseEventOnClientClose) {
      this.clientClosePending = true;
      return;
    }
    this.closeInternal(code ?? 1000);
  }

  // open simulates the underlying transport finishing its connect handshake.
  open(): void {
    this.onopen?.();
  }

  // receive simulates a single incoming text frame from the server.
  receive(obj: unknown): void {
    this.onmessage?.({ data: JSON.stringify(obj) });
  }

  // closeFromServer simulates the server closing the connection, unprompted
  // by a client-side close() call.
  closeFromServer(code: number): void {
    this.closeInternal(code);
  }

  // Completes a client-initiated close that was configured not to emit its
  // event synchronously, matching a browser delivering it after a replacement
  // transport is already live.
  finishClientClose(code = 1000): void {
    if (!this.clientClosePending) throw new Error("FakeSocket: no client close request is pending");
    this.clientClosePending = false;
    this.closeInternal(code);
  }

  private closeInternal(code: number): void {
    if (this.isClosed) return;
    this.isClosed = true;
    this.onclose?.({ code });
  }
}
