import { describe, expect, it, vi } from "vitest";

import type { AppwireChannelEvent } from "./appwireSocket";
import {
  type AppwireClientWithUrl,
  createAppwireClient,
  createAppwireSocketFactory,
  decodeAppwireChannelEvent,
} from "./appwireSocket";
import type { TauriBridge, TauriChannel } from "./tauri";

// ---------------------------------------------------------------------------
// Fake TauriBridge — captures the event channel and scripts invoke results
// ---------------------------------------------------------------------------

interface OpenScript {
  connectionId: string;
  generation: number;
  profileId?: string;
  error?: string;
  result?: Promise<{
    connectionId: string;
    profileId: string;
    generation: number;
  }>;
}

interface FakeBridge {
  invoke: <T>(cmd: string, args?: Record<string, unknown>) => Promise<T>;
  createChannel: <T>(onMessage: (response: T) => void) => {
    id: number;
    onmessage: (response: T) => void;
    onclose: (() => void) | null;
    dispose(): void;
  };
  /** Captured channel emit helper (typed for AppwireChannelEvent). */
  emit: (event: AppwireChannelEvent) => void;
  readonly invokes: { cmd: string; args: Record<string, unknown> }[];
  /** Script the send result/error per connectionId. */
  sendResult: (connId: string, ok: boolean, error?: string) => void;
  /** Script the close result per connectionId. */
  closeResult: (connId: string, ok: boolean, error?: string) => void;
  disposedChannelCount(): number;
}

function fakeBridge(openScript: OpenScript): FakeBridge {
  const invokes: { cmd: string; args: Record<string, unknown> }[] = [];
  let channel: { onmessage: (response: AppwireChannelEvent) => void } | null =
    null;
  const sendScripts = new Map<string, { ok: boolean; error?: string }>();
  const closeScripts = new Map<string, { ok: boolean; error?: string }>();
  let disposedChannels = 0;

  const bridge: FakeBridge = {
    async invoke<T>(cmd: string, args?: Record<string, unknown>): Promise<T> {
      invokes.push({ cmd, args: args ?? {} });
      if (cmd === "appwire_open") {
        if (openScript.error) throw new Error(openScript.error);
        if (openScript.result) return (await openScript.result) as T;
        return {
          connectionId: openScript.connectionId,
          profileId: openScript.profileId ?? PROFILE,
          generation: openScript.generation,
        } as T;
      }
      if (cmd === "appwire_send") {
        const request = args?.request as Record<string, unknown> | undefined;
        const connId = request?.connectionId as string;
        const script = sendScripts.get(connId);
        if (script && !script.ok)
          throw new Error(script.error ?? "send failed");
        return null as T;
      }
      if (cmd === "appwire_close") {
        const request = args?.request as Record<string, unknown> | undefined;
        const connId = request?.connectionId as string;
        const script = closeScripts.get(connId);
        if (script && !script.ok)
          throw new Error(script.error ?? "close failed");
        return null as T;
      }
      throw new Error(`unexpected command: ${cmd}`);
    },
    createChannel<T>(onMessage: (response: T) => void) {
      channel = {
        onmessage: onMessage as unknown as (r: AppwireChannelEvent) => void,
      };
      return {
        id: 1,
        onmessage: onMessage,
        onclose: null,
        dispose() {
          disposedChannels += 1;
          this.onclose?.();
        },
      };
    },
    emit(event) {
      channel?.onmessage(event);
    },
    invokes,
    sendResult(connId, ok, error) {
      sendScripts.set(connId, { ok, error });
    },
    closeResult(connId, ok, error) {
      closeScripts.set(connId, { ok, error });
    },
    disposedChannelCount() {
      return disposedChannels;
    },
  };
  return bridge;
}

const PROFILE = "11111111-1111-1111-1111-111111111111";

function deferred<T>(): {
  promise: Promise<T>;
  resolve: (value: T) => void;
  reject: (error: Error) => void;
} {
  let resolve!: (value: T) => void;
  let reject!: (error: Error) => void;
  const promise = new Promise<T>((res, rej) => {
    resolve = res;
    reject = rej;
  });
  return { promise, resolve, reject };
}

describe("appwireSocket — strict Rust channel DTO agreement", () => {
  it("decodes every metadata-bearing variant including exact close reason", () => {
    const identity = {
      connectionId: "p1:7",
      profileId: PROFILE,
      generation: 7,
    };
    expect(
      decodeAppwireChannelEvent({ type: "text", ...identity, data: "frame" }),
    ).toEqual({
      type: "text",
      ...identity,
      data: "frame",
    });
    expect(
      decodeAppwireChannelEvent({
        type: "closed",
        ...identity,
        code: 1012,
        reason: "service restart",
      }),
    ).toEqual({
      type: "closed",
      ...identity,
      code: 1012,
      reason: "service restart",
    });
    expect(decodeAppwireChannelEvent({ type: "error", ...identity })).toEqual({
      type: "error",
      ...identity,
    });
  });

  it("rejects missing identity, unsafe generations, and unknown fields", () => {
    expect(() =>
      decodeAppwireChannelEvent({ type: "text", data: "frame" }),
    ).toThrow();
    expect(() =>
      decodeAppwireChannelEvent({
        type: "error",
        connectionId: "p1:7",
        profileId: PROFILE,
        generation: Number.MAX_SAFE_INTEGER + 1,
      }),
    ).toThrow();
    expect(() =>
      decodeAppwireChannelEvent({
        type: "closed",
        connectionId: "p1:7",
        profileId: PROFILE,
        generation: 7,
        code: 1000,
        reason: "",
        token: "must be rejected",
      }),
    ).toThrow();
  });
});

describe("appwireSocket — TauriSocket implements WebSocketLike", () => {
  it("fires onopen after appwire_open resolves with connectionId+generation", async () => {
    const bridge = fakeBridge({
      connectionId: "p1:3",
      generation: 3,
    });
    const factory = createAppwireSocketFactory(bridge, PROFILE);
    const socket = factory("wss://hub.example.com/rpc");
    const opened = vi.fn();
    socket.onopen = opened;
    await vi.waitFor(() => expect(opened).toHaveBeenCalledTimes(1));
    expect(bridge.invokes[0]).toEqual({
      cmd: "appwire_open",
      args: {
        request: { profileId: PROFILE },
        onEvent: expect.any(Object),
      },
    });
  });

  it("fires onerror (not onopen) when appwire_open rejects", async () => {
    const bridge = fakeBridge({
      connectionId: "x",
      generation: 0,
      error: "no capability for profile",
    });
    const factory = createAppwireSocketFactory(bridge, PROFILE);
    const socket = factory("wss://hub.example.com/rpc");
    const opened = vi.fn();
    const errored = vi.fn();
    socket.onopen = opened;
    socket.onerror = errored;
    await vi.waitFor(() => expect(errored).toHaveBeenCalledTimes(1));
    expect(opened).not.toHaveBeenCalled();
    expect(bridge.disposedChannelCount()).toBe(1);
  });
});

describe("appwireSocket — ordered text messages", () => {
  it("delivers text frames in order via onmessage", async () => {
    const bridge = fakeBridge({
      connectionId: "p1:3",
      generation: 3,
    });
    const factory = createAppwireSocketFactory(bridge, PROFILE);
    const socket = factory("wss://hub.example.com/rpc");
    await new Promise((r) => {
      socket.onopen = () => r(null);
    });
    const messages: unknown[] = [];
    socket.onmessage = (ev) => messages.push(ev.data);
    bridge.emit({
      type: "text",
      connectionId: "p1:3",
      profileId: PROFILE,
      generation: 3,
      data: "first",
    });
    bridge.emit({
      type: "text",
      connectionId: "p1:3",
      profileId: PROFILE,
      generation: 3,
      data: "second",
    });
    bridge.emit({
      type: "text",
      connectionId: "p1:3",
      profileId: PROFILE,
      generation: 3,
      data: "third",
    });
    expect(messages).toEqual(["first", "second", "third"]);
  });
});

describe("appwireSocket — send", () => {
  it("invokes appwire_send with {connectionId, frame}", async () => {
    const bridge = fakeBridge({
      connectionId: "p1:3",
      generation: 3,
    });
    const factory = createAppwireSocketFactory(bridge, PROFILE);
    const socket = factory("wss://hub.example.com/rpc");
    await new Promise((r) => {
      socket.onopen = () => r(null);
    });
    socket.send("hello hub");
    await vi.waitFor(() =>
      expect(
        bridge.invokes.some(
          (i) =>
            i.cmd === "appwire_send" &&
            (i.args.request as Record<string, unknown>)?.connectionId ===
              "p1:3" &&
            (i.args.request as Record<string, unknown>)?.frame === "hello hub",
        ),
      ).toBe(true),
    );
  });

  it("send before open is a no-op (does not invoke appwire_send)", async () => {
    const bridge = fakeBridge({
      connectionId: "p1:3",
      generation: 3,
    });
    const factory = createAppwireSocketFactory(bridge, PROFILE);
    const socket = factory("wss://hub.example.com/rpc");
    socket.send("early");
    // Allow microtasks to flush; no send should occur before open.
    await Promise.resolve();
    expect(bridge.invokes.some((i) => i.cmd === "appwire_send")).toBe(false);
  });
});

describe("appwireSocket — close", () => {
  it("invokes appwire_close and fires onclose with the given code", async () => {
    const bridge = fakeBridge({
      connectionId: "p1:3",
      generation: 3,
    });
    const factory = createAppwireSocketFactory(bridge, PROFILE);
    const socket = factory("wss://hub.example.com/rpc");
    await new Promise((r) => {
      socket.onopen = () => r(null);
    });
    const closed = vi.fn();
    socket.onclose = closed;
    socket.close(1000);
    await vi.waitFor(() => expect(closed).toHaveBeenCalledTimes(1));
    expect(closed).toHaveBeenCalledWith({ code: 1000 });
    expect(bridge.invokes.some((i) => i.cmd === "appwire_close")).toBe(true);
    expect(bridge.disposedChannelCount()).toBe(1);
  });

  it("close is idempotent — second close does not invoke appwire_close again", async () => {
    const bridge = fakeBridge({
      connectionId: "p1:3",
      generation: 3,
    });
    const factory = createAppwireSocketFactory(bridge, PROFILE);
    const socket = factory("wss://hub.example.com/rpc");
    await new Promise((r) => {
      socket.onopen = () => r(null);
    });
    socket.close(1000);
    await vi.waitFor(() =>
      expect(
        bridge.invokes.filter((i) => i.cmd === "appwire_close").length,
      ).toBe(1),
    );
    socket.close(1000);
    await Promise.resolve();
    expect(bridge.invokes.filter((i) => i.cmd === "appwire_close").length).toBe(
      1,
    );
  });

  it("delivers a server-initiated close frame via onclose with the code", async () => {
    const bridge = fakeBridge({
      connectionId: "p1:3",
      generation: 3,
    });
    const factory = createAppwireSocketFactory(bridge, PROFILE);
    const socket = factory("wss://hub.example.com/rpc");
    await new Promise((r) => {
      socket.onopen = () => r(null);
    });
    const closed = vi.fn();
    socket.onclose = closed;
    bridge.emit({
      type: "closed",
      connectionId: "p1:3",
      profileId: PROFILE,
      generation: 3,
      code: 1011,
      reason: "server restart",
    });
    expect(closed).toHaveBeenCalledWith({ code: 1011 });
    expect(bridge.disposedChannelCount()).toBe(1);
  });

  it("close-before-open-resolution closes the backend exactly once and suppresses handlers", async () => {
    const opening = deferred<{
      connectionId: string;
      profileId: string;
      generation: number;
    }>();
    const bridge = fakeBridge({
      connectionId: "unused",
      generation: 0,
      result: opening.promise,
    });
    const socket = createAppwireSocketFactory(bridge, PROFILE)("ignored");
    const opened = vi.fn();
    const messaged = vi.fn();
    const errored = vi.fn();
    const closed = vi.fn();
    socket.onopen = opened;
    socket.onmessage = messaged;
    socket.onerror = errored;
    socket.onclose = closed;

    socket.close(1001);
    socket.close(1002);
    bridge.emit({
      type: "text",
      connectionId: "p1:9",
      profileId: PROFILE,
      generation: 9,
      data: "queued after close",
    });
    opening.resolve({
      connectionId: "p1:9",
      profileId: PROFILE,
      generation: 9,
    });

    await vi.waitFor(() =>
      expect(
        bridge.invokes.filter((call) => call.cmd === "appwire_close"),
      ).toHaveLength(1),
    );
    await vi.waitFor(() => expect(closed).toHaveBeenCalledTimes(1));
    expect(closed).toHaveBeenCalledWith({ code: 1001 });
    expect(opened).not.toHaveBeenCalled();
    expect(messaged).not.toHaveBeenCalled();
    expect(errored).not.toHaveBeenCalled();
  });

  it("an open rejection after close reports one close without error or backend close", async () => {
    const opening = deferred<{
      connectionId: string;
      profileId: string;
      generation: number;
    }>();
    const bridge = fakeBridge({
      connectionId: "unused",
      generation: 0,
      result: opening.promise,
    });
    const socket = createAppwireSocketFactory(bridge, PROFILE)("ignored");
    const errored = vi.fn();
    const closed = vi.fn();
    socket.onerror = errored;
    socket.onclose = closed;
    socket.close();
    opening.reject(new Error("open failed with secret details"));

    await vi.waitFor(() => expect(closed).toHaveBeenCalledTimes(1));
    expect(errored).not.toHaveBeenCalled();
    expect(
      bridge.invokes.filter((call) => call.cmd === "appwire_close"),
    ).toHaveLength(0);
  });
});

describe("appwireSocket — error event", () => {
  it("fires onerror when the backend emits an error event", async () => {
    const bridge = fakeBridge({
      connectionId: "p1:3",
      generation: 3,
    });
    const factory = createAppwireSocketFactory(bridge, PROFILE);
    const socket = factory("wss://hub.example.com/rpc");
    await new Promise((r) => {
      socket.onopen = () => r(null);
    });
    const errored = vi.fn();
    socket.onerror = errored;
    bridge.emit({
      type: "error",
      connectionId: "p1:3",
      profileId: PROFILE,
      generation: 3,
    });
    expect(errored).toHaveBeenCalledTimes(1);
  });

  it("error event carries no secret/token field", async () => {
    const bridge = fakeBridge({
      connectionId: "p1:3",
      generation: 3,
    });
    const factory = createAppwireSocketFactory(bridge, PROFILE);
    const socket = factory("wss://hub.example.com/rpc");
    await new Promise((r) => {
      socket.onopen = () => r(null);
    });
    // The error event type has no data field at all (redacted in Rust).
    const events: unknown[] = [];
    socket.onerror = () => events.push("err");
    bridge.emit({
      type: "error",
      connectionId: "p1:3",
      profileId: PROFILE,
      generation: 3,
    });
    expect(events).toEqual(["err"]);
  });
});

describe("appwireSocket — stale profile/connection generation rejection", () => {
  it("send on a stale connection (after a new open) is rejected by the backend", async () => {
    const bridge = fakeBridge({
      connectionId: "p1:3",
      generation: 3,
    });
    const factory = createAppwireSocketFactory(bridge, PROFILE);
    const socket = factory("wss://hub.example.com/rpc");
    await new Promise((r) => {
      socket.onopen = () => r(null);
    });
    // Simulate the backend rejecting a stale send (profile switched).
    bridge.sendResult("p1:3", false, "stale connection");
    const errors: unknown[] = [];
    socket.onerror = () => errors.push("err");
    socket.send("stale frame");
    // The send rejection surfaces as an error event (no throw into AppwireClient).
    await vi.waitFor(() => expect(errors.length).toBe(1));
  });

  it("rejects stale events queued before the current open identity resolves", async () => {
    const opening = deferred<{
      connectionId: string;
      profileId: string;
      generation: number;
    }>();
    const bridge = fakeBridge({
      connectionId: "unused",
      generation: 0,
      result: opening.promise,
    });
    const socket = createAppwireSocketFactory(bridge, PROFILE)("ignored");
    const messages = vi.fn();
    const opened = vi.fn();
    socket.onmessage = messages;
    socket.onopen = opened;
    bridge.emit({
      type: "text",
      connectionId: "old:4",
      profileId: PROFILE,
      generation: 4,
      data: "stale queued frame",
    });
    opening.resolve({
      connectionId: "new:5",
      profileId: PROFILE,
      generation: 5,
    });

    await vi.waitFor(() => expect(opened).toHaveBeenCalledTimes(1));
    expect(messages).not.toHaveBeenCalled();
    bridge.emit({
      type: "text",
      connectionId: "new:5",
      profileId: PROFILE,
      generation: 5,
      data: "current frame",
    });
    expect(messages).toHaveBeenCalledWith({ data: "current frame" });
  });
});

describe("appwireSocket — retry-created socket", () => {
  it("a second factory call creates a fresh socket with its own connection", async () => {
    const bridge = fakeBridge({
      connectionId: "p1:3",
      generation: 3,
    });
    const factory = createAppwireSocketFactory(bridge, PROFILE);
    const socket1 = factory("wss://hub.example.com/rpc");
    await new Promise((r) => {
      socket1.onopen = () => r(null);
    });
    // A retry creates a new socket (new open call) on a fresh bridge.
    const bridge2 = fakeBridge({
      connectionId: "p1:4",
      generation: 4,
    });
    const factory2 = createAppwireSocketFactory(bridge2, PROFILE);
    const socket2 = factory2("wss://hub.example.com/rpc");
    const opened2 = vi.fn();
    socket2.onopen = opened2;
    await vi.waitFor(() => expect(opened2).toHaveBeenCalledTimes(1));
    // socket2 sends use the new connectionId.
    socket2.send("retry");
    await vi.waitFor(() =>
      expect(
        bridge2.invokes.some(
          (i) =>
            i.cmd === "appwire_send" &&
            (i.args.request as Record<string, unknown>)?.connectionId ===
              "p1:4",
        ),
      ).toBe(true),
    );
  });
});

describe("appwireSocket — AppwireClient clientInfo", () => {
  it("createAppwireClient sets clientInfo {name:'evener-mobile', version:'0.1.0'}", async () => {
    const bridge = fakeBridge({
      connectionId: "p1:3",
      generation: 3,
    });
    const client = createAppwireClient({
      bridge,
      url: "wss://hub.example.com/rpc",
      profileId: PROFILE,
    });
    // Connect triggers the handshake: appwire_open resolves, the socket opens,
    // and the client sends an `initialize` frame via appwire_send. We capture
    // that frame and assert the clientInfo it carries.
    const connectPromise = client.connect();
    const clientInfo = await vi.waitFor(() => {
      const send = bridge.invokes.find(
        (i) =>
          i.cmd === "appwire_send" &&
          typeof (i.args.request as Record<string, unknown>)?.frame ===
            "string",
      );
      expect(send).toBeDefined();
      const parsed = JSON.parse(
        ((send?.args.request as Record<string, unknown>)?.frame as string) ??
          "{}",
      ) as {
        method?: string;
        params?: { clientInfo?: { name: string; version: string } };
      };
      expect(parsed.method).toBe("initialize");
      return parsed.params?.clientInfo;
    });
    expect(clientInfo).toEqual({
      name: "evener-mobile",
      version: "0.1.0",
    });
    // No server answers; close rejects the pending connect.
    client.close();
    await connectPromise.catch(() => {
      // Expected: close aborts the in-flight handshake.
    });
  });

  it("createAppwireClient returns a typed AppwireClient bound to the socket factory", () => {
    const bridge = fakeBridge({
      connectionId: "p1:3",
      generation: 3,
    });
    const client: AppwireClientWithUrl = createAppwireClient({
      bridge,
      url: "wss://hub.example.com/rpc",
      profileId: PROFILE,
    });
    expect(typeof client.connect).toBe("function");
    expect(typeof client.close).toBe("function");
    expect(typeof client.request).toBe("function");
    client.close();
  });
});

describe("appwireSocket — imported AppwireClient scripted Tauri integration", () => {
  it("performs handshake, correlation, notification, close/reconnect, and profile switch", async () => {
    vi.useFakeTimers();
    try {
      const PROFILE_TWO = "22222222-2222-2222-2222-222222222222";
      interface ScriptedConnection {
        readonly identity: {
          connectionId: string;
          profileId: string;
          generation: number;
        };
        readonly channel: TauriChannel<unknown>;
        readonly clientFrames: Record<string, unknown>[];
      }
      const invokes: { cmd: string; args: Record<string, unknown> }[] = [];
      const connections: ScriptedConnection[] = [];
      const closeWaiters = new Map<string, () => void>();
      let generation = 10;
      const bridge: TauriBridge = {
        async invoke<T>(
          cmd: string,
          rawArgs?: Record<string, unknown> | ArrayBuffer | Uint8Array,
        ): Promise<T> {
          const args = (rawArgs ?? {}) as Record<string, unknown>;
          const call = { cmd, args };
          invokes.push(call);
          if (cmd === "appwire_open") {
            expect(Object.keys(args).sort()).toEqual(["onEvent", "request"]);
            const request = args.request as { profileId: string };
            const identity = {
              connectionId: `${request.profileId}:${++generation}`,
              profileId: request.profileId,
              generation,
            };
            connections.push({
              identity,
              channel: args.onEvent as TauriChannel<unknown>,
              clientFrames: [],
            });
            return identity as T;
          }
          if (cmd === "appwire_send") {
            expect(Object.keys(args)).toEqual(["request"]);
            const request = args.request as Record<string, unknown>;
            const connectionId = request.connectionId as string;
            const connection = connections.find(
              (candidate) => candidate.identity.connectionId === connectionId,
            );
            if (!connection) throw new Error("unknown scripted connection");
            const frame = JSON.parse(request.frame as string) as Record<
              string,
              unknown
            > & {
              id?: number;
              method?: string;
            };
            connection.clientFrames.push(frame);
            if (frame.id !== undefined) {
              queueMicrotask(() => {
                const result =
                  frame.method === "initialize"
                    ? { protocolVersion: "evener-appwire-v3" }
                    : {};
                connection.channel.onmessage({
                  type: "text",
                  ...connection.identity,
                  data: JSON.stringify({
                    id: frame.id,
                    result,
                  }),
                });
              });
            }
            return undefined as T;
          }
          if (cmd === "appwire_close") {
            expect(Object.keys(args)).toEqual(["request"]);
            const request = args.request as { connectionId: string };
            closeWaiters.get(request.connectionId)?.();
            return undefined as T;
          }
          throw new Error(`unexpected command ${cmd}`);
        },
        createChannel<T>(onMessage: (event: T) => void) {
          return {
            id: connections.length + 1,
            onmessage: onMessage,
            onclose: null,
            dispose() {
              this.onclose?.();
            },
          };
        },
      };

      const client = createAppwireClient({
        bridge,
        url: "ignored",
        profileId: PROFILE,
      });
      const notifications: string[] = [];
      client.onNotification((notification) =>
        notifications.push(notification.method),
      );
      await client.connect();
      expect(client.state).toBe("ready");
      const first = connections[0];
      expect(first).toBeDefined();
      const initialize = first?.clientFrames[0];
      expect(initialize).toMatchObject({
        method: "initialize",
        params: {
          protocolVersion: "evener-appwire-v3",
          clientInfo: { name: "evener-mobile", version: "0.1.0" },
        },
      });
      expect(first?.clientFrames[1]).toEqual({
        method: "initialized",
        params: {},
      });
      await expect(client.request("ping", {})).resolves.toEqual({});

      first?.channel.onmessage({
        type: "text",
        ...first.identity,
        data: JSON.stringify({ method: "treeChanged", params: {} }),
      });
      expect(notifications).toEqual(["treeChanged"]);

      const secondReady = deferred<void>();
      const stopReady = client.onReady(() => secondReady.resolve(undefined));
      first?.channel.onmessage({
        type: "closed",
        ...first.identity,
        code: 1012,
        reason: "service restart",
      });
      expect(client.state).toBe("reconnecting");
      await vi.advanceTimersByTimeAsync(250);
      await secondReady.promise;
      stopReady();
      expect(client.state).toBe("ready");
      const second = connections[1];
      expect(second?.identity.generation).toBeGreaterThan(
        first?.identity.generation ?? 0,
      );

      first?.channel.onmessage({
        type: "text",
        ...first.identity,
        data: JSON.stringify({ method: "staleNotification", params: {} }),
      });
      second?.channel.onmessage({
        type: "text",
        ...second.identity,
        data: JSON.stringify({ method: "currentNotification", params: {} }),
      });
      expect(notifications).toEqual(["treeChanged", "currentNotification"]);

      const closeSettled = deferred<void>();
      if (second) {
        closeWaiters.set(second.identity.connectionId, () =>
          closeSettled.resolve(undefined),
        );
      }
      client.close();
      await closeSettled.promise;

      const profileTwoClient = createAppwireClient({
        bridge,
        url: "ignored",
        profileId: PROFILE_TWO,
      });
      const profileTwoNotifications: string[] = [];
      profileTwoClient.onNotification((notification) =>
        profileTwoNotifications.push(notification.method),
      );
      await profileTwoClient.connect();
      const third = connections[2];
      expect(third?.identity.profileId).toBe(PROFILE_TWO);
      second?.channel.onmessage({
        type: "text",
        ...second.identity,
        data: JSON.stringify({ method: "staleProfileEvent", params: {} }),
      });
      third?.channel.onmessage({
        type: "text",
        ...third.identity,
        data: JSON.stringify({ method: "profileTwoEvent", params: {} }),
      });
      expect(profileTwoNotifications).toEqual(["profileTwoEvent"]);

      const finalClose = deferred<void>();
      if (third) {
        closeWaiters.set(third.identity.connectionId, () =>
          finalClose.resolve(undefined),
        );
      }
      profileTwoClient.close();
      await finalClose.promise;
      expect(
        invokes.every((call) => {
          if (call.cmd === "appwire_open") {
            return "request" in call.args && "onEvent" in call.args;
          }
          if (call.cmd === "appwire_send" || call.cmd === "appwire_close") {
            return (
              Object.keys(call.args).length === 1 && "request" in call.args
            );
          }
          return true;
        }),
      ).toBe(true);
    } finally {
      vi.useRealTimers();
    }
  });
});
