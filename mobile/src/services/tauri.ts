/**
 * The single import seam for `@tauri-apps/api`.
 *
 * Every other service receives an injected {@link TauriBridge} so tests can
 * substitute a fake invoke/channel/listen boundary. No service outside this
 * file imports `@tauri-apps/api` directly. The real implementation delegates
 * to Tauri's `invoke`, `Channel`, and event `listen`; the browser/test fixture
 * injects a fake.
 */
import { Channel, invoke } from "@tauri-apps/api/core";
import { listen } from "@tauri-apps/api/event";

/**
 * A Tauri IPC channel as seen by services. The real {@link Channel} relays
 * serialized backend events to `onmessage` and is itself serializable when
 * passed as a command argument (via its IPC marker). The structural type
 * lets services depend on the surface without importing `@tauri-apps/api`.
 */
export interface TauriChannel<T = unknown> {
  readonly id: number;
  onmessage: (response: T) => void;
  onclose: (() => void) | null;
  /** Idempotently unregister the underlying Tauri callback. */
  dispose(): void;
}

/** A Tauri event as received by `listen`. */
export interface TauriEvent<T = unknown> {
  readonly event: string;
  readonly id: number;
  readonly payload: T;
}

/** A function to unlisten a Tauri event. */
export type UnlistenFn = () => void;

/**
 * The transport surface services depend on. Wrapping `invoke`, `createChannel`,
 * and `listen` here keeps `@tauri-apps/api` imported from exactly one module.
 */
export interface TauriBridge {
  invoke<T = unknown>(
    cmd: string,
    args?: Record<string, unknown> | ArrayBuffer | Uint8Array,
    options?: { readonly headers: HeadersInit },
  ): Promise<T>;
  createChannel<T = unknown>(onMessage: (response: T) => void): TauriChannel<T>;
  listen<T = unknown>(
    event: string,
    handler: (event: TauriEvent<T>) => void,
  ): Promise<UnlistenFn>;
}

/**
 * Real Tauri bridge backed by `@tauri-apps/api/core` and `@tauri-apps/api/event`.
 *
 * `Channel` relays serialized backend events to its `onmessage` handler and
 * carries the IPC serialization marker Tauri needs when it is passed as a
 * command argument. Returning the real instance preserves that marker.
 */
export function createTauriBridge(): TauriBridge {
  return {
    invoke,
    createChannel<T>(onMessage: (response: T) => void): TauriChannel<T> {
      const channel = new Channel<T>(onMessage);
      const internal = channel as unknown as {
        cleanupCallback(): void;
      };
      const cleanup = internal.cleanupCallback.bind(channel);
      const exposed = channel as unknown as TauriChannel<T>;
      let disposed = false;
      exposed.onclose = null;
      exposed.dispose = () => {
        if (disposed) return;
        disposed = true;
        cleanup();
        exposed.onclose?.();
      };
      // Tauri 2.11 Channel has no public close method, but its runtime invokes
      // this callback when Rust drops the last Channel owner. Wrap that pinned
      // implementation hook so service code can observe end-of-channel and the
      // explicit dispose path shares the same idempotent unregister operation.
      internal.cleanupCallback = exposed.dispose;
      return exposed;
    },
    listen<T>(
      event: string,
      handler: (e: TauriEvent<T>) => void,
    ): Promise<UnlistenFn> {
      return listen<T>(event, handler);
    },
  };
}
