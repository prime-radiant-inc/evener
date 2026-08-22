/**
 * Typed client for the native bridge.
 *
 * Service interfaces call this adapter; stores never invoke Tauri directly.
 * The client is injected so tests substitute {@link fakeNativeBridge}.
 */

import type {
  ContentSizeCategory,
  HapticKind,
  LifecycleState,
  NativeCommand,
  NativeError,
  NativeResponse,
  PermissionKind,
} from "./contract";

import { decodeNativeResponse, NATIVE_BRIDGE_VERSION } from "./contract";

// ---------------------------------------------------------------------------
// Public service interface
// ---------------------------------------------------------------------------

export interface NativeBridge {
  readonly version: number;
  secureGet(profileId: string): Promise<SecureState>;
  secureSet(profileId: string, capability: string): Promise<SecureUpdated>;
  secureDelete(profileId: string): Promise<SecureDeleted>;
  scanAndPreviewPairing(): Promise<PairingPreview>;
  requestPermission(kind: PermissionKind): Promise<PermissionStatus>;
  speechStart(): Promise<void>;
  speechStop(): Promise<void>;
  synthesisSpeak(text: string): Promise<void>;
  synthesisStop(): Promise<void>;
  hapticPerform(kind: HapticKind): Promise<void>;
  getContentSize(): Promise<ContentSizeCategory>;
  onLifecycle(handler: (state: LifecycleState) => void): () => void;
}

export interface SecureState {
  readonly present: boolean;
}

export interface SecureUpdated {
  readonly stored: boolean;
}

export interface SecureDeleted {
  readonly deleted: boolean;
}

export interface PairingPreview {
  readonly previewId: string;
  readonly origin: string;
}

export interface PermissionStatus {
  readonly kind: PermissionKind;
  readonly granted: boolean;
}

// ---------------------------------------------------------------------------
// Error
// ---------------------------------------------------------------------------

export class NativeBridgeError extends Error {
  readonly error: NativeError;
  constructor(error: NativeError) {
    super(error.message);
    this.name = "NativeBridgeError";
    this.error = error;
  }
}

// ---------------------------------------------------------------------------
// Transport abstraction — real impl uses Tauri invoke; tests inject a fake
// ---------------------------------------------------------------------------

export interface NativeTransport {
  send(command: NativeCommand): Promise<NativeResponse>;
  subscribe(
    type: "lifecycle.changed",
    handler: (event: { state: LifecycleState }) => void,
  ): () => void;
}

function unwrap(response: NativeResponse): NativeResponse {
  if (response.type === "error") {
    throw new NativeBridgeError(response.error);
  }
  return response;
}

// ---------------------------------------------------------------------------
// Concrete bridge backed by a transport
// ---------------------------------------------------------------------------

export function createNativeBridge(transport: NativeTransport): NativeBridge {
  return {
    version: NATIVE_BRIDGE_VERSION,

    async secureGet(profileId) {
      const res = unwrap(
        await transport.send({ version: 1, type: "secure.get", profileId }),
      );
      if (res.type !== "secure.state") {
        throw new NativeBridgeError({
          id: "client",
          kind: "internal",
          message: "unexpected response",
        });
      }
      return { present: res.present };
    },

    async secureSet(profileId, capability) {
      const res = unwrap(
        await transport.send({
          version: 1,
          type: "secure.set",
          profileId,
          capability,
        }),
      );
      if (res.type !== "secure.updated") {
        throw new NativeBridgeError({
          id: "client",
          kind: "internal",
          message: "unexpected response",
        });
      }
      return { stored: res.stored };
    },

    async secureDelete(profileId) {
      const res = unwrap(
        await transport.send({ version: 1, type: "secure.delete", profileId }),
      );
      if (res.type !== "secure.deleted") {
        throw new NativeBridgeError({
          id: "client",
          kind: "internal",
          message: "unexpected response",
        });
      }
      return { deleted: res.deleted };
    },

    async scanAndPreviewPairing() {
      const res = unwrap(
        await transport.send({ version: 1, type: "pairing.scanAndPreview" }),
      );
      if (res.type !== "pairing.preview") {
        throw new NativeBridgeError({
          id: "client",
          kind: "internal",
          message: "unexpected response",
        });
      }
      return { previewId: res.previewId, origin: res.origin };
    },

    async requestPermission(kind) {
      const res = unwrap(
        await transport.send({ version: 1, type: "permission.request", kind }),
      );
      if (res.type !== "permission.status") {
        throw new NativeBridgeError({
          id: "client",
          kind: "internal",
          message: "unexpected response",
        });
      }
      return { kind: res.kind, granted: res.granted };
    },

    async speechStart() {
      unwrap(await transport.send({ version: 1, type: "speech.start" }));
    },

    async speechStop() {
      unwrap(await transport.send({ version: 1, type: "speech.stop" }));
    },

    async synthesisSpeak(text) {
      unwrap(
        await transport.send({ version: 1, type: "synthesis.speak", text }),
      );
    },

    async synthesisStop() {
      unwrap(await transport.send({ version: 1, type: "synthesis.stop" }));
    },

    async hapticPerform(kind) {
      unwrap(
        await transport.send({ version: 1, type: "haptic.perform", kind }),
      );
    },

    async getContentSize() {
      const res = unwrap(
        await transport.send({ version: 1, type: "contentSize.get" }),
      );
      if (res.type !== "contentSize.value") {
        throw new NativeBridgeError({
          id: "client",
          kind: "internal",
          message: "unexpected response",
        });
      }
      return res.category;
    },

    onLifecycle(handler) {
      return transport.subscribe("lifecycle.changed", (e) => handler(e.state));
    },
  };
}

// Re-export decode for consumers that validate raw plugin output
export { decodeNativeResponse };
