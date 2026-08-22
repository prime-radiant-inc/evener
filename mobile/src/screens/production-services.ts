/**
 * Production service factory — wires real Tauri-backed services.
 *
 * The real {@link TauriBridge} delegates to `@tauri-apps/api` (imported from
 * exactly one seam: `services/tauri.ts`). The real {@link ProfileService}
 * wraps the Tauri bridge. The real {@link NativeBridge} wraps the production
 * transport adapter ({@link createTauriNativeTransport}) that invokes actual
 * registered plugin commands via namespaced routes.
 *
 * This module is imported only by the production App path — never by tests or
 * fixture mode. No credential, raw URL, or token is ever held in JS state.
 */
import type { NativeBridge } from "../native/client";
import { createNativeBridge } from "../native/client";
import type { ProfileService } from "../services/nativeProfiles";
import { createProfileService } from "../services/nativeProfiles";
import { createTauriBridge } from "../services/tauri";
import { createTauriNativeTransport } from "./production-transport";

export interface ProductionServices {
  readonly profile: ProfileService;
  readonly native: NativeBridge;
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
  return { profile, native };
}
