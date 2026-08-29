/**
 * Production transport — exact routes, envelopes, and real raw shapes.
 *
 * Drives the real production adapter through an injected fake `TauriBridge`
 * to assert that `NativeBridge.scanAndPreviewPairing`, `hapticPerform`, and
 * `getContentSize` call the exact Tauri plugin routes with exact envelopes.
 *
 * Raw response shapes are pinned to what the Rust commands actually return:
 * - scan_and_preview_pairing → `{version, type, previewId, origin}` (versioned)
 * - haptic_perform            → `{completed: boolean}`                 (NOT versioned)
 * - content_size_get          → `{category: string}`                   (NOT versioned)
 * The adapter must decode each to the typed NativeResponse the client expects.
 */
import { describe, expect, it, vi } from "vitest";
import type { TauriBridge } from "../services/tauri";

// ---------------------------------------------------------------------------
// Fake TauriBridge — records invoke routes and args, returns scripted results
// ---------------------------------------------------------------------------

interface ScriptedInvoke {
  readonly route: string;
  readonly result: unknown;
}

function fakeTauriBridge(scripts: ScriptedInvoke[]): TauriBridge & {
  readonly invocations: { route: string; args: unknown }[];
} {
  const invocations: { route: string; args: unknown }[] = [];
  const queue = [...scripts];
  const bridge: TauriBridge = {
    async invoke<T>(
      route: string,
      args?: Record<string, unknown> | ArrayBuffer | Uint8Array,
    ): Promise<T> {
      const recordedArgs = args ?? {};
      invocations.push({ route, args: recordedArgs });
      const next = queue.shift();
      if (next === undefined) {
        throw new Error(`unexpected invoke: ${route}`);
      }
      if (next.route !== route) {
        throw new Error(`route mismatch: expected ${next.route}, got ${route}`);
      }
      return next.result as T;
    },
    createChannel() {
      return {
        id: 0,
        onmessage: () => {},
        onclose: null,
        dispose: () => {},
      } as never;
    },
    listen(): Promise<() => void> {
      return Promise.resolve(() => {});
    },
  };
  return Object.assign(bridge, { invocations });
}

// ---------------------------------------------------------------------------
// The real production transport adapter — imported from the source
// ---------------------------------------------------------------------------

import { createNativeBridge, NativeBridgeError } from "../native/client";
import { createTauriNativeTransport } from "./production-transport";

// ---------------------------------------------------------------------------
// Routes and envelopes
// ---------------------------------------------------------------------------

describe("official opener transport", () => {
  it("calls openUrl with the canonical URL and no openWith argument", async () => {
    const bridge = fakeTauriBridge([]);
    const openUrl = vi.fn(async () => {});
    const transport = createTauriNativeTransport(bridge, openUrl);
    await transport.openExternalUrl("https://example.com/docs");
    expect(openUrl).toHaveBeenCalledWith("https://example.com/docs");
    expect(openUrl.mock.calls[0]).toHaveLength(1);
    expect(bridge.invocations).toHaveLength(0);
  });
});

describe("7A transport: scanAndPreviewPairing route and response", () => {
  it("invokes plugin:evener-native|scan_and_preview_pairing with {payload:{}}", async () => {
    const bridge = fakeTauriBridge([
      {
        route: "plugin:evener-native|scan_and_preview_pairing",
        result: {
          version: 1,
          type: "pairing.preview",
          previewId: "pv-1",
          origin: "https://hub.example.com:8443",
        },
      },
    ]);
    const transport = createTauriNativeTransport(bridge);
    const native = createNativeBridge(transport);
    const result = await native.scanAndPreviewPairing();
    expect(result).toEqual({
      previewId: "pv-1",
      origin: "https://hub.example.com:8443",
    });
    expect(bridge.invocations.length).toBe(1);
    expect(bridge.invocations[0]?.route).toBe(
      "plugin:evener-native|scan_and_preview_pairing",
    );
    expect(bridge.invocations[0]?.args).toEqual({ payload: {} });
  });
});

describe("7A transport: hapticPerform route and real raw shape", () => {
  it("invokes plugin:evener-native|haptic_perform with {payload:{kind}}", async () => {
    const bridge = fakeTauriBridge([
      {
        route: "plugin:evener-native|haptic_perform",
        // Real Rust HapticPerformResponse: {completed: true}
        result: { completed: true },
      },
    ]);
    const transport = createTauriNativeTransport(bridge);
    const native = createNativeBridge(transport);
    await native.hapticPerform("notificationSuccess");
    expect(bridge.invocations.length).toBe(1);
    expect(bridge.invocations[0]?.route).toBe(
      "plugin:evener-native|haptic_perform",
    );
    expect(bridge.invocations[0]?.args).toEqual({
      payload: { kind: "notificationSuccess" },
    });
  });

  it("accepts the real raw shape {completed:true} and resolves", async () => {
    const bridge = fakeTauriBridge([
      {
        route: "plugin:evener-native|haptic_perform",
        result: { completed: true },
      },
    ]);
    const transport = createTauriNativeTransport(bridge);
    const native = createNativeBridge(transport);
    // Must not throw — the adapter maps {completed:true} to haptic.completed.
    await expect(native.hapticPerform("impactLight")).resolves.toBeUndefined();
  });

  it("rejects {completed:false} as a haptic failure", async () => {
    const bridge = fakeTauriBridge([
      {
        route: "plugin:evener-native|haptic_perform",
        result: { completed: false },
      },
    ]);
    const transport = createTauriNativeTransport(bridge);
    const native = createNativeBridge(transport);
    // completed:false means the haptic did not perform — must surface as an error.
    await expect(native.hapticPerform("selection")).rejects.toThrow();
  });

  it("rejects missing `completed` field", async () => {
    const bridge = fakeTauriBridge([
      {
        route: "plugin:evener-native|haptic_perform",
        result: {},
      },
    ]);
    const transport = createTauriNativeTransport(bridge);
    const native = createNativeBridge(transport);
    await expect(native.hapticPerform("selection")).rejects.toThrow();
  });

  it("rejects wrong-typed `completed` field", async () => {
    const bridge = fakeTauriBridge([
      {
        route: "plugin:evener-native|haptic_perform",
        result: { completed: "yes" },
      },
    ]);
    const transport = createTauriNativeTransport(bridge);
    const native = createNativeBridge(transport);
    await expect(native.hapticPerform("selection")).rejects.toThrow();
  });

  it("rejects extra fields in the haptic raw shape", async () => {
    const bridge = fakeTauriBridge([
      {
        route: "plugin:evener-native|haptic_perform",
        result: { completed: true, extra: "no" },
      },
    ]);
    const transport = createTauriNativeTransport(bridge);
    const native = createNativeBridge(transport);
    await expect(native.hapticPerform("selection")).rejects.toThrow();
  });
});

describe("7A transport: getContentSize route and real raw shape", () => {
  it("invokes plugin:evener-native|content_size_get with {payload:{}}", async () => {
    const bridge = fakeTauriBridge([
      {
        route: "plugin:evener-native|content_size_get",
        // Real Rust ContentSizeGetResponse: {category: "extraExtraLarge"}
        result: { category: "extraExtraLarge" },
      },
    ]);
    const transport = createTauriNativeTransport(bridge);
    const native = createNativeBridge(transport);
    const result = await native.getContentSize();
    expect(result).toBe("extraExtraLarge");
    expect(bridge.invocations.length).toBe(1);
    expect(bridge.invocations[0]?.route).toBe(
      "plugin:evener-native|content_size_get",
    );
    expect(bridge.invocations[0]?.args).toEqual({ payload: {} });
  });

  it("accepts the real raw shape {category:string} and returns the category", async () => {
    const bridge = fakeTauriBridge([
      {
        route: "plugin:evener-native|content_size_get",
        result: { category: "accessibilityLarge" },
      },
    ]);
    const transport = createTauriNativeTransport(bridge);
    const native = createNativeBridge(transport);
    const result = await native.getContentSize();
    expect(result).toBe("accessibilityLarge");
    expect(typeof result).toBe("string");
  });

  it("rejects a category string outside the ContentSizeCategory union", async () => {
    const bridge = fakeTauriBridge([
      {
        route: "plugin:evener-native|content_size_get",
        result: { category: "invalidCategory" },
      },
    ]);
    const transport = createTauriNativeTransport(bridge);
    const native = createNativeBridge(transport);
    await expect(native.getContentSize()).rejects.toThrow();
  });

  it("rejects a missing `category` field", async () => {
    const bridge = fakeTauriBridge([
      {
        route: "plugin:evener-native|content_size_get",
        result: {},
      },
    ]);
    const transport = createTauriNativeTransport(bridge);
    const native = createNativeBridge(transport);
    await expect(native.getContentSize()).rejects.toThrow();
  });

  it("rejects a wrong-typed `category` field", async () => {
    const bridge = fakeTauriBridge([
      {
        route: "plugin:evener-native|content_size_get",
        result: { category: 42 },
      },
    ]);
    const transport = createTauriNativeTransport(bridge);
    const native = createNativeBridge(transport);
    await expect(native.getContentSize()).rejects.toThrow();
  });

  it("rejects extra fields in the content-size raw shape", async () => {
    const bridge = fakeTauriBridge([
      {
        route: "plugin:evener-native|content_size_get",
        result: { category: "large", extra: "no" },
      },
    ]);
    const transport = createTauriNativeTransport(bridge);
    const native = createNativeBridge(transport);
    await expect(native.getContentSize()).rejects.toThrow();
  });
});

describe("7A transport: clipboardPaste route and real raw shape", () => {
  it("invokes plugin:evener-native|clipboard_paste with {payload:{}}", async () => {
    const bridge = fakeTauriBridge([
      {
        route: "plugin:evener-native|clipboard_paste",
        // Real Rust ClipboardPasteResponse: {text: "..."}
        result: { text: "http://192.168.1.5:9181/auth?token=abc" },
      },
    ]);
    const transport = createTauriNativeTransport(bridge);
    const native = createNativeBridge(transport);
    const result = await native.clipboardPaste();
    expect(result).toBe("http://192.168.1.5:9181/auth?token=abc");
    expect(bridge.invocations.length).toBe(1);
    expect(bridge.invocations[0]?.route).toBe(
      "plugin:evener-native|clipboard_paste",
    );
    expect(bridge.invocations[0]?.args).toEqual({ payload: {} });
  });

  it("accepts the real raw shape {text:string} and returns the text", async () => {
    const bridge = fakeTauriBridge([
      {
        route: "plugin:evener-native|clipboard_paste",
        result: { text: "hello" },
      },
    ]);
    const transport = createTauriNativeTransport(bridge);
    const native = createNativeBridge(transport);
    const result = await native.clipboardPaste();
    expect(result).toBe("hello");
  });

  it("accepts an empty string when clipboard is empty", async () => {
    const bridge = fakeTauriBridge([
      {
        route: "plugin:evener-native|clipboard_paste",
        result: { text: "" },
      },
    ]);
    const transport = createTauriNativeTransport(bridge);
    const native = createNativeBridge(transport);
    const result = await native.clipboardPaste();
    expect(result).toBe("");
  });

  it("rejects a missing `text` field", async () => {
    const bridge = fakeTauriBridge([
      {
        route: "plugin:evener-native|clipboard_paste",
        result: {},
      },
    ]);
    const transport = createTauriNativeTransport(bridge);
    const native = createNativeBridge(transport);
    await expect(native.clipboardPaste()).rejects.toThrow();
  });

  it("rejects a wrong-typed `text` field", async () => {
    const bridge = fakeTauriBridge([
      {
        route: "plugin:evener-native|clipboard_paste",
        result: { text: 42 },
      },
    ]);
    const transport = createTauriNativeTransport(bridge);
    const native = createNativeBridge(transport);
    await expect(native.clipboardPaste()).rejects.toThrow();
  });

  it("rejects extra fields in the clipboard paste raw shape", async () => {
    const bridge = fakeTauriBridge([
      {
        route: "plugin:evener-native|clipboard_paste",
        result: { text: "hello", extra: "no" },
      },
    ]);
    const transport = createTauriNativeTransport(bridge);
    const native = createNativeBridge(transport);
    await expect(native.clipboardPaste()).rejects.toThrow();
  });
});

// ---------------------------------------------------------------------------
// Decoded responses — no version/type leak from versioned scan response
// ---------------------------------------------------------------------------

describe("7A transport: decoded scan result has no version/type leak", () => {
  it("scan result is a decoded PairingPreview, not a raw shape", async () => {
    const bridge = fakeTauriBridge([
      {
        route: "plugin:evener-native|scan_and_preview_pairing",
        result: {
          version: 1,
          type: "pairing.preview",
          previewId: "pv-2",
          origin: "https://hub.test:9000",
        },
      },
    ]);
    const transport = createTauriNativeTransport(bridge);
    const native = createNativeBridge(transport);
    const result = await native.scanAndPreviewPairing();
    expect(result).toEqual({
      previewId: "pv-2",
      origin: "https://hub.test:9000",
    });
    expect(result).not.toHaveProperty("version");
    expect(result).not.toHaveProperty("type");
  });
});

// ---------------------------------------------------------------------------
// Native error propagation — versioned scan error becomes NativeBridgeError
// ---------------------------------------------------------------------------

describe("7A transport: native error propagation", () => {
  it("a versioned scan error response becomes NativeBridgeError", async () => {
    const bridge = fakeTauriBridge([
      {
        route: "plugin:evener-native|scan_and_preview_pairing",
        result: {
          version: 1,
          type: "error",
          error: {
            id: "scan-and-preview",
            kind: "pairing_unavailable",
            message: "Pairing scan is unavailable on this platform",
          },
        },
      },
    ]);
    const transport = createTauriNativeTransport(bridge);
    const native = createNativeBridge(transport);
    try {
      await native.scanAndPreviewPairing();
      expect.unreachable("should have thrown");
    } catch (err) {
      expect(err).toBeInstanceOf(NativeBridgeError);
      expect((err as NativeBridgeError).error.kind).toBe("pairing_unavailable");
      expect((err as NativeBridgeError).error.message).toBe(
        "Pairing scan is unavailable on this platform",
      );
    }
  });

  it("rejects a malformed error kind", async () => {
    const bridge = fakeTauriBridge([
      {
        route: "plugin:evener-native|scan_and_preview_pairing",
        result: {
          version: 1,
          type: "error",
          error: {
            id: "scan-and-preview",
            kind: "totally_made_up",
            message: "bad kind",
          },
        },
      },
    ]);
    const transport = createTauriNativeTransport(bridge);
    const native = createNativeBridge(transport);
    await expect(native.scanAndPreviewPairing()).rejects.toThrow();
  });

  it("rejects an error with extra fields on the error object", async () => {
    const bridge = fakeTauriBridge([
      {
        route: "plugin:evener-native|scan_and_preview_pairing",
        result: {
          version: 1,
          type: "error",
          error: {
            id: "scan-and-preview",
            kind: "internal",
            message: "ok",
            token: "must-not-cross",
          },
        },
      },
    ]);
    const transport = createTauriNativeTransport(bridge);
    const native = createNativeBridge(transport);
    await expect(native.scanAndPreviewPairing()).rejects.toThrow();
  });
});
