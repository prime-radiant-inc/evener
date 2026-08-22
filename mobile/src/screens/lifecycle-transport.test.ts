/**
 * RED test 3: Lifecycle transport behavior.
 *
 * The sole `services/tauri.ts` import seam exposes real Tauri event listening.
 * Production maps `tauri://suspended`→background and `tauri://resumed`→foreground,
 * returns race-safe/idempotent unsubscribe, and never delivers after
 * unsubscribe (including unsubscribe-before-async-listen-resolves).
 */
import { describe, expect, it, vi } from "vitest";
import type { TauriBridge } from "../services/tauri";

// ---------------------------------------------------------------------------
// Fake TauriBridge with listen/unlisten support
// ---------------------------------------------------------------------------

type ListenerHandler = (event: { event: string; payload: unknown }) => void;

function fakeTauriBridgeWithEvents(): TauriBridge & {
  readonly invocations: { route: string; args: unknown }[];
  emit(event: string, payload: unknown): void;
  pendingResolvers: Array<() => void>;
} {
  const invocations: { route: string; args: unknown }[] = [];
  const listeners = new Map<
    number,
    { event: string; handler: ListenerHandler }
  >();
  let nextId = 1;
  const pendingResolvers: Array<() => void> = [];

  const bridge: TauriBridge = {
    async invoke<T>(route: string, args?: unknown): Promise<T> {
      invocations.push({ route, args: args ?? {} });
      // The listen command returns an unlisten function ID.
      if (route === "plugin:event|listen") {
        const listenArgs = args as { event: string; handler: number };
        const id = nextId++;
        const handler = listenArgs.handler as unknown as number;
        // Store the handler number; the real Tauri runtime maps this to a callback.
        // For the test, we store a placeholder and resolve the listen promise.
        return { id, eventId: id } as T;
      }
      throw new Error(`unexpected invoke: ${route}`);
    },
    createChannel<T>() {
      return {
        id: 0,
        onmessage: () => {},
        onclose: null,
        dispose: () => {},
      } as never;
    },
    listen<T>(
      event: string,
      handler: (e: {
        readonly event: string;
        readonly id: number;
        readonly payload: T;
      }) => void,
    ): Promise<() => void> {
      const id = nextId++;
      listeners.set(id, {
        event,
        handler: handler as unknown as ListenerHandler,
      });
      return Promise.resolve(() => {
        listeners.delete(id);
      });
    },
  };

  const ext = Object.assign(bridge, {
    invocations,
    emit(event: string, payload: unknown) {
      for (const [, l] of listeners) {
        if (l.event === event) {
          l.handler({ event, payload });
        }
      }
    },
    pendingResolvers,
  });
  return ext;
}

// ---------------------------------------------------------------------------
// Tests — these test the production transport adapter's subscribe method
// ---------------------------------------------------------------------------

import { createTauriNativeTransport } from "./production-transport";

describe("7A lifecycle: subscribe maps suspended→background and resumed→foreground", () => {
  it("delivers background state on tauri://suspended", async () => {
    const bridge = fakeTauriBridgeWithEvents();
    const transport = createTauriNativeTransport(bridge);
    const states: string[] = [];
    const unsub = transport.subscribe("lifecycle.changed", (e) => {
      states.push(e.state);
    });
    // Simulate the Tauri event arriving.
    bridge.emit("tauri://suspended", null);
    expect(states).toContain("background");
    unsub();
  });

  it("delivers foreground state on tauri://resumed", async () => {
    const bridge = fakeTauriBridgeWithEvents();
    const transport = createTauriNativeTransport(bridge);
    const states: string[] = [];
    const unsub = transport.subscribe("lifecycle.changed", (e) => {
      states.push(e.state);
    });
    bridge.emit("tauri://resumed", null);
    expect(states).toContain("foreground");
    unsub();
  });
});

describe("7A lifecycle: unsubscribe is idempotent and race-safe", () => {
  it("unsubscribe prevents further deliveries", () => {
    const bridge = fakeTauriBridgeWithEvents();
    const transport = createTauriNativeTransport(bridge);
    const states: string[] = [];
    const unsub = transport.subscribe("lifecycle.changed", (e) => {
      states.push(e.state);
    });
    unsub();
    bridge.emit("tauri://suspended", null);
    expect(states).toHaveLength(0);
  });

  it("double unsubscribe does not throw", () => {
    const bridge = fakeTauriBridgeWithEvents();
    const transport = createTauriNativeTransport(bridge);
    const unsub = transport.subscribe("lifecycle.changed", () => {});
    unsub();
    expect(() => unsub()).not.toThrow();
  });
});
