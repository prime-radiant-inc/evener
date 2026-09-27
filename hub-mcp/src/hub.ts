// The hub seam. Everything the tools do goes through HubPort, so tests drive
// the tool layer against a scripted fake and only the adapter in this file
// knows a real AppwireClient exists.

import type { AnyNotification, ConnectionState, MethodName, MethodTypes } from "@evener/appwire-client";
import { AppwireClient, WireError } from "@evener/appwire-client";
import WebSocket from "ws";

import type { HubConfig } from "./config.js";

export interface HubPort {
  request<M extends MethodName>(
    method: M,
    params: MethodTypes[M]["params"],
    opts?: { timeoutMs?: number },
  ): Promise<MethodTypes[M]["result"]>;
  onNotification(cb: (n: AnyNotification) => void): () => void;
  /** Fires on every successful (re)connect, after the handshake. */
  onReady(cb: () => void): () => void;
  connectionState(): ConnectionState;
  url(): string;
  /** The hub's identity from the last completed handshake, once connected. */
  info(): HubInfo | undefined;
  /** Close the hub connection and stop every timer the process holds. */
  close(): void;
}

export interface HubInfo {
  name: string;
  version: string;
  sourceId: string;
}

// HubUnavailableError names the connection problem in operator terms: the
// tools surface its message directly, so it must say what to check.
export class HubUnavailableError extends Error {
  constructor(url: string, state: ConnectionState, cause?: unknown) {
    const detail = cause instanceof Error ? `: ${cause.message}` : "";
    super(
      `the hub is not reachable at ${url} (connection state: ${state})${detail}. ` +
        `Check that evener hub is running and that EVENER_HUB_RPC_URL points at it.`,
    );
    this.name = "HubUnavailableError";
  }
}

// authenticatedSocket builds the WebSocketLike the SDK needs from a `ws`
// socket carrying the Bearer token. Node's platform WebSocket cannot set
// headers, which is why the adapter does not use it. Frames are normalized to
// strings because AppwireClient ignores anything else (client.ts handleMessage
// guards typeof data === "string").
function authenticatedSocket(url: string, token: string) {
  const sock = new WebSocket(url, { headers: { Authorization: `Bearer ${token}` } });
  let onopen: (() => void) | null = null;
  let onmessage: ((ev: { data: unknown }) => void) | null = null;
  let onclose: ((ev: { code: number }) => void) | null = null;
  let onerror: (() => void) | null = null;
  sock.on("open", () => onopen?.());
  sock.on("message", (data) => onmessage?.({ data: data.toString("utf8") }));
  sock.on("close", (code) => onclose?.({ code }));
  sock.on("error", () => onerror?.());
  return {
    send: (data: string) => sock.send(data),
    close: (code?: number) => sock.close(code),
    set onopen(cb: (() => void) | null) {
      onopen = cb;
    },
    set onmessage(cb: ((ev: { data: unknown }) => void) | null) {
      onmessage = cb;
    },
    set onclose(cb: ((ev: { code: number }) => void) | null) {
      onclose = cb;
    },
    set onerror(cb: (() => void) | null) {
      onerror = cb;
    },
  };
}

const CONNECT_TIMEOUT_MS = 10_000;

/**
 * AppwireHub adapts one lazily-connected AppwireClient to HubPort. The MCP
 * server's own initialize does not touch the hub, so tools/list works while
 * the hub is down; the first hub-touching call connects, and a reconnect after
 * a drop is automatic (the SDK's reconnect machinery) — callers re-subscribe
 * via onReady.
 */
export class AppwireHub implements HubPort {
  private readonly client: AppwireClient;
  private readonly hubUrl: string;
  private state: ConnectionState;
  private connecting: Promise<void> | null = null;
  private initializeInfo: HubInfo | undefined;

  constructor(config: HubConfig) {
    this.hubUrl = config.url;
    this.client = new AppwireClient({
      url: config.url,
      clientInfo: { name: "evener-hub-mcp", version: "0.1.0" },
      socketFactory: (url) => authenticatedSocket(url, config.token),
    });
    this.state = "idle";
    this.client.onStateChange((s) => {
      this.state = s;
    });
  }

  private async ensureConnected(): Promise<void> {
    if (this.state === "ready") return;
    if (this.state === "closed") {
      throw new HubUnavailableError(this.hubUrl, this.state);
    }
    if (this.connecting) return this.connecting;
    if (this.state === "reconnecting") return this.waitForReady();
    this.connecting = (async () => {
      try {
        const initialized = await this.client.connect();
        this.initializeInfo = {
          name: initialized.serverInfo.name,
          version: initialized.serverInfo.version,
          sourceId: initialized.sourceId,
        };
      } catch (err) {
        throw new HubUnavailableError(this.hubUrl, this.state, err);
      }
    })();
    try {
      await this.connecting;
    } finally {
      this.connecting = null;
    }
  }

  private waitForReady(): Promise<void> {
    return new Promise((resolve, reject) => {
      const timer = setTimeout(() => {
        stop();
        reject(new HubUnavailableError(this.hubUrl, this.state));
      }, CONNECT_TIMEOUT_MS);
      const stop = this.client.onReady(() => {
        clearTimeout(timer);
        stop();
        resolve();
      });
    });
  }

  async request<M extends MethodName>(
    method: M,
    params: MethodTypes[M]["params"],
    opts?: { timeoutMs?: number },
  ): Promise<MethodTypes[M]["result"]> {
    await this.ensureConnected();
    try {
      return await this.client.request(method, params, opts);
    } catch (err) {
      if (err instanceof WireError) throw err;
      if (this.state !== "ready") throw new HubUnavailableError(this.hubUrl, this.state, err);
      throw err;
    }
  }

  onNotification(cb: (n: AnyNotification) => void): () => void {
    return this.client.onNotification(cb);
  }

  onReady(cb: () => void): () => void {
    return this.client.onReady(cb);
  }

  connectionState(): ConnectionState {
    return this.state;
  }

  url(): string {
    return this.hubUrl;
  }

  info(): HubInfo | undefined {
    return this.initializeInfo;
  }

  close(): void {
    this.client.close();
  }
}
