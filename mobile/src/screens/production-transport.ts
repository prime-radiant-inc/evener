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
  ContentSizeCategory,
  LifecycleState,
  NativeCommand,
  NativeResponse,
} from "../native/contract";
import { decodeNativeResponse } from "../native/contract";
import type { TauriBridge } from "../services/tauri";

const PLUGIN = "plugin:evener-native|";

const CONTENT_SIZE_CATEGORIES: readonly ContentSizeCategory[] = [
  "small",
  "medium",
  "large",
  "extraLarge",
  "extraExtraLarge",
  "extraExtraExtraLarge",
  "accessibilityMedium",
  "accessibilityLarge",
  "accessibilityExtraLarge",
  "accessibilityExtraExtraLarge",
  "accessibilityExtraExtraExtraLarge",
];

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
      if (type !== "lifecycle.changed") {
        return () => {};
      }
      return subscribeLifecycle(bridge, handler);
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

function isContentSizeCategory(value: string): value is ContentSizeCategory {
  return (CONTENT_SIZE_CATEGORIES as readonly string[]).includes(value);
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
