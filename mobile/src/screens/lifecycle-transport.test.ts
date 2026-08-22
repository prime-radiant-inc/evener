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
  readonly promise: Promise<UnlistenFn>;
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
      let resolve!: (fn: UnlistenFn) => void;
      let reject!: (err: unknown) => void;
      const promise = new Promise<UnlistenFn>((res, rej) => {
        resolve = res;
        reject = rej;
      });
      const deferred: DeferredListen = {
        event,
        id,
        promise,
        resolve,
        reject,
        unlistenCalls: 0,
      };
      deferredListens.push(deferred);
      return promise;
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
    // Await the listen promises to ensure the adapter's .then has run.
    await Promise.all(bridge.deferredListens.map((d) => d.promise));
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
    await Promise.all(bridge.deferredListens.map((d) => d.promise));
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
    await Promise.all(bridge.deferredListens.map((d) => d.promise));
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
    // Await the listen promises so the adapter's .then callbacks settle.
    await Promise.all(bridge.deferredListens.map((d) => d.promise));
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
    await first?.promise;
    // Emit before unsubscribe — should deliver.
    bridge.emit("tauri://suspended", null);
    expect(states).toContain("background");
    unsub();
    // Resolve the second listen promise after unsubscribe — must not deliver.
    if (second) bridge.resolveListen(second.id);
    await second?.promise;
    bridge.emit("tauri://resumed", null);
    expect(states).not.toContain("foreground");
  });

  it("one listen rejects after unsubscribe without unhandled rejection", async () => {
    const bridge = fakeTauriBridgeWithEvents();
    const transport = createTauriNativeTransport(bridge);
    const unsub = transport.subscribe("lifecycle.changed", () => {});
    // Unsubscribe BEFORE rejecting — the adapter's .catch must swallow it.
    unsub();
    const [first, second] = bridge.deferredListens;
    if (first) bridge.rejectListen(first.id);
    if (second) bridge.resolveListen(second.id);
    // Await both promises — the rejected one must settle without throwing.
    const results = await Promise.allSettled(
      bridge.deferredListens.map((d) => d.promise),
    );
    // First rejected, second fulfilled — no unhandled rejection.
    expect(results[0]?.status).toBe("rejected");
    expect(results[1]?.status).toBe("fulfilled");
  });

  it("both listen promises reject after unsubscribe without unhandled rejection", async () => {
    const bridge = fakeTauriBridgeWithEvents();
    const transport = createTauriNativeTransport(bridge);
    const unsub = transport.subscribe("lifecycle.changed", () => {});
    // Unsubscribe BEFORE rejecting both.
    unsub();
    bridge.rejectListen(bridge.deferredListens[0]?.id ?? 0);
    bridge.rejectListen(bridge.deferredListens[1]?.id ?? 0);
    // Await both — both rejected, no unhandled rejection.
    const results = await Promise.allSettled(
      bridge.deferredListens.map((d) => d.promise),
    );
    expect(results[0]?.status).toBe("rejected");
    expect(results[1]?.status).toBe("rejected");
    // Double-unsubscribe must still be safe after rejections.
    expect(() => unsub()).not.toThrow();
  });
});
