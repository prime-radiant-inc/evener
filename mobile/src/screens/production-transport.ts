/**
 * Production native transport adapter — maps the typed {@link NativeCommand}
 * union to real Tauri plugin routes and decodes the raw Rust response shapes
 * into typed {@link NativeResponse} values.
 *
 * Tauri plugin commands are invoked via namespaced routes:
 * `plugin:evener-native|<snake_case_command>` with a `{payload: ...}` envelope.
 * This module is the only place that knows the route/envelope format; tests
 * inject a fake {@link TauriBridge} to assert exact routes without a Tauri
 * runtime.
 *
 * Raw response shapes are pinned to the actual Rust serde output:
 * - `scan_and_preview_pairing` → versioned `ScanAndPreviewResponse`
 *   `{version, type, previewId?, origin?, error?}` — decoded by
 *   {@link decodeNativeResponse}.
 * - `haptic_perform` → `HapticPerformResponse` `{completed: boolean}` — NOT
 *   versioned. Decoded by a strict command-specific mapper.
 * - `content_size_get` → `ContentSizeGetResponse` `{category: string}` — NOT
 *   versioned. Decoded by a strict command-specific mapper that validates the
 *   category against the `ContentSizeCategory` union.
 *
 * Lifecycle events use Tauri's `listen` API (from `@tauri-apps/api/event`,
 * imported through the sole seam `services/tauri.ts`). The adapter maps
 * `tauri://suspended`→background and `tauri://resumed`→foreground, returns
 * race-safe/idempotent unsubscribe, and never delivers after unsubscribe.
 */
import type { NativeTransport } from "../native/client";
import type {
  LifecycleState,
  NativeCommand,
  NativeResponse,
} from "../native/contract";
import {
  decodeNativeEvent,
  decodeNativeResponse,
  isContentSizeCategory,
} from "../native/contract";
import type { TauriBridge } from "../services/tauri";

const PLUGIN = "plugin:evener-native|";

/**
 * Create the production native transport backed by a real {@link TauriBridge}.
 * Exported so tests can inject a fake bridge and assert exact routes/envelopes.
 */
export function createTauriNativeTransport(
  bridge: TauriBridge,
): NativeTransport {
  return {
    async send(command: NativeCommand): Promise<NativeResponse> {
      const route = `${PLUGIN}${commandToRoute(command)}`;
      const payload = commandToPayload(command);
      const raw = await bridge.invoke<unknown>(route, { payload });
      return decodeRawResponse(command, raw);
    },

    subscribe(type, handler) {
      if (type === "lifecycle.changed") {
        return subscribeLifecycle(
          bridge,
          handler as (event: { state: LifecycleState }) => void,
        );
      }
      if (type === "voice.event") {
        return subscribeVoiceEvents(
          bridge,
          handler as (event: import("../native/contract").NativeEvent) => void,
        );
      }
      return () => {};
    },
  };
}

// ---------------------------------------------------------------------------
// Command → route and payload mapping
// ---------------------------------------------------------------------------

function commandToRoute(command: NativeCommand): string {
  switch (command.type) {
    case "pairing.scanAndPreview":
      return "scan_and_preview_pairing";
    case "haptic.perform":
      return "haptic_perform";
    case "contentSize.get":
      return "content_size_get";
    case "clipboard.paste":
      return "clipboard_paste";
    case "voice.permissions":
      return "voice_permissions";
    case "voice.start":
      return "voice_start";
    case "voice.stop":
      return "voice_stop";
    case "voice.speak":
      return "voice_speak";
    case "voice.stopSpeaking":
      return "voice_stop_speaking";
    case "voice.setRate":
      return "voice_set_rate";
    default:
      // secure.*, speech.*, synthesis.*, permission.* are NOT exposed to JS.
      throw new Error(`unsupported native command: ${command.type}`);
  }
}

function commandToPayload(command: NativeCommand): Record<string, unknown> {
  switch (command.type) {
    case "pairing.scanAndPreview":
      return {};
    case "haptic.perform":
      return { kind: command.kind };
    case "contentSize.get":
      return {};
    case "clipboard.paste":
      return {};
    case "voice.permissions":
      return {};
    case "voice.start":
      return { locale: command.locale };
    case "voice.stop":
      return { voiceSessionId: command.voiceSessionId };
    case "voice.speak":
      return {
        voiceSessionId: command.voiceSessionId,
        chunkId: command.chunkId,
        text: command.text,
      };
    case "voice.stopSpeaking":
      return { voiceSessionId: command.voiceSessionId };
    case "voice.setRate":
      return { voiceSessionId: command.voiceSessionId, rate: command.rate };
    default:
      throw new Error(`unsupported native command: ${command.type}`);
  }
}

// ---------------------------------------------------------------------------
// Command-specific raw response decoders
// ---------------------------------------------------------------------------

/**
 * Decode the raw Rust response into a typed {@link NativeResponse}. Each
 * command returns a different raw shape — the decoder is selected by command
 * type so it can validate the exact fields the Rust serde struct produces.
 */
function decodeRawResponse(
  command: NativeCommand,
  raw: unknown,
): NativeResponse {
  switch (command.type) {
    case "pairing.scanAndPreview":
      // ScanAndPreviewResponse is versioned — use the shared contract decoder.
      return decodeNativeResponse(raw);
    case "haptic.perform":
      return decodeHapticResponse(raw);
    case "contentSize.get":
      return decodeContentSizeResponse(raw);
    case "clipboard.paste":
      return decodeClipboardPasteResponse(raw);
    case "voice.permissions":
      return decodeVoicePermissionsResponse(raw);
    case "voice.start":
      return decodeVoiceReadyResponse(raw);
    case "voice.stop":
      return { version: 1, type: "voice.stopped" };
    case "voice.speak":
      return decodeVoiceQueuedResponse(raw);
    case "voice.stopSpeaking":
      return { version: 1, type: "voice.speakingStopped" };
    case "voice.setRate":
      return decodeVoiceRateSetResponse(raw);
    default:
      throw new Error(`unsupported native command: ${command.type}`);
  }
}

/**
 * Decode the raw `HapticPerformResponse` `{completed: boolean}` into a typed
 * `haptic.completed` response. Strict: rejects missing/wrong-typed/extra
 * fields and `completed:false` (haptic did not perform).
 */
function decodeHapticResponse(raw: unknown): NativeResponse {
  assertRawObject(raw, "haptic response");
  const obj = raw as Record<string, unknown>;
  assertNoExtraFields(obj, ["completed"], "haptic response");
  if (typeof obj.completed !== "boolean") {
    throw new Error('Field "completed" must be a boolean in haptic response');
  }
  if (!obj.completed) {
    throw new Error("haptic perform did not complete");
  }
  return { version: 1, type: "haptic.completed" };
}

/**
 * Decode the raw `ContentSizeGetResponse` `{category: string}` into a typed
 * `contentSize.value` response. Strict: rejects missing/wrong-typed/extra
 * fields and categories outside the `ContentSizeCategory` union.
 */
function decodeContentSizeResponse(raw: unknown): NativeResponse {
  assertRawObject(raw, "content-size response");
  const obj = raw as Record<string, unknown>;
  assertNoExtraFields(obj, ["category"], "content-size response");
  if (typeof obj.category !== "string") {
    throw new Error(
      'Field "category" must be a string in content-size response',
    );
  }
  if (!isContentSizeCategory(obj.category)) {
    throw new Error(`Unknown content size category: ${obj.category}`);
  }
  return {
    version: 1,
    type: "contentSize.value",
    category: obj.category,
  };
}

/**
 * Decode the raw `ClipboardPasteResponse` `{text: string}` into a typed
 * `clipboard.pasted` response. Strict: rejects missing/wrong-typed/extra
 * fields. The text may be an empty string (clipboard empty).
 */
function decodeClipboardPasteResponse(raw: unknown): NativeResponse {
  assertRawObject(raw, "clipboard paste response");
  const obj = raw as Record<string, unknown>;
  assertNoExtraFields(obj, ["text"], "clipboard paste response");
  if (typeof obj.text !== "string") {
    throw new Error(
      'Field "text" must be a string in clipboard paste response',
    );
  }
  return {
    version: 1,
    type: "clipboard.pasted",
    text: obj.text,
  };
}

/**
 * Decode the raw voice permissions response `{granted: boolean}`.
 */
function decodeVoicePermissionsResponse(raw: unknown): NativeResponse {
  assertRawObject(raw, "voice permissions response");
  const obj = raw as Record<string, unknown>;
  assertNoExtraFields(obj, ["granted"], "voice permissions response");
  if (typeof obj.granted !== "boolean") {
    throw new Error(
      'Field "granted" must be a boolean in voice permissions response',
    );
  }
  return { version: 1, type: "voice.permissions", granted: obj.granted };
}

/**
 * Decode the raw voice ready response `{voiceSessionId: string}`.
 */
function decodeVoiceReadyResponse(raw: unknown): NativeResponse {
  assertRawObject(raw, "voice ready response");
  const obj = raw as Record<string, unknown>;
  assertNoExtraFields(obj, ["voiceSessionId"], "voice ready response");
  if (typeof obj.voiceSessionId !== "string") {
    throw new Error(
      'Field "voiceSessionId" must be a string in voice ready response',
    );
  }
  return {
    version: 1,
    type: "voice.ready",
    voiceSessionId: obj.voiceSessionId,
  };
}

/**
 * Decode the raw voice queued response `{chunkId: string}`.
 */
function decodeVoiceQueuedResponse(raw: unknown): NativeResponse {
  assertRawObject(raw, "voice queued response");
  const obj = raw as Record<string, unknown>;
  assertNoExtraFields(obj, ["chunkId"], "voice queued response");
  if (typeof obj.chunkId !== "string") {
    throw new Error(
      'Field "chunkId" must be a string in voice queued response',
    );
  }
  return { version: 1, type: "voice.queued", chunkId: obj.chunkId };
}

/**
 * Decode the raw voice rate-set response `{rate: number}`.
 */
function decodeVoiceRateSetResponse(raw: unknown): NativeResponse {
  assertRawObject(raw, "voice rate-set response");
  const obj = raw as Record<string, unknown>;
  assertNoExtraFields(obj, ["rate"], "voice rate-set response");
  if (typeof obj.rate !== "number" || !Number.isFinite(obj.rate)) {
    throw new Error(
      'Field "rate" must be a finite number in voice rate-set response',
    );
  }
  return { version: 1, type: "voice.rateSet", rate: obj.rate };
}

// ---------------------------------------------------------------------------
// Raw object validation helpers (shared with contract decoders)
// ---------------------------------------------------------------------------

function assertRawObject(value: unknown, label: string): void {
  if (typeof value !== "object" || value === null || Array.isArray(value)) {
    throw new Error(`${label} must be an object`);
  }
}

function assertNoExtraFields(
  obj: Record<string, unknown>,
  allowed: readonly string[],
  label: string,
): void {
  for (const key of Object.keys(obj)) {
    if (!allowed.includes(key)) {
      throw new Error(`Unknown field "${key}" in ${label}`);
    }
  }
}

// ---------------------------------------------------------------------------
// Lifecycle subscription — race-safe, idempotent unsubscribe
// ---------------------------------------------------------------------------

function subscribeLifecycle(
  bridge: TauriBridge,
  handler: (event: { state: LifecycleState }) => void,
): () => void {
  let unsubscribed = false;
  const unlistenFns: Array<() => void> = [];

  const onEvent = (event: { event: string }) => {
    if (unsubscribed) return;
    const state = mapLifecycleEvent(event.event);
    if (state !== null) {
      handler({ state });
    }
  };

  // listen returns a Promise<UnlistenFn>. We store the unlisten functions as
  // they resolve. If unsubscribe is called before the promises resolve,
  // `unsubscribed` prevents further deliveries and the unlisten functions
  // are called when they arrive.
  void bridge
    .listen("tauri://suspended", onEvent)
    .then((fn) => {
      if (unsubscribed) {
        fn();
      } else {
        unlistenFns.push(fn);
      }
    })
    .catch(() => {});

  void bridge
    .listen("tauri://resumed", onEvent)
    .then((fn) => {
      if (unsubscribed) {
        fn();
      } else {
        unlistenFns.push(fn);
      }
    })
    .catch(() => {});

  return () => {
    if (unsubscribed) return;
    unsubscribed = true;
    for (const fn of unlistenFns) {
      fn();
    }
    unlistenFns.length = 0;
  };
}

function mapLifecycleEvent(eventName: string): LifecycleState | null {
  if (eventName === "tauri://suspended") return "background";
  if (eventName === "tauri://resumed") return "foreground";
  return null;
}

// ---------------------------------------------------------------------------
// Voice event subscription — Tauri event → typed NativeEvent
// ---------------------------------------------------------------------------

function subscribeVoiceEvents(
  bridge: TauriBridge,
  handler: (event: import("../native/contract").NativeEvent) => void,
): () => void {
  let unsubscribed = false;
  const unlistenFns: Array<() => void> = [];

  const onEvent = (payload: { event: string; payload: unknown }) => {
    if (unsubscribed) return;
    try {
      const decoded = decodeNativeEvent(payload.payload);
      handler(decoded);
    } catch {
      // Ignore malformed voice events rather than crashing the subscription.
    }
  };

  void bridge
    .listen("voice://event", onEvent as never)
    .then((fn) => {
      if (unsubscribed) {
        fn();
      } else {
        unlistenFns.push(fn);
      }
    })
    .catch(() => {});

  return () => {
    if (unsubscribed) return;
    unsubscribed = true;
    for (const fn of unlistenFns) {
      fn();
    }
    unlistenFns.length = 0;
  };
}
