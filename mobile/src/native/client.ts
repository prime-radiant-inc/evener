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
  NativeEvent,
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
  clipboardPaste(): Promise<string>;
  openExternalUrl(url: string): Promise<void>;
  requestPermission(kind: PermissionKind): Promise<PermissionStatus>;
  speechStart(): Promise<void>;
  speechStop(): Promise<void>;
  synthesisSpeak(text: string): Promise<void>;
  synthesisStop(): Promise<void>;
  hapticPerform(kind: HapticKind): Promise<void>;
  getContentSize(): Promise<ContentSizeCategory>;
  onLifecycle(handler: (state: LifecycleState) => void): () => void;
  voicePermissions(): Promise<boolean>;
  voiceStart(locale: string): Promise<VoiceReady>;
  voiceStop(voiceSessionId: string): Promise<void>;
  voiceSpeak(
    voiceSessionId: string,
    chunkId: string,
    text: string,
  ): Promise<VoiceQueued>;
  voiceStopSpeaking(voiceSessionId: string): Promise<void>;
  voiceSetRate(voiceSessionId: string, rate: number): Promise<number>;
  onVoiceEvent(handler: VoiceEventHandler): () => void;
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

export interface VoiceReady {
  readonly voiceSessionId: string;
}

export interface VoiceQueued {
  readonly chunkId: string;
}

export type VoiceEventHandler = (event: VoiceBridgeEvent) => void;

export type VoiceBridgeEvent = Extract<
  NativeEvent,
  {
    readonly type:
      | "voice.level"
      | "voice.partial"
      | "voice.final"
      | "voice.speechStarted"
      | "voice.speechFinished"
      | "voice.bargeIn"
      | "voice.interrupted"
      | "voice.error";
  }
>;

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
  openExternalUrl(url: string): Promise<void>;
  subscribe(
    type: "lifecycle.changed",
    handler: (event: { state: LifecycleState }) => void,
  ): () => void;
  subscribe(
    type: "voice.event",
    handler: (event: VoiceBridgeEvent) => void,
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

    async clipboardPaste() {
      const res = unwrap(
        await transport.send({ version: 1, type: "clipboard.paste" }),
      );
      if (res.type !== "clipboard.pasted") {
        throw new NativeBridgeError({
          id: "client",
          kind: "internal",
          message: "unexpected response",
        });
      }
      return res.text ?? "";
    },

    async openExternalUrl(rawUrl) {
      try {
        const parsed = new URL(rawUrl);
        if (parsed.protocol !== "http:" && parsed.protocol !== "https:") {
          throw new Error("unsupported protocol");
        }
        await transport.openExternalUrl(parsed.href);
      } catch {
        throw new Error("External link unavailable");
      }
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

    async voicePermissions() {
      const res = unwrap(
        await transport.send({ version: 1, type: "voice.permissions" }),
      );
      if (res.type !== "voice.permissions") {
        throw new NativeBridgeError({
          id: "client",
          kind: "internal",
          message: "unexpected response",
        });
      }
      return res.granted;
    },

    async voiceStart(locale) {
      const res = unwrap(
        await transport.send({ version: 1, type: "voice.start", locale }),
      );
      if (res.type !== "voice.ready") {
        throw new NativeBridgeError({
          id: "client",
          kind: "internal",
          message: "unexpected response",
        });
      }
      return { voiceSessionId: res.voiceSessionId };
    },

    async voiceStop(voiceSessionId) {
      unwrap(
        await transport.send({
          version: 1,
          type: "voice.stop",
          voiceSessionId,
        }),
      );
    },

    async voiceSpeak(voiceSessionId, chunkId, text) {
      const res = unwrap(
        await transport.send({
          version: 1,
          type: "voice.speak",
          voiceSessionId,
          chunkId,
          text,
        }),
      );
      if (res.type !== "voice.queued") {
        throw new NativeBridgeError({
          id: "client",
          kind: "internal",
          message: "unexpected response",
        });
      }
      return { chunkId: res.chunkId };
    },

    async voiceStopSpeaking(voiceSessionId) {
      unwrap(
        await transport.send({
          version: 1,
          type: "voice.stopSpeaking",
          voiceSessionId,
        }),
      );
    },

    async voiceSetRate(voiceSessionId, rate) {
      const res = unwrap(
        await transport.send({
          version: 1,
          type: "voice.setRate",
          voiceSessionId,
          rate,
        }),
      );
      if (res.type !== "voice.rateSet") {
        throw new NativeBridgeError({
          id: "client",
          kind: "internal",
          message: "unexpected response",
        });
      }
      return res.rate;
    },

    onVoiceEvent(handler) {
      return transport.subscribe("voice.event", handler);
    },
  };
}

// Re-export decode for consumers that validate raw plugin output
export { decodeNativeResponse };
