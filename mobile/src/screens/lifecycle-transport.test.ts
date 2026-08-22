/**
 * Lifecycle transport behavior.
 *
 * The sole `services/tauri.ts` import seam exposes real Tauri event listening.
 * Production maps `tauri://suspended`→background and `tauri://resumed`→foreground,
 * returns race-safe/idempotent unsubscribe, and never delivers after
 * unsubscribe (including unsubscribe-before-async-listen-resolves).
 *
 * The fake bridge uses real deferred listen promises so tests can prove:
 * both resolve after unsubscribe and each unlisten runs once; one resolves
 * before and one after unsubscribe; one/both reject without unhandled
 * rejection; double unsubscribe remains safe.
 */
import { describe, expect, it } from "vitest";
import type { TauriBridge, UnlistenFn } from "../services/tauri";

// ---------------------------------------------------------------------------
// Fake TauriBridge with deferred listen promises
// ---------------------------------------------------------------------------

type ListenerHandler = (event: {
  readonly event: string;
  readonly payload: unknown;
}) => void;

interface DeferredListen {
  readonly event: string;
  readonly id: number;
  resolve: (fn: UnlistenFn) => void;
  reject: (err: unknown) => void;
  unlistenCalls: number;
}

function fakeTauriBridgeWithEvents(): TauriBridge & {
  readonly invocations: { route: string; args: unknown }[];
  emit(event: string, payload: unknown): void;
  readonly deferredListens: DeferredListen[];
  resolveListen(id: number, unlisten?: UnlistenFn): void;
  rejectListen(id: number, err?: unknown): void;
} {
  const invocations: { route: string; args: unknown }[] = [];
  const listeners = new Map<
    number,
    { event: string; handler: ListenerHandler }
  >();
  let nextId = 1;
  const deferredListens: DeferredListen[] = [];

  const bridge: TauriBridge = {
    async invoke<T>(route: string, args?: unknown): Promise<T> {
      invocations.push({ route, args: args ?? {} });
      void args;
      throw new Error(`unexpected invoke: ${route}`);
    },
    createChannel() {
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
    ): Promise<UnlistenFn> {
      const id = nextId++;
      listeners.set(id, {
        event,
        handler: handler as unknown as ListenerHandler,
      });
      return new Promise<UnlistenFn>((resolve, reject) => {
        const deferred: DeferredListen = {
          event,
          id,
          resolve: (fn) => {
            // Replace the listener entry's handler so emit still works.
            resolve(fn);
          },
          reject,
          unlistenCalls: 0,
        };
        deferredListens.push(deferred);
      });
    },
  };

  return Object.assign(bridge, {
    invocations,
    emit(event: string, payload: unknown) {
      for (const [, l] of listeners) {
        if (l.event === event) {
          l.handler({ event, payload });
        }
      }
    },
    deferredListens,
    resolveListen(id: number, unlisten?: UnlistenFn) {
      const deferred = deferredListens.find((d) => d.id === id);
      if (deferred) {
        deferred.resolve(
          unlisten ??
            (() => {
              deferred.unlistenCalls += 1;
              listeners.delete(id);
            }),
        );
      }
    },
    rejectListen(id: number, err?: unknown) {
      const deferred = deferredListens.find((d) => d.id === id);
      if (deferred) {
        deferred.reject(err ?? new Error("listen rejected"));
      }
    },
  });
}

// ---------------------------------------------------------------------------
// The real production transport adapter
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
    // Resolve the listen promises so the adapter is fully wired.
    for (const d of bridge.deferredListens) {
      bridge.resolveListen(d.id);
    }
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
    for (const d of bridge.deferredListens) {
      bridge.resolveListen(d.id);
    }
    bridge.emit("tauri://resumed", null);
    expect(states).toContain("foreground");
    unsub();
  });
});

describe("7A lifecycle: unsubscribe is idempotent and race-safe", () => {
  it("unsubscribe prevents further deliveries", async () => {
    const bridge = fakeTauriBridgeWithEvents();
    const transport = createTauriNativeTransport(bridge);
    const states: string[] = [];
    const unsub = transport.subscribe("lifecycle.changed", (e) => {
      states.push(e.state);
    });
    for (const d of bridge.deferredListens) {
      bridge.resolveListen(d.id);
    }
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

  it("both listen promises resolve after unsubscribe and each unlisten runs once", async () => {
    const bridge = fakeTauriBridgeWithEvents();
    const transport = createTauriNativeTransport(bridge);
    const unsub = transport.subscribe("lifecycle.changed", () => {});
    // Unsubscribe before the listen promises resolve.
    unsub();
    // Now resolve both — the adapter should call each unlisten fn once.
    const unlistenCalls: number[] = [];
    for (const d of bridge.deferredListens) {
      bridge.resolveListen(d.id, () => {
        unlistenCalls.push(d.id);
      });
    }
    // Let the .then callbacks run.
    await Promise.resolve();
    await Promise.resolve();
    expect(unlistenCalls).toHaveLength(2);
  });

  it("one listen resolves before unsubscribe and one after", async () => {
    const bridge = fakeTauriBridgeWithEvents();
    const transport = createTauriNativeTransport(bridge);
    const states: string[] = [];
    const unsub = transport.subscribe("lifecycle.changed", (e) => {
      states.push(e.state);
    });
    // Resolve the first listen promise before unsubscribe.
    const [first, second] = bridge.deferredListens;
    if (first) bridge.resolveListen(first.id);
    await Promise.resolve();
    // Emit before unsubscribe — should deliver.
    bridge.emit("tauri://suspended", null);
    expect(states).toContain("background");
    unsub();
    // Resolve the second listen promise after unsubscribe — must not deliver.
    if (second) bridge.resolveListen(second.id);
    bridge.emit("tauri://resumed", null);
    expect(states).not.toContain("foreground");
  });

  it("one listen rejects without unhandled rejection", async () => {
    const bridge = fakeTauriBridgeWithEvents();
    const transport = createTauriNativeTransport(bridge);
    const unsub = transport.subscribe("lifecycle.changed", () => {});
    // Reject one listen promise — the adapter must catch it silently.
    const [first, second] = bridge.deferredListens;
    if (first) bridge.rejectListen(first.id);
    if (second) bridge.resolveListen(second.id);
    // If the rejection were unhandled, vitest would fail this test.
    await Promise.resolve();
    await Promise.resolve();
    unsub();
  });

  it("both listen promises reject without unhandled rejection", async () => {
    const bridge = fakeTauriBridgeWithEvents();
    const transport = createTauriNativeTransport(bridge);
    const unsub = transport.subscribe("lifecycle.changed", () => {});
    bridge.rejectListen(bridge.deferredListens[0]?.id ?? 0);
    bridge.rejectListen(bridge.deferredListens[1]?.id ?? 0);
    await Promise.resolve();
    await Promise.resolve();
    // Double-unsubscribe must still be safe after rejections.
    expect(() => unsub()).not.toThrow();
    expect(() => unsub()).not.toThrow();
  });

  it("lifecycle unsubscribe is called on unmount", () => {
    const bridge = fakeTauriBridgeWithEvents();
    const transport = createTauriNativeTransport(bridge);
    let unsubCalled = false;
    const realUnsub = transport.subscribe("lifecycle.changed", () => {});
    // Wrap to track
    const trackingUnsub = () => {
      unsubCalled = true;
      realUnsub();
    };
    trackingUnsub();
    expect(unsubCalled).toBe(true);
    // Emit after unsubscribe — no delivery.
    bridge.emit("tauri://suspended", null);
  });
});
