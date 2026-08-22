/**
 * Fake native bridge for tests and the browser fixture mode.
 *
 * Implements the {@link NativeTransport} contract so stores and services can be
 * exercised without a real Tauri plugin. No saved token ever appears in any
 * response, error, or recorded state.
 */

import type { NativeTransport } from "./client";
import type {
  ContentSizeCategory,
  HapticKind,
  LifecycleState,
  NativeCommand,
  NativeResponse,
  PermissionKind,
} from "./contract";
import { NATIVE_BRIDGE_VERSION } from "./contract";

export interface FakeNativeBridgeOptions {
  readonly contentSize?: ContentSizeCategory;
  readonly permissions?: Partial<Record<PermissionKind, boolean>>;
}

export class FakeNativeBridge implements NativeTransport {
  readonly version = NATIVE_BRIDGE_VERSION;
  private contentSize: ContentSizeCategory;
  private readonly permissions: Partial<Record<PermissionKind, boolean>>;
  private readonly secureStore = new Map<string, string>();
  private readonly lifecycleHandlers = new Set<
    (e: { state: LifecycleState }) => void
  >();
  readonly commands: NativeCommand[] = [];

  constructor(options: FakeNativeBridgeOptions = {}) {
    this.contentSize = options.contentSize ?? "large";
    this.permissions = options.permissions ?? {};
  }

  // -- NativeTransport ------------------------------------------------------

  async send(command: NativeCommand): Promise<NativeResponse> {
    this.commands.push(command);
    return this.handle(command);
  }

  subscribe(
    _type: "lifecycle.changed",
    handler: (e: { state: LifecycleState }) => void,
  ): () => void {
    this.lifecycleHandlers.add(handler);
    return () => {
      this.lifecycleHandlers.delete(handler);
    };
  }

  // -- Test helpers --------------------------------------------------------

  emitLifecycle(state: LifecycleState): void {
    for (const h of this.lifecycleHandlers) {
      h({ state });
    }
  }

  setPermission(kind: PermissionKind, granted: boolean): void {
    this.permissions[kind] = granted;
  }

  setContentSize(category: ContentSizeCategory): void {
    this.contentSize = category;
  }

  hasProfile(profileId: string): boolean {
    return this.secureStore.has(profileId);
  }

  /** Inject a stored capability without going through the bridge (setup only). */
  seedProfile(profileId: string, capability: string): void {
    this.secureStore.set(profileId, capability);
  }

  // -- Handler --------------------------------------------------------------

  private handle(command: NativeCommand): NativeResponse {
    switch (command.type) {
      case "secure.get":
        return {
          version: 1,
          type: "secure.state",
          present: this.secureStore.has(command.profileId),
        };
      case "secure.set":
        this.secureStore.set(command.profileId, command.capability);
        return { version: 1, type: "secure.updated", stored: true };
      case "secure.delete":
        this.secureStore.delete(command.profileId);
        return { version: 1, type: "secure.deleted", deleted: true };
      case "pairing.scanAndPreview":
        // Until Task 5 wires real pairing, production returns structured
        // pairing_unavailable; the fake returns it too so tests mirror prod.
        return {
          version: 1,
          type: "error",
          error: {
            id: "fake-pairing",
            kind: "pairing_unavailable",
            message: "Pairing is unavailable",
          },
        };
      case "permission.request": {
        const granted = this.permissions[command.kind] ?? false;
        return {
          version: 1,
          type: "permission.status",
          kind: command.kind,
          granted,
        };
      }
      case "speech.start":
        return { version: 1, type: "speech.ready" };
      case "speech.stop":
        return { version: 1, type: "speech.stopped" };
      case "synthesis.speak":
        return { version: 1, type: "synthesis.started" };
      case "synthesis.stop":
        return { version: 1, type: "synthesis.stopped" };
      case "haptic.perform":
        return { version: 1, type: "haptic.completed" };
      case "contentSize.get":
        return {
          version: 1,
          type: "contentSize.value",
          category: this.contentSize,
        };
      default: {
        // Exhaustiveness check
        const _exhaustive: never = command;
        return _exhaustive;
      }
    }
  }
}
