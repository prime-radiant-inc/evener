/**
 * The single import seam for `@tauri-apps/api`.
 *
 * Every other service receives an injected {@link TauriBridge} so tests can
 * substitute a fake invoke/channel boundary. No service outside this file
 * imports `@tauri-apps/api` directly. The real implementation delegates to
 * Tauri's `invoke` and `Channel`; the browser/test fixture injects a fake.
 */

import { Channel, invoke } from "@tauri-apps/api/core";

/**
 * A Tauri IPC channel as seen by services. The real {@link Channel} relays
 * serialized backend events to `onmessage` and is itself serializable when
 * passed as a command argument (via its IPC marker). The structural type
 * lets services depend on the surface without importing `@tauri-apps/api`.
 */
export interface TauriChannel<T = unknown> {
  readonly id: number;
  onmessage: (response: T) => void;
}

/**
 * The transport surface services depend on. Wrapping `invoke` and channel
 * creation here keeps `@tauri-apps/api` imported from exactly one module.
 */
export interface TauriBridge {
  invoke<T = unknown>(cmd: string, args?: Record<string, unknown>): Promise<T>;
  createChannel<T = unknown>(onMessage: (response: T) => void): TauriChannel<T>;
}

/**
 * Real Tauri bridge backed by `@tauri-apps/api/core`.
 *
 * `Channel` relays serialized backend events to its `onmessage` handler and
 * carries the IPC serialization marker Tauri needs when it is passed as a
 * command argument. Returning the real instance preserves that marker.
 */
export function createTauriBridge(): TauriBridge {
  return {
    invoke,
    createChannel<T>(onMessage: (response: T) => void): TauriChannel<T> {
      return new Channel<T>(onMessage);
    },
  };
}
