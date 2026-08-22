/**
 * Tauri-backed AppWire `WebSocketLike` adapter. Rust owns credentials and the
 * upstream socket; this adapter strictly decodes identity-bearing channel
 * events and rejects stale queued events before they reach AppwireClient.
 */

import { AppwireClient } from "../../../cmd/evener-hub/frontend/src/protocol/client";

import type { WebSocketLike } from "../../../cmd/evener-hub/frontend/src/protocol/transport";
import type { TauriBridge, TauriChannel } from "./tauri";

interface EventIdentity {
  readonly connectionId: string;
  readonly profileId: string;
  readonly generation: number;
}

export type AppwireChannelEvent =
  | (EventIdentity & { readonly type: "text"; readonly data: string })
  | (EventIdentity & {
      readonly type: "closed";
      readonly code: number;
      readonly reason: string;
    })
  | (EventIdentity & { readonly type: "error" });

interface AppwireOpenResponse extends EventIdentity {}

function record(value: unknown): Record<string, unknown> {
  if (typeof value !== "object" || value === null || Array.isArray(value)) {
    throw new Error("invalid AppWire payload");
  }
  return value as Record<string, unknown>;
}

function hasExactKeys(
  value: Record<string, unknown>,
  keys: readonly string[],
): boolean {
  const actual = Object.keys(value).sort();
  const expected = [...keys].sort();
  return (
    actual.length === expected.length &&
    actual.every((key, index) => key === expected[index])
  );
}

function identity(value: Record<string, unknown>): EventIdentity {
  if (
    typeof value.connectionId !== "string" ||
    value.connectionId.length === 0 ||
    typeof value.profileId !== "string" ||
    value.profileId.length === 0 ||
    typeof value.generation !== "number" ||
    !Number.isSafeInteger(value.generation) ||
    value.generation < 1
  ) {
    throw new Error("invalid AppWire identity");
  }
  return {
    connectionId: value.connectionId,
    profileId: value.profileId,
    generation: value.generation,
  };
}

function decodeOpenResponse(
  value: unknown,
  expectedProfileId: string,
): AppwireOpenResponse {
  const decoded = record(value);
  if (!hasExactKeys(decoded, ["connectionId", "profileId", "generation"])) {
    throw new Error("invalid AppWire open response");
  }
  const result = identity(decoded);
  if (result.profileId !== expectedProfileId) {
    throw new Error("AppWire profile mismatch");
  }
  return result;
}

/** Strict Rust DTO decoder. Unknown fields and incomplete identities fail closed. */
export function decodeAppwireChannelEvent(value: unknown): AppwireChannelEvent {
  const decoded = record(value);
  const type = decoded.type;
  if (type === "text") {
    if (
      !hasExactKeys(decoded, [
        "type",
        "connectionId",
        "profileId",
        "generation",
        "data",
      ]) ||
      typeof decoded.data !== "string"
    ) {
      throw new Error("invalid AppWire text event");
    }
    return { type, ...identity(decoded), data: decoded.data };
  }
  if (type === "closed") {
    if (
      !hasExactKeys(decoded, [
        "type",
        "connectionId",
        "profileId",
        "generation",
        "code",
        "reason",
      ]) ||
      typeof decoded.code !== "number" ||
      !Number.isInteger(decoded.code) ||
      decoded.code < 0 ||
      decoded.code > 65535 ||
      typeof decoded.reason !== "string"
    ) {
      throw new Error("invalid AppWire closed event");
    }
    return {
      type,
      ...identity(decoded),
      code: decoded.code,
      reason: decoded.reason,
    };
  }
  if (type === "error") {
    if (
      !hasExactKeys(decoded, [
        "type",
        "connectionId",
        "profileId",
        "generation",
      ])
    ) {
      throw new Error("invalid AppWire error event");
    }
    return { type, ...identity(decoded) };
  }
  throw new Error("unknown AppWire event");
}

class TauriSocket implements WebSocketLike {
  onopen: (() => void) | null = null;
  onmessage: ((ev: { data: unknown }) => void) | null = null;
  onclose: ((ev: { code: number }) => void) | null = null;
  onerror: (() => void) | null = null;

  private readonly bridge: TauriBridge;
  private readonly profileId: string;
  private readonly channel: TauriChannel<unknown>;
  private readonly queuedEvents: AppwireChannelEvent[] = [];
  private connectionId: string | null = null;
  private generation: number | null = null;
  private opened = false;
  private closed = false;
  private backendCloseStarted = false;
  private closeNotified = false;
  private closeCode = 1000;
  private readonly opening: Promise<void>;

  constructor(bridge: TauriBridge, profileId: string, _url: string) {
    this.bridge = bridge;
    this.profileId = profileId;
    this.channel = bridge.createChannel<unknown>((event) =>
      this.receiveEvent(event),
    );
    this.opening = this.open();
  }

  private async open(): Promise<void> {
    try {
      const raw = await this.bridge.invoke<unknown>("appwire_open", {
        profileId: this.profileId,
        onEvent: this.channel,
      });
      const response = decodeOpenResponse(raw, this.profileId);
      // Capture identity even after frontend close. finishClose() needs it to
      // close the backend socket created by the resolved open exactly once.
      this.connectionId = response.connectionId;
      this.generation = response.generation;
      if (this.closed) return;

      this.opened = true;
      this.onopen?.();
      const queued = this.queuedEvents.splice(0);
      for (const event of queued) this.deliverEvent(event);
    } catch {
      if (!this.closed) this.onerror?.();
    }
  }

  private receiveEvent(raw: unknown): void {
    if (this.closed) return;
    let event: AppwireChannelEvent;
    try {
      event = decodeAppwireChannelEvent(raw);
    } catch {
      this.onerror?.();
      return;
    }
    if (this.connectionId === null || this.generation === null) {
      this.queuedEvents.push(event);
      return;
    }
    this.deliverEvent(event);
  }

  private deliverEvent(event: AppwireChannelEvent): void {
    if (this.closed || !this.matchesCurrent(event)) return;
    switch (event.type) {
      case "text":
        this.onmessage?.({ data: event.data });
        break;
      case "closed":
        this.closed = true;
        this.closeCode = event.code;
        this.notifyClose();
        break;
      case "error":
        this.onerror?.();
        break;
    }
  }

  private matchesCurrent(event: EventIdentity): boolean {
    return (
      event.connectionId === this.connectionId &&
      event.profileId === this.profileId &&
      event.generation === this.generation
    );
  }

  send(data: string): void {
    if (!this.opened || this.closed || this.connectionId === null) return;
    void this.bridge
      .invoke("appwire_send", {
        connectionId: this.connectionId,
        frame: data,
      })
      .catch(() => {
        if (!this.closed) this.onerror?.();
      });
  }

  close(code?: number): void {
    if (this.closed) return;
    this.closed = true;
    this.closeCode = code ?? 1000;
    void this.finishClose();
  }

  private async finishClose(): Promise<void> {
    // Await the actual open completion. If it resolved after close(), open()
    // captured the backend connection ID without dispatching any handler.
    await this.opening.catch(() => undefined);
    if (this.connectionId !== null && !this.backendCloseStarted) {
      this.backendCloseStarted = true;
      await this.bridge
        .invoke("appwire_close", { connectionId: this.connectionId })
        .catch(() => undefined);
    }
    this.queuedEvents.length = 0;
    this.notifyClose();
  }

  private notifyClose(): void {
    if (this.closeNotified) return;
    this.closeNotified = true;
    this.onclose?.({ code: this.closeCode });
  }
}

export function createAppwireSocketFactory(
  bridge: TauriBridge,
  profileId: string,
): (url: string) => WebSocketLike {
  return (url: string) => new TauriSocket(bridge, profileId, url);
}

export interface CreateAppwireClientInput {
  readonly bridge: TauriBridge;
  readonly url: string;
  readonly profileId: string;
  readonly now?: () => number;
}

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
