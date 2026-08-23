/**
 * Native bridge contract version 1.
 *
 * Defines discriminated unions for commands, responses, and events exchanged
 * between the React renderer and the native (Rust/Swift) plugin layer.
 * No response, error, debug, fixture, or event carries a saved token.
 * Commands may carry a capability as input (secure.set); responses never echo it.
 */

export const NATIVE_BRIDGE_VERSION = 1 as const;
export type BridgeVersion = typeof NATIVE_BRIDGE_VERSION;

// ---------------------------------------------------------------------------
// Enum-like string unions
// ---------------------------------------------------------------------------

export type HapticKind =
  | "selection"
  | "impactLight"
  | "impactMedium"
  | "impactHeavy"
  | "notificationSuccess"
  | "notificationWarning"
  | "notificationError";

export type ContentSizeCategory =
  | "small"
  | "medium"
  | "large"
  | "extraLarge"
  | "extraExtraLarge"
  | "extraExtraExtraLarge"
  | "accessibilityMedium"
  | "accessibilityLarge"
  | "accessibilityExtraLarge"
  | "accessibilityExtraExtraLarge"
  | "accessibilityExtraExtraExtraLarge";

/** Canonical content-size category values — the single source of truth. */
export const CONTENT_SIZE_CATEGORIES: readonly ContentSizeCategory[] = [
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

/** Type guard: true if `value` is a valid `ContentSizeCategory`. */
export function isContentSizeCategory(
  value: unknown,
): value is ContentSizeCategory {
  return (
    typeof value === "string" &&
    (CONTENT_SIZE_CATEGORIES as readonly string[]).includes(value)
  );
}

export type LifecycleState =
  | "active"
  | "inactive"
  | "background"
  | "foreground";

export type PermissionKind =
  | "camera"
  | "microphone"
  | "speech"
  | "localNetwork";

export type NativeErrorKind =
  | "pairing_unavailable"
  | "internal"
  | "secure_store"
  | "scanner"
  | "permission_denied"
  | "unsupported";

// ---------------------------------------------------------------------------
// Error — redacted, no token fields
// ---------------------------------------------------------------------------

export interface NativeError {
  readonly id: string;
  readonly kind: NativeErrorKind;
  readonly message: string;
}

// ---------------------------------------------------------------------------
// Commands
// ---------------------------------------------------------------------------

export type NativeCommand =
  | {
      readonly version: 1;
      readonly type: "secure.get";
      readonly profileId: string;
    }
  | {
      readonly version: 1;
      readonly type: "secure.set";
      readonly profileId: string;
      readonly capability: string;
    }
  | {
      readonly version: 1;
      readonly type: "secure.delete";
      readonly profileId: string;
    }
  | { readonly version: 1; readonly type: "pairing.scanAndPreview" }
  | {
      readonly version: 1;
      readonly type: "permission.request";
      readonly kind: PermissionKind;
    }
  | { readonly version: 1; readonly type: "speech.start" }
  | { readonly version: 1; readonly type: "speech.stop" }
  | {
      readonly version: 1;
      readonly type: "synthesis.speak";
      readonly text: string;
    }
  | { readonly version: 1; readonly type: "synthesis.stop" }
  | {
      readonly version: 1;
      readonly type: "haptic.perform";
      readonly kind: HapticKind;
    }
  | {
      readonly version: 1;
      readonly type: "clipboard.paste";
    }
  | { readonly version: 1; readonly type: "contentSize.get" };

export type NativeCommandType = NativeCommand["type"];

// ---------------------------------------------------------------------------
// Responses
// ---------------------------------------------------------------------------

export type NativeResponse =
  | {
      readonly version: 1;
      readonly type: "secure.state";
      readonly present: boolean;
    }
  | {
      readonly version: 1;
      readonly type: "secure.updated";
      readonly stored: boolean;
    }
  | {
      readonly version: 1;
      readonly type: "secure.deleted";
      readonly deleted: boolean;
    }
  | {
      readonly version: 1;
      readonly type: "pairing.preview";
      readonly previewId: string;
      readonly origin: string;
    }
  | {
      readonly version: 1;
      readonly type: "permission.status";
      readonly kind: PermissionKind;
      readonly granted: boolean;
    }
  | { readonly version: 1; readonly type: "speech.ready" }
  | { readonly version: 1; readonly type: "speech.stopped" }
  | { readonly version: 1; readonly type: "synthesis.started" }
  | { readonly version: 1; readonly type: "synthesis.stopped" }
  | { readonly version: 1; readonly type: "haptic.completed" }
  | {
      readonly version: 1;
      readonly type: "clipboard.pasted";
      readonly text: string;
    }
  | {
      readonly version: 1;
      readonly type: "contentSize.value";
      readonly category: ContentSizeCategory;
    }
  | {
      readonly version: 1;
      readonly type: "error";
      readonly error: NativeError;
    };

export type NativeResponseType = NativeResponse["type"];

// ---------------------------------------------------------------------------
// Events
// ---------------------------------------------------------------------------

export type NativeEvent =
  | {
      readonly version: 1;
      readonly type: "lifecycle.changed";
      readonly state: LifecycleState;
    }
  | {
      readonly version: 1;
      readonly type: "speech.partial";
      readonly text: string;
    }
  | {
      readonly version: 1;
      readonly type: "speech.final";
      readonly text: string;
    }
  | { readonly version: 1; readonly type: "barge.in" };

export type NativeEventType = NativeEvent["type"];

// ---------------------------------------------------------------------------
// Internal validation helpers
// ---------------------------------------------------------------------------

function assertObject(
  value: unknown,
): asserts value is Record<string, unknown> {
  if (typeof value !== "object" || value === null || Array.isArray(value)) {
    throw new Error("Bridge message must be an object");
  }
}

function assertVersion1(obj: Record<string, unknown>): void {
  if (obj.version !== 1) {
    throw new Error(`Unsupported bridge version: ${String(obj.version)}`);
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

function assertRequiredFields(
  obj: Record<string, unknown>,
  fields: readonly string[],
  label: string,
): void {
  for (const f of fields) {
    if (!(f in obj)) {
      throw new Error(`Missing field "${f}" in ${label}`);
    }
  }
}

function assertString(obj: Record<string, unknown>, field: string): void {
  if (typeof obj[field] !== "string") {
    throw new Error(`Field "${field}" must be a string`);
  }
}

function assertBoolean(obj: Record<string, unknown>, field: string): void {
  if (typeof obj[field] !== "boolean") {
    throw new Error(`Field "${field}" must be a boolean`);
  }
}

function assertErrorObject(obj: Record<string, unknown>, field: string): void {
  const err = obj[field];
  if (typeof err !== "object" || err === null || Array.isArray(err)) {
    throw new Error(`Field "${field}" must be an object`);
  }
  const allowed = ["id", "kind", "message"];
  for (const key of Object.keys(err)) {
    if (!allowed.includes(key)) {
      throw new Error(`Unknown field "${key}" in ${field}`);
    }
  }
  for (const f of allowed) {
    if (!(f in err)) {
      throw new Error(`Missing field "${f}" in ${field}`);
    }
  }
  for (const f of allowed) {
    if (typeof (err as Record<string, unknown>)[f] !== "string") {
      throw new Error(`Field "${f}" in ${field} must be a string`);
    }
  }
  const kind = (err as Record<string, unknown>).kind;
  const validKinds: readonly string[] = [
    "pairing_unavailable",
    "internal",
    "secure_store",
    "scanner",
    "permission_denied",
    "unsupported",
  ];
  if (typeof kind === "string" && !validKinds.includes(kind)) {
    throw new Error(`Unknown error kind: ${kind}`);
  }
}

// ---------------------------------------------------------------------------
// Decoders — fail closed on unknown version, type, or field
// ---------------------------------------------------------------------------

export function decodeNativeCommand(obj: unknown): NativeCommand {
  assertObject(obj);
  assertVersion1(obj);
  switch (obj.type) {
    case "secure.get":
      assertNoExtraFields(obj, ["version", "type", "profileId"], "command");
      assertRequiredFields(obj, ["profileId"], "command");
      assertString(obj, "profileId");
      return obj as NativeCommand;
    case "secure.set":
      assertNoExtraFields(
        obj,
        ["version", "type", "profileId", "capability"],
        "command",
      );
      assertRequiredFields(obj, ["profileId", "capability"], "command");
      assertString(obj, "profileId");
      assertString(obj, "capability");
      return obj as NativeCommand;
    case "secure.delete":
      assertNoExtraFields(obj, ["version", "type", "profileId"], "command");
      assertRequiredFields(obj, ["profileId"], "command");
      assertString(obj, "profileId");
      return obj as NativeCommand;
    case "pairing.scanAndPreview":
      assertNoExtraFields(obj, ["version", "type"], "command");
      return obj as NativeCommand;
    case "permission.request":
      assertNoExtraFields(obj, ["version", "type", "kind"], "command");
      assertRequiredFields(obj, ["kind"], "command");
      assertString(obj, "kind");
      return obj as NativeCommand;
    case "speech.start":
      assertNoExtraFields(obj, ["version", "type"], "command");
      return obj as NativeCommand;
    case "speech.stop":
      assertNoExtraFields(obj, ["version", "type"], "command");
      return obj as NativeCommand;
    case "synthesis.speak":
      assertNoExtraFields(obj, ["version", "type", "text"], "command");
      assertRequiredFields(obj, ["text"], "command");
      assertString(obj, "text");
      return obj as NativeCommand;
    case "synthesis.stop":
      assertNoExtraFields(obj, ["version", "type"], "command");
      return obj as NativeCommand;
    case "haptic.perform":
      assertNoExtraFields(obj, ["version", "type", "kind"], "command");
      assertRequiredFields(obj, ["kind"], "command");
      assertString(obj, "kind");
      return obj as NativeCommand;
    case "contentSize.get":
      assertNoExtraFields(obj, ["version", "type"], "command");
      return obj as NativeCommand;
    default:
      throw new Error(`Unknown command type: ${String(obj.type)}`);
  }
}

export function decodeNativeResponse(obj: unknown): NativeResponse {
  assertObject(obj);
  assertVersion1(obj);
  switch (obj.type) {
    case "secure.state":
      assertNoExtraFields(obj, ["version", "type", "present"], "response");
      assertRequiredFields(obj, ["present"], "response");
      assertBoolean(obj, "present");
      return obj as NativeResponse;
    case "secure.updated":
      assertNoExtraFields(obj, ["version", "type", "stored"], "response");
      assertRequiredFields(obj, ["stored"], "response");
      assertBoolean(obj, "stored");
      return obj as NativeResponse;
    case "secure.deleted":
      assertNoExtraFields(obj, ["version", "type", "deleted"], "response");
      assertRequiredFields(obj, ["deleted"], "response");
      assertBoolean(obj, "deleted");
      return obj as NativeResponse;
    case "pairing.preview":
      assertNoExtraFields(
        obj,
        ["version", "type", "previewId", "origin"],
        "response",
      );
      assertRequiredFields(obj, ["previewId", "origin"], "response");
      assertString(obj, "previewId");
      assertString(obj, "origin");
      return obj as NativeResponse;
    case "permission.status":
      assertNoExtraFields(
        obj,
        ["version", "type", "kind", "granted"],
        "response",
      );
      assertRequiredFields(obj, ["kind", "granted"], "response");
      assertString(obj, "kind");
      assertBoolean(obj, "granted");
      return obj as NativeResponse;
    case "speech.ready":
      assertNoExtraFields(obj, ["version", "type"], "response");
      return obj as NativeResponse;
    case "speech.stopped":
      assertNoExtraFields(obj, ["version", "type"], "response");
      return obj as NativeResponse;
    case "synthesis.started":
      assertNoExtraFields(obj, ["version", "type"], "response");
      return obj as NativeResponse;
    case "synthesis.stopped":
      assertNoExtraFields(obj, ["version", "type"], "response");
      return obj as NativeResponse;
    case "haptic.completed":
      assertNoExtraFields(obj, ["version", "type"], "response");
      return obj as NativeResponse;
    case "contentSize.value":
      assertNoExtraFields(obj, ["version", "type", "category"], "response");
      assertRequiredFields(obj, ["category"], "response");
      assertString(obj, "category");
      if (!isContentSizeCategory(obj.category)) {
        throw new Error(`Unknown content size category: ${obj.category}`);
      }
      return obj as NativeResponse;
    case "error":
      assertNoExtraFields(obj, ["version", "type", "error"], "response");
      assertRequiredFields(obj, ["error"], "response");
      assertErrorObject(obj, "error");
      return obj as NativeResponse;
    default:
      throw new Error(`Unknown response type: ${String(obj.type)}`);
  }
}

export function decodeNativeEvent(obj: unknown): NativeEvent {
  assertObject(obj);
  assertVersion1(obj);
  switch (obj.type) {
    case "lifecycle.changed":
      assertNoExtraFields(obj, ["version", "type", "state"], "event");
      assertRequiredFields(obj, ["state"], "event");
      assertString(obj, "state");
      return obj as NativeEvent;
    case "speech.partial":
      assertNoExtraFields(obj, ["version", "type", "text"], "event");
      assertRequiredFields(obj, ["text"], "event");
      assertString(obj, "text");
      return obj as NativeEvent;
    case "speech.final":
      assertNoExtraFields(obj, ["version", "type", "text"], "event");
      assertRequiredFields(obj, ["text"], "event");
      assertString(obj, "text");
      return obj as NativeEvent;
    case "barge.in":
      assertNoExtraFields(obj, ["version", "type"], "event");
      return obj as NativeEvent;
    default:
      throw new Error(`Unknown event type: ${String(obj.type)}`);
  }
}
