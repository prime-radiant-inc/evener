/**
 * RED test 1: Production transport — exact routes and envelopes.
 *
 * Drives the real production adapter through an injected fake `TauriBridge`
 * to assert that `NativeBridge.scanAndPreviewPairing`, `hapticPerform`, and
 * `getContentSize` call the exact Tauri plugin routes with exact envelopes.
 * Asserts decoded bridge-visible responses, not source substrings.
 */
import { describe, expect, it, vi } from "vitest";
import type { NativeTransport } from "../native/client";
import { createNativeBridge, type NativeBridge } from "../native/client";
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
    createChannel<T>() {
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

import { createTauriNativeTransport } from "./production-transport";

// ---------------------------------------------------------------------------
// Tests
// ---------------------------------------------------------------------------

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

describe("7A transport: hapticPerform route and response", () => {
  it("invokes plugin:evener-native|haptic_perform with {payload:{kind}}", async () => {
    const bridge = fakeTauriBridge([
      {
        route: "plugin:evener-native|haptic_perform",
        result: { version: 1, type: "haptic.completed" },
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
});

describe("7A transport: getContentSize route and response", () => {
  it("invokes plugin:evener-native|content_size_get with {payload:{}}", async () => {
    const bridge = fakeTauriBridge([
      {
        route: "plugin:evener-native|content_size_get",
        result: {
          version: 1,
          type: "contentSize.value",
          category: "extraExtraLarge",
        },
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
});

describe("7A transport: no `as never` masking — decoded responses", () => {
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
    // The result should have exactly {previewId, origin}, no version/type leak.
    expect(result).toEqual({
      previewId: "pv-2",
      origin: "https://hub.test:9000",
    });
    expect(result).not.toHaveProperty("version");
    expect(result).not.toHaveProperty("type");
  });

  it("contentSize result is a decoded category string", async () => {
    const bridge = fakeTauriBridge([
      {
        route: "plugin:evener-native|content_size_get",
        result: {
          version: 1,
          type: "contentSize.value",
          category: "accessibilityLarge",
        },
      },
    ]);
    const transport = createTauriNativeTransport(bridge);
    const native = createNativeBridge(transport);
    const result = await native.getContentSize();
    expect(result).toBe("accessibilityLarge");
    expect(typeof result).toBe("string");
  });
});
