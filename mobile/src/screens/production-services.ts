/**
 * Production service factory — wires real Tauri-backed services.
 *
 * The real {@link TauriBridge} delegates to `@tauri-apps/api/core` (imported
 * from exactly one seam: `services/tauri.ts`). The real {@link ProfileService}
 * wraps the Tauri bridge. The real {@link NativeBridge} wraps a Tauri-backed
 * {@link NativeTransport} that invokes the Rust plugin's `native_bridge` command.
 *
 * This module is imported only by the production App path — never by tests or
 * fixture mode. No credential, raw URL, or token is ever held in JS state.
 */
import type { NativeBridge, NativeTransport } from "../native/client";
import { createNativeBridge } from "../native/client";
import type {
  LifecycleState,
  NativeCommand,
  NativeResponse,
} from "../native/contract";
import { decodeNativeResponse } from "../native/contract";
import type { ProfileService } from "../services/nativeProfiles";
import { createProfileService } from "../services/nativeProfiles";
import { createTauriBridge, type TauriBridge } from "../services/tauri";

export interface ProductionServices {
  readonly profile: ProfileService;
  readonly native: NativeBridge;
  readonly hapticCalls: never[];
  readonly contentSize: () => string;
}

/**
 * Build the production service bundle backed by real Tauri IPC. This is the
 * only path that imports `createTauriBridge` and `createProfileService` for
 * live use — fixture mode injects fakes instead.
 */
export function createProductionServices(): ProductionServices {
  const bridge = createTauriBridge();
  const profile = createProfileService(bridge);
  const native = createNativeBridge(createTauriNativeTransport(bridge));
  return {
    profile,
    native,
    hapticCalls: [],
    contentSize: () => "large",
  };
}

/**
 * Tauri-backed {@link NativeTransport}. Sends a {@link NativeCommand} via
 * Tauri `invoke` to the Rust plugin's `native_bridge` command, which routes
 * the typed command to Swift and returns a typed {@link NativeResponse}.
 * Lifecycle events arrive via a Tauri channel subscription.
 */
function createTauriNativeTransport(bridge: TauriBridge): NativeTransport {
  return {
    async send(command: NativeCommand): Promise<NativeResponse> {
      const raw = await bridge.invoke("native_bridge", {
        request: command,
      });
      return decodeNativeResponse(raw);
    },

    subscribe(type, handler) {
      if (type !== "lifecycle.changed") {
        return () => {};
      }
      const channel = bridge.createChannel<{ state: LifecycleState }>(
        (response) => {
          handler({ state: response.state });
        },
      );
      // The Rust side pushes lifecycle events to this channel. The channel is
      // registered by invoking `native_subscribe_lifecycle` with the channel.
      void bridge.invoke("native_subscribe_lifecycle", {
        channel,
      });
      return () => {
        channel.dispose();
      };
    },
  };
}
