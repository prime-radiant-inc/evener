import {
  encodeLocalConfig,
  makeTranscriptDisplayConfig,
  type TranscriptDisplayConfigV1,
  type ViewportClass,
} from "@evener/appwire-client";
import { afterEach, describe, expect, it, vi } from "vitest";
import { createBrowserSync } from "./transcriptDisplay/crossTabSync";
import { createVersionedChannel, makeSourceId, type VersionedChannelMessage } from "./versionedChannel";

class TestChannel extends EventTarget {
  readonly postMessage = vi.fn<(message: unknown) => void>();
  readonly close = vi.fn();

  emit(data: unknown): void {
    this.dispatchEvent(new MessageEvent("message", { data }));
  }
}

interface TestMessage extends VersionedChannelMessage {
  value: string;
}

function channelFixture(createChannel?: (name: string) => BroadcastChannel | null) {
  const transport = new TestChannel();
  const onMessage = vi.fn();
  let sourceId = "origin";
  const channel = createVersionedChannel<TestMessage>({
    name: "fixture.v1",
    getSourceId: () => sourceId,
    isMessage: (value): value is TestMessage => typeof (value as Partial<TestMessage>).value === "string",
    onMessage,
    createChannel: createChannel ?? (() => transport as unknown as BroadcastChannel),
  });
  return { channel, transport, onMessage, setSourceId: (value: string) => (sourceId = value) };
}

afterEach(() => {
  vi.restoreAllMocks();
  vi.unstubAllGlobals();
});

describe("versioned channel", () => {
  it("sends the exact v1 envelope and filters echoes with the caller's current incarnation", () => {
    const { channel, transport, onMessage, setSourceId } = channelFixture();
    channel.connect();
    channel.postMessage({ value: "first" });
    expect(transport.postMessage).toHaveBeenLastCalledWith({ version: 1, sourceId: "origin", value: "first" });
    transport.emit({ version: 1, sourceId: "origin", value: "echo" });
    expect(onMessage).not.toHaveBeenCalled();
    setSourceId("next-incarnation");
    channel.postMessage({ value: "second" });
    expect(transport.postMessage).toHaveBeenLastCalledWith({
      version: 1,
      sourceId: "next-incarnation",
      value: "second",
    });
    transport.emit({ version: 1, sourceId: "origin", value: "peer" });
    expect(onMessage).toHaveBeenCalledExactlyOnceWith({ version: 1, sourceId: "origin", value: "peer" });
    channel.close();
  });

  it("rejects invalid envelopes and payloads without strengthening the legacy cache envelope", () => {
    const { channel, transport, onMessage } = channelFixture();
    channel.connect();
    const peer = { version: 1, sourceId: "peer", value: "valid" };
    for (const invalid of [
      null,
      "message",
      {},
      { ...peer, version: 2 },
      { ...peer, sourceId: "" },
      { ...peer, sourceId: undefined },
      { ...peer, value: 5 },
    ]) {
      transport.emit(invalid);
    }
    expect(onMessage).not.toHaveBeenCalled();
    // Cache v1 accepted non-string sources, extra keys and array envelopes.
    // Its extraction must not silently turn that floor into a stricter schema.
    for (const accepted of [
      { ...peer, sourceId: null },
      { ...peer, sourceId: 5, extra: true },
      Object.assign([], peer),
    ]) {
      transport.emit(accepted);
      expect(onMessage).toHaveBeenLastCalledWith(accepted);
    }
    expect(onMessage).toHaveBeenCalledTimes(3);
    channel.close();
  });

  it("attaches once, removes the listener on close, and does not reconnect implicitly on send", () => {
    const transport = new TestChannel();
    const createChannel = vi.fn(() => transport as unknown as BroadcastChannel);
    const { channel, onMessage } = channelFixture(createChannel);
    channel.postMessage({ value: "before attach" });
    expect(createChannel).not.toHaveBeenCalled();
    channel.connect();
    channel.connect();
    expect(createChannel).toHaveBeenCalledExactlyOnceWith("fixture.v1");
    channel.close();
    channel.close();
    transport.emit({ version: 1, sourceId: "peer", value: "after detach" });
    channel.postMessage({ value: "after detach" });
    expect(onMessage).not.toHaveBeenCalled();
    expect(transport.postMessage).not.toHaveBeenCalled();
    expect(transport.close).toHaveBeenCalledOnce();
  });

  it.each(["absent", "denied"])("allows an explicit retry after an %s channel", (failure) => {
    const transport = new TestChannel();
    const createChannel = vi
      .fn<() => BroadcastChannel | null>()
      .mockImplementationOnce(() => {
        if (failure === "denied") throw new Error("channel denied");
        return null;
      })
      .mockReturnValue(transport as unknown as BroadcastChannel);
    const { channel } = channelFixture(createChannel);
    expect(() => channel.connect()).not.toThrow();
    channel.postMessage({ value: "unavailable" });
    expect(transport.postMessage).not.toHaveBeenCalled();
    channel.connect();
    channel.postMessage({ value: "available" });
    expect(transport.postMessage).toHaveBeenCalledExactlyOnceWith({
      version: 1,
      sourceId: "origin",
      value: "available",
    });
    channel.close();
  });

  it("drops a refused post without closing a usable channel", () => {
    const { channel, transport } = channelFixture();
    channel.connect();
    transport.postMessage.mockImplementationOnce(() => {
      throw new Error("post refused");
    });
    expect(() => channel.postMessage({ value: "refused" })).not.toThrow();
    channel.postMessage({ value: "next" });
    expect(transport.postMessage).toHaveBeenCalledTimes(2);
    expect(transport.close).not.toHaveBeenCalled();
    channel.close();
  });
});

describe("source ids", () => {
  it("uses randomUUID when available", () => {
    vi.stubGlobal("crypto", { randomUUID: () => "browser-uuid" });
    expect(makeSourceId()).toBe("browser-uuid");
  });

  it.each(["absent", "missing UUID", "denied"])("keeps the random-time fallback for %s crypto", (failure) => {
    vi.stubGlobal(
      "crypto",
      failure === "absent"
        ? undefined
        : failure === "missing UUID"
          ? {}
          : {
              randomUUID: () => {
                throw new Error("randomUUID denied");
              },
            },
    );
    vi.spyOn(Math, "random").mockReturnValue(0.5);
    vi.spyOn(Date, "now").mockReturnValue(36);
    expect(makeSourceId()).toBe("i-10");
  });
});

function browserFixture() {
  const local: Partial<Record<ViewportClass, TranscriptDisplayConfigV1>> = {};
  const applyLocal = vi.fn((layout: ViewportClass, config: TranscriptDisplayConfigV1 | undefined) => {
    local[layout] = config;
  });
  const onDetach = vi.fn();
  const sync = createBrowserSync({
    channelName: "evener.transcript-display.v1",
    localKeys: { desktop: "display.desktop", mobile: "display.mobile" },
    getState: () => ({ local }),
    applyLocal,
    onDetach,
  });
  return { sync, local, applyLocal, onDetach };
}

describe("transcript display channel compatibility", () => {
  it("keeps the five-field envelope, strict validation and per-attachment source ids", () => {
    const transports: TestChannel[] = [];
    vi.stubGlobal(
      "BroadcastChannel",
      class extends TestChannel {
        constructor(name: string) {
          super();
          expect(name).toBe("evener.transcript-display.v1");
          transports.push(this);
        }
      },
    );
    vi.stubGlobal("crypto", {
      randomUUID: vi.fn().mockReturnValueOnce("first-source").mockReturnValueOnce("second-source"),
    });
    const { sync, applyLocal, onDetach } = browserFixture();
    const config = makeTranscriptDisplayConfig({ kind: "preset", level: "chat" });
    const encoded = encodeLocalConfig(config);
    const own = { version: 1, sourceId: "first-source", layout: "desktop", config: encoded, fingerprint: encoded };
    sync.attach();
    try {
      const transport = transports[0];
      if (transport === undefined) throw new Error("attach must create the channel");
      sync.broadcastLocal("desktop", encoded);
      expect(transport.postMessage).toHaveBeenCalledExactlyOnceWith(own);
      const peer = { ...own, sourceId: "peer" };
      for (const invalid of [
        own,
        { ...peer, extra: true },
        { ...peer, sourceId: 5 },
        { ...peer, sourceId: null },
        { ...peer, layout: "other" },
        { ...peer, fingerprint: "mismatch" },
        { ...peer, config: "malformed" },
        { ...peer, config: null },
        Object.assign([], peer),
      ]) {
        transport.emit(invalid);
      }
      expect(applyLocal).not.toHaveBeenCalled();
      transport.emit(peer);
      expect(applyLocal).toHaveBeenCalledExactlyOnceWith("desktop", config);
      transport.emit(peer);
      expect(applyLocal).toHaveBeenCalledTimes(1); // identical fingerprints coalesce
      sync.broadcastLocal("desktop", "malformed");
      expect(transport.postMessage).toHaveBeenCalledTimes(1);
      sync.broadcastLocal("desktop", null);
      expect(transport.postMessage).toHaveBeenLastCalledWith({ ...own, config: null, fingerprint: null });
    } finally {
      sync.detach();
    }
    expect(onDetach).toHaveBeenCalledOnce();
    sync.attach();
    try {
      const transport = transports[1];
      if (transport === undefined) throw new Error("reattach must create a new channel");
      sync.broadcastLocal("desktop", encoded);
      expect(transport.postMessage).toHaveBeenLastCalledWith({ ...own, sourceId: "second-source" });
    } finally {
      sync.detach();
    }
  });

  it.each(["absent", "denied"])("keeps storage-event fallback when BroadcastChannel is %s", (failure) => {
    const denied = vi.fn(() => {
      throw new Error("channel denied");
    });
    vi.stubGlobal(
      "BroadcastChannel",
      failure === "absent"
        ? undefined
        : class {
            constructor() {
              denied();
            }
          },
    );
    const { sync, local, applyLocal } = browserFixture();
    const config = makeTranscriptDisplayConfig({ kind: "preset", level: "activity" });
    const encoded = encodeLocalConfig(config);
    sync.attach();
    try {
      expect(denied).toHaveBeenCalledTimes(failure === "denied" ? 1 : 0);
      expect(() => sync.broadcastLocal("desktop", encoded)).not.toThrow();
      window.dispatchEvent(new StorageEvent("storage", { key: "display.mobile", newValue: encoded }));
      expect(local.mobile).toEqual(config);
      window.dispatchEvent(new StorageEvent("storage", { key: "display.mobile", newValue: null }));
      expect(local.mobile).toBeUndefined();
      expect(applyLocal).toHaveBeenCalledTimes(2);
    } finally {
      sync.detach();
    }
    window.dispatchEvent(new StorageEvent("storage", { key: "display.mobile", newValue: encoded }));
    expect(applyLocal).toHaveBeenCalledTimes(2);
  });
});
