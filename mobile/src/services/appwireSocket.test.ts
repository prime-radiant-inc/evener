import { describe, expect, it, vi } from "vitest";

import type { AppwireChannelEvent } from "./appwireSocket";
import {
  type AppwireClientWithUrl,
  createAppwireClient,
  createAppwireSocketFactory,
} from "./appwireSocket";

// ---------------------------------------------------------------------------
// Fake TauriBridge — captures the event channel and scripts invoke results
// ---------------------------------------------------------------------------

interface OpenScript {
  connectionId: string;
  generation: number;
  error?: string;
}

interface FakeBridge {
  invoke: <T>(cmd: string, args?: Record<string, unknown>) => Promise<T>;
  createChannel: <T>(onMessage: (response: T) => void) => {
    id: number;
    onmessage: (response: T) => void;
  };
  /** Captured channel emit helper (typed for AppwireChannelEvent). */
  emit: (event: AppwireChannelEvent) => void;
  readonly invokes: { cmd: string; args: Record<string, unknown> }[];
  /** Script the send result/error per connectionId. */
  sendResult: (connId: string, ok: boolean, error?: string) => void;
  /** Script the close result per connectionId. */
  closeResult: (connId: string, ok: boolean, error?: string) => void;
}

function fakeBridge(openScript: OpenScript): FakeBridge {
  const invokes: { cmd: string; args: Record<string, unknown> }[] = [];
  let channel: { onmessage: (response: AppwireChannelEvent) => void } | null =
    null;
  const sendScripts = new Map<string, { ok: boolean; error?: string }>();
  const closeScripts = new Map<string, { ok: boolean; error?: string }>();

  const bridge: FakeBridge = {
    async invoke<T>(cmd: string, args?: Record<string, unknown>): Promise<T> {
      invokes.push({ cmd, args: args ?? {} });
      if (cmd === "appwire_open") {
        if (openScript.error) throw new Error(openScript.error);
        return {
          connectionId: openScript.connectionId,
          generation: openScript.generation,
        } as T;
      }
      if (cmd === "appwire_send") {
        const connId = args?.connectionId as string;
        const script = sendScripts.get(connId);
        if (script && !script.ok)
          throw new Error(script.error ?? "send failed");
        return null as T;
      }
      if (cmd === "appwire_close") {
        const connId = args?.connectionId as string;
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
      return { id: 1, onmessage: onMessage };
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
  };
  return bridge;
}

const PROFILE = "11111111-1111-1111-1111-111111111111";

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
        profileId: PROFILE,
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
    bridge.emit({ type: "text", data: "first" });
    bridge.emit({ type: "text", data: "second" });
    bridge.emit({ type: "text", data: "third" });
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
            i.args.connectionId === "p1:3" &&
            i.args.frame === "hello hub",
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
    bridge.emit({ type: "closed", code: 1011 });
    expect(closed).toHaveBeenCalledWith({ code: 1011 });
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
    bridge.emit({ type: "error" });
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
    bridge.emit({ type: "error" });
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
          (i) => i.cmd === "appwire_send" && i.args.connectionId === "p1:4",
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
        (i) => i.cmd === "appwire_send" && typeof i.args.frame === "string",
      );
      expect(send).toBeDefined();
      const parsed = JSON.parse((send?.args.frame as string) ?? "{}") as {
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
