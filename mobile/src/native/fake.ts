/**
 * Fake native bridge for tests and the browser fixture mode.
 *
 * Implements the {@link NativeTransport} contract so stores and services can be
 * exercised without a real Tauri plugin. No saved token ever appears in any
 * response, error, or recorded state.
 */

import type { NativeTransport, VoiceBridgeEvent } from "./client";
import type {
  ContentSizeCategory,
  LifecycleState,
  NativeCommand,
  NativeResponse,
  PermissionKind,
} from "./contract";
import { NATIVE_BRIDGE_VERSION } from "./contract";

export interface FakeNativeBridgeOptions {
  readonly contentSize?: ContentSizeCategory;
  readonly permissions?: Partial<Record<PermissionKind, boolean>>;
  /**
   * When set, `pairing.scanAndPreview` returns a redacted preview
   * `{previewId, origin}` instead of `pairing_unavailable`. Never carries a
   * token — the real bridge returns only an opaque preview id + origin.
   */
  readonly scanPreview?: {
    readonly previewId: string;
    readonly origin: string;
  };
}

export class FakeNativeBridge implements NativeTransport {
  readonly version = NATIVE_BRIDGE_VERSION;
  private contentSize: ContentSizeCategory;
  private readonly permissions: Partial<Record<PermissionKind, boolean>>;
  private scanPreview: {
    readonly previewId: string;
    readonly origin: string;
  } | null;
  private readonly secureStore = new Map<string, string>();
  private readonly lifecycleHandlers = new Set<
    (e: { state: LifecycleState }) => void
  >();
  private voiceSessionId: string | null = null;
  voiceRate: number | null = null;
  private readonly voiceEventHandlers = new Set<
    (event: VoiceBridgeEvent) => void
  >();
  readonly commands: NativeCommand[] = [];
  readonly externalUrls: string[] = [];

  constructor(options: FakeNativeBridgeOptions = {}) {
    this.contentSize = options.contentSize ?? "large";
    this.permissions = options.permissions ?? {};
    this.scanPreview = options.scanPreview ?? null;
  }

  // -- NativeTransport ------------------------------------------------------

  async send(command: NativeCommand): Promise<NativeResponse> {
    this.commands.push(command);
    return this.handle(command);
  }

  async openExternalUrl(url: string): Promise<void> {
    this.externalUrls.push(url);
  }

  subscribe(
    type: "lifecycle.changed",
    handler: (e: { state: LifecycleState }) => void,
  ): () => void;
  subscribe(
    type: "voice.event",
    handler: (event: VoiceBridgeEvent) => void,
  ): () => void;
  subscribe(
    type: "lifecycle.changed" | "voice.event",
    handler:
      | ((e: { state: LifecycleState }) => void)
      | ((event: VoiceBridgeEvent) => void),
  ): () => void {
    if (type === "lifecycle.changed") {
      this.lifecycleHandlers.add(
        handler as (e: { state: LifecycleState }) => void,
      );
      return () => {
        this.lifecycleHandlers.delete(
          handler as (e: { state: LifecycleState }) => void,
        );
      };
    }
    this.voiceEventHandlers.add(handler as (event: VoiceBridgeEvent) => void);
    return () => {
      this.voiceEventHandlers.delete(
        handler as (event: VoiceBridgeEvent) => void,
      );
    };
  }

  // -- Test helpers --------------------------------------------------------

  emitLifecycle(state: LifecycleState): void {
    for (const h of this.lifecycleHandlers) {
      h({ state });
    }
  }

  emitVoiceEvent(event: VoiceBridgeEvent): void {
    for (const h of this.voiceEventHandlers) {
      h(event);
    }
  }

  setPermission(kind: PermissionKind, granted: boolean): void {
    this.permissions[kind] = granted;
  }

  setContentSize(category: ContentSizeCategory): void {
    this.contentSize = category;
  }

  /**
   * Configure the scan+preview result. Pass null to restore the default
   * pairing_unavailable behavior. The preview carries only an opaque id and
   * origin — never a token or raw QR text.
   */
  setScanPreview(
    preview: { readonly previewId: string; readonly origin: string } | null,
  ): void {
    this.scanPreview = preview;
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
        // When a scan preview is configured, return a redacted preview (opaque
        // id + origin only — never a token or raw QR text). Otherwise mirror
        // production: a structured pairing_unavailable error.
        if (this.scanPreview) {
          return {
            version: 1,
            type: "pairing.preview",
            previewId: this.scanPreview.previewId,
            origin: this.scanPreview.origin,
          };
        }
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
      case "clipboard.paste":
        return {
          version: 1,
          type: "clipboard.pasted",
          text: "",
        };
      case "contentSize.get":
        return {
          version: 1,
          type: "contentSize.value",
          category: this.contentSize,
        };
      case "voice.permissions":
        return { version: 1, type: "voice.permissions", granted: true };
      case "voice.start":
        this.voiceSessionId = `voice-session-${Date.now()}`;
        return {
          version: 1,
          type: "voice.ready",
          voiceSessionId: this.voiceSessionId,
        };
      case "voice.stop":
        this.voiceSessionId = null;
        return { version: 1, type: "voice.stopped" };
      case "voice.speak":
        return {
          version: 1,
          type: "voice.queued",
          chunkId: command.chunkId,
        };
      case "voice.stopSpeaking":
        return { version: 1, type: "voice.speakingStopped" };
      case "voice.setRate":
        this.voiceRate = command.rate;
        return { version: 1, type: "voice.rateSet", rate: command.rate };
      default: {
        // Exhaustiveness check
        const _exhaustive: never = command;
        return _exhaustive;
      }
    }
  }
}
