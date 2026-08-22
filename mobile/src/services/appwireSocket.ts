/**
 * AppWire transport service — a Tauri-backed `WebSocketLike` and an
 * `AppwireClient` factory that dials the Rust `appwire_open`/`appwire_send`/
 * `appwire_close` commands in `mobile/src-tauri/src/commands.rs`.
 *
 * The Rust layer holds the token, pins the origin via NetworkPolicy, and
 * relays server frames to JavaScript through a Tauri `Channel` as redacted
 * `AppwireChannelEvent`s (`text`/`closed`/`error`). This module adapts that
 * channel to the `WebSocketLike` surface the headless `AppwireClient` expects,
 * so the mobile app reuses the exact same protocol client as the web app —
 * only the socket factory differs. No service outside `tauri.ts` imports
 * `@tauri-apps/api`.
 */

import { AppwireClient } from "../../../cmd/evener-hub/frontend/src/protocol/client";

import type { WebSocketLike } from "../../../cmd/evener-hub/frontend/src/protocol/transport";
import type { TauriBridge, TauriChannel } from "./tauri";

// ---------------------------------------------------------------------------
// Channel events — mirror the Rust `AppwireChannelEvent` (camelCase, tag=type)
// The Rust enum serializes as `{type:"text",data}`, `{type:"closed",code}`,
// or `{type:"error"}`. Never carries the token or URL.
// ---------------------------------------------------------------------------

export type AppwireChannelEvent =
  | { readonly type: "text"; readonly data: string }
  | { readonly type: "closed"; readonly code: number }
  | { readonly type: "error" };

// ---------------------------------------------------------------------------
// Open response — mirror the Rust `AppwireOpenResponse` (camelCase)
// ---------------------------------------------------------------------------

interface AppwireOpenResponse {
  readonly connectionId: string;
  readonly generation: number;
}

// ---------------------------------------------------------------------------
// TauriSocket — implements WebSocketLike over Tauri invoke + Channel
// ---------------------------------------------------------------------------

/**
 * A `WebSocketLike` backed by Tauri AppWire commands. The `url` argument is
 * ignored: the Rust side derives the WebSocket URL from the selected profile's
 * origin, so the client never sees the URL or token.
 */
class TauriSocket implements WebSocketLike {
  onopen: (() => void) | null = null;
  onmessage: ((ev: { data: unknown }) => void) | null = null;
  onclose: ((ev: { code: number }) => void) | null = null;
  onerror: (() => void) | null = null;

  private readonly bridge: TauriBridge;
  private readonly profileId: string;
  private readonly channel: TauriChannel<AppwireChannelEvent>;
  private connectionId: string | null = null;
  private opened = false;
  private closed = false;
  private opening: Promise<void>;

  constructor(bridge: TauriBridge, profileId: string, _url: string) {
    this.bridge = bridge;
    this.profileId = profileId;
    // Create the channel before opening so server frames are never dropped.
    this.channel = bridge.createChannel<AppwireChannelEvent>((event) =>
      this.handleEvent(event),
    );
    this.opening = this.open();
  }

  private async open(): Promise<void> {
    try {
      const res = await this.bridge.invoke<AppwireOpenResponse>(
        "appwire_open",
        {
          profileId: this.profileId,
          onEvent: this.channel,
        },
      );
      if (this.closed) return;
      this.connectionId = res.connectionId;
      this.opened = true;
      this.onopen?.();
    } catch {
      if (this.closed) return;
      this.onerror?.();
    }
  }

  private handleEvent(event: AppwireChannelEvent): void {
    if (this.closed) return;
    switch (event.type) {
      case "text":
        this.onmessage?.({ data: event.data });
        break;
      case "closed":
        this.markClosed();
        this.onclose?.({ code: event.code });
        break;
      case "error":
        this.onerror?.();
        break;
    }
  }

  private markClosed(): void {
    this.closed = true;
  }

  send(data: string): void {
    if (!this.opened || this.closed || this.connectionId === null) {
      // Before open completes or after close, sends are dropped. A send whose
      // backend rejects (stale connection) surfaces via the error event.
      return;
    }
    void this.bridge
      .invoke("appwire_send", {
        connectionId: this.connectionId,
        frame: data,
      })
      .catch(() => {
        // A rejected send (e.g. stale connection after a profile switch) is
        // reported as an error, mirroring a transport failure.
        if (!this.closed) this.onerror?.();
      });
  }

  close(code?: number): void {
    if (this.closed) return;
    this.closed = true;
    const connId = this.connectionId;
    // Ensure the open promise has settled before reporting close so onclose
    // never fires before onopen/error.
    void this.opening.finally(() => {
      if (connId !== null) {
        void this.bridge
          .invoke("appwire_close", { connectionId: connId })
          .catch(() => {
            // Best-effort: the connection may already be closed server-side.
          });
      }
      this.onclose?.({ code: code ?? 1000 });
    });
  }
}

// ---------------------------------------------------------------------------
// Socket factory — used by AppwireClient
// ---------------------------------------------------------------------------

export function createAppwireSocketFactory(
  bridge: TauriBridge,
  profileId: string,
): (url: string) => WebSocketLike {
  return (url: string) => new TauriSocket(bridge, profileId, url);
}

// ---------------------------------------------------------------------------
// AppwireClient factory — mobile clientInfo
// ---------------------------------------------------------------------------

export interface CreateAppwireClientInput {
  readonly bridge: TauriBridge;
  readonly url: string;
  readonly profileId: string;
  readonly now?: () => number;
}

/** The mobile AppWire client. clientInfo identifies the app to the Hub. */
export const MOBILE_CLIENT_INFO = {
  name: "evener-mobile",
  version: "0.1.0",
} as const;

export type AppwireClientWithUrl = InstanceType<typeof AppwireClient>;

export function createAppwireClient(
  input: CreateAppwireClientInput,
): AppwireClientWithUrl {
  return new AppwireClient({
    url: input.url,
    socketFactory: createAppwireSocketFactory(input.bridge, input.profileId),
    now: input.now,
    clientInfo: { ...MOBILE_CLIENT_INFO },
  });
}
