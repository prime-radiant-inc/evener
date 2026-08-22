/**
 * Profile lifecycle service — typed adapter over the Rust Tauri commands in
 * `mobile/src-tauri/src/commands.rs`.
 *
 * JavaScript receives only redacted `{id,name,origin}` summaries, opaque
 * preview IDs, and generations. No capability, token, raw QR text, or native
 * path ever crosses this surface: the Rust layer holds those and returns only
 * the redacted DTOs decoded here. This module decodes strictly — rejecting any
 * extra field (capability/token/rawQr/…) — so a backend that ever leaked one
 * fails closed instead of silently passing it through.
 */

import type { TauriBridge } from "./tauri";

// ---------------------------------------------------------------------------
// Redacted response shapes (mirror the Rust serde camelCase DTOs)
// ---------------------------------------------------------------------------

/** A redacted profile summary: exactly `{id,name,origin}`. */
export interface ProfileRedacted {
  readonly id: string;
  readonly name: string;
  readonly origin: string;
}

/** A preview of a parsed pairing payload: opaque preview ID + normalized origin. */
export interface ProfilePreview {
  readonly previewId: string;
  readonly origin: string;
}

/** Result of a select/remove: the now-active profile (or null) and generation. */
export interface SelectResult {
  readonly profileId: string | null;
  readonly generation: number;
}

/** Health snapshot: active profile, all profiles, and the store generation. */
export interface HealthSnapshot {
  readonly activeProfileId: string | null;
  readonly profiles: readonly ProfileRedacted[];
  readonly generation: number;
}

// ---------------------------------------------------------------------------
// Inputs (camelCase, matching the Rust serde `rename_all = "camelCase"`)
// ---------------------------------------------------------------------------

export interface PreviewPasteInput {
  readonly raw: string;
}

export interface PreviewRepairInput {
  readonly profileId: string;
  readonly raw: string;
}

export interface ConfirmPairingInput {
  readonly previewId: string;
  readonly name: string;
  readonly allowDuplicateOrigin?: boolean;
}

export interface RenameInput {
  readonly profileId: string;
  readonly newName: string;
}

export interface ProfileIdInput {
  readonly profileId: string;
}

// ---------------------------------------------------------------------------
// Structured error — never carries a secret
// ---------------------------------------------------------------------------

export type ProfileServiceErrorCode =
  | "decode_failed"
  | "preview_failed"
  | "command_failed";

export class ProfileServiceError extends Error {
  readonly code: ProfileServiceErrorCode;
  constructor(
    code: ProfileServiceErrorCode,
    message: string,
    options?: { cause?: unknown },
  ) {
    super(message, options);
    this.name = "ProfileServiceError";
    this.code = code;
  }
}

export function isProfileServiceError(
  value: unknown,
): value is ProfileServiceError {
  return value instanceof ProfileServiceError;
}

// ---------------------------------------------------------------------------
// Strict decoders — reject unknown fields so a leaked secret fails closed
// ---------------------------------------------------------------------------

function assertObject(
  value: unknown,
): asserts value is Record<string, unknown> {
  if (typeof value !== "object" || value === null || Array.isArray(value)) {
    throw new ProfileServiceError("decode_failed", "expected an object");
  }
}

function assertStringField(
  obj: Record<string, unknown>,
  field: string,
): string {
  if (typeof obj[field] !== "string") {
    throw new ProfileServiceError(
      "decode_failed",
      `field "${field}" must be a string`,
    );
  }
  return obj[field] as string;
}

function assertNoExtraFields(
  obj: Record<string, unknown>,
  allowed: readonly string[],
): void {
  for (const key of Object.keys(obj)) {
    if (!allowed.includes(key)) {
      throw new ProfileServiceError(
        "decode_failed",
        `unknown field "${key}" in profile response`,
      );
    }
  }
}

function decodeProfile(value: unknown): ProfileRedacted {
  assertObject(value);
  const obj = value as Record<string, unknown>;
  assertNoExtraFields(obj, ["id", "name", "origin"]);
  return {
    id: assertStringField(obj, "id"),
    name: assertStringField(obj, "name"),
    origin: assertStringField(obj, "origin"),
  };
}

function decodeProfileList(value: unknown): ProfileRedacted[] {
  if (!Array.isArray(value)) {
    throw new ProfileServiceError("decode_failed", "expected an array");
  }
  return value.map(decodeProfile);
}

function decodePreview(value: unknown): ProfilePreview {
  assertObject(value);
  const obj = value as Record<string, unknown>;
  assertNoExtraFields(obj, ["previewId", "origin"]);
  return {
    previewId: assertStringField(obj, "previewId"),
    origin: assertStringField(obj, "origin"),
  };
}

function decodeSelectResult(value: unknown): SelectResult {
  assertObject(value);
  const obj = value as Record<string, unknown>;
  assertNoExtraFields(obj, ["profileId", "generation"]);
  const profileIdRaw = obj.profileId;
  if (profileIdRaw !== null && typeof profileIdRaw !== "string") {
    throw new ProfileServiceError(
      "decode_failed",
      'field "profileId" must be a string or null',
    );
  }
  if (typeof obj.generation !== "number" || !Number.isFinite(obj.generation)) {
    throw new ProfileServiceError(
      "decode_failed",
      'field "generation" must be a number',
    );
  }
  return {
    profileId: profileIdRaw as string | null,
    generation: obj.generation,
  };
}

function decodeHealth(value: unknown): HealthSnapshot {
  assertObject(value);
  const obj = value as Record<string, unknown>;
  assertNoExtraFields(obj, ["activeProfileId", "profiles", "generation"]);
  const activeRaw = obj.activeProfileId;
  if (activeRaw !== null && typeof activeRaw !== "string") {
    throw new ProfileServiceError(
      "decode_failed",
      'field "activeProfileId" must be a string or null',
    );
  }
  if (!Array.isArray(obj.profiles)) {
    throw new ProfileServiceError(
      "decode_failed",
      'field "profiles" must be an array',
    );
  }
  if (typeof obj.generation !== "number" || !Number.isFinite(obj.generation)) {
    throw new ProfileServiceError(
      "decode_failed",
      'field "generation" must be a number',
    );
  }
  return {
    activeProfileId: activeRaw as string | null,
    profiles: obj.profiles.map(decodeProfile),
    generation: obj.generation,
  };
}

// ---------------------------------------------------------------------------
// Error wrapping — never echo the raw backend message (it may carry a secret)
// ---------------------------------------------------------------------------

function redactedInvokeError(
  code: ProfileServiceErrorCode,
  _command: string,
  cause: unknown,
): ProfileServiceError {
  // Tauri commands return `Result<T, String>`; the rejection's message is the
  // Rust error text, which could embed a token/origin/path. Never surface it —
  // map to a stable, secret-free code + message. The raw cause is dropped
  // deliberately: it may carry a secret and must not be reachable.
  void cause;
  return new ProfileServiceError(code, humanize(code));
}

function humanize(code: ProfileServiceErrorCode): string {
  switch (code) {
    case "preview_failed":
      return "pairing preview failed";
    case "decode_failed":
      return "received an invalid profile response";
    case "command_failed":
      return "profile command failed";
  }
}

// ---------------------------------------------------------------------------
// Service interface
// ---------------------------------------------------------------------------

export interface ProfileService {
  list(): Promise<ProfileRedacted[]>;
  previewPaste(input: PreviewPasteInput): Promise<ProfilePreview>;
  previewRepair(input: PreviewRepairInput): Promise<ProfilePreview>;
  confirmPairing(input: ConfirmPairingInput): Promise<ProfileRedacted>;
  cancelPreview(input: { readonly previewId: string }): Promise<void>;
  clearPreviews(): Promise<void>;
  rename(input: RenameInput): Promise<ProfileRedacted>;
  remove(input: ProfileIdInput): Promise<SelectResult>;
  select(input: ProfileIdInput): Promise<SelectResult>;
  health(): Promise<HealthSnapshot>;
}

export function createProfileService(bridge: TauriBridge): ProfileService {
  return {
    async list() {
      try {
        return decodeProfileList(await bridge.invoke("profile_list"));
      } catch (cause) {
        if (isProfileServiceError(cause)) throw cause;
        throw redactedInvokeError("command_failed", "profile_list", cause);
      }
    },

    async previewPaste(input) {
      try {
        return decodePreview(
          await bridge.invoke("profile_preview_paste", {
            request: { raw: input.raw },
          }),
        );
      } catch (cause) {
        if (isProfileServiceError(cause)) throw cause;
        throw redactedInvokeError(
          "preview_failed",
          "profile_preview_paste",
          cause,
        );
      }
    },

    async previewRepair(input) {
      try {
        return decodePreview(
          await bridge.invoke("profile_preview_repair", {
            request: { profileId: input.profileId, raw: input.raw },
          }),
        );
      } catch (cause) {
        if (isProfileServiceError(cause)) throw cause;
        throw redactedInvokeError(
          "preview_failed",
          "profile_preview_repair",
          cause,
        );
      }
    },

    async confirmPairing(input) {
      try {
        return decodeProfile(
          await bridge.invoke("profile_confirm_pairing", {
            request: {
              previewId: input.previewId,
              name: input.name,
              allowDuplicateOrigin: input.allowDuplicateOrigin ?? false,
            },
          }),
        );
      } catch (cause) {
        if (isProfileServiceError(cause)) throw cause;
        throw redactedInvokeError(
          "command_failed",
          "profile_confirm_pairing",
          cause,
        );
      }
    },

    async cancelPreview(input) {
      try {
        await bridge.invoke("profile_preview_cancel", {
          request: { previewId: input.previewId },
        });
      } catch (cause) {
        throw redactedInvokeError(
          "command_failed",
          "profile_preview_cancel",
          cause,
        );
      }
    },

    async clearPreviews() {
      try {
        await bridge.invoke("profile_previews_clear");
      } catch (cause) {
        throw redactedInvokeError(
          "command_failed",
          "profile_previews_clear",
          cause,
        );
      }
    },

    async rename(input) {
      try {
        return decodeProfile(
          await bridge.invoke("profile_rename", {
            request: { profileId: input.profileId, newName: input.newName },
          }),
        );
      } catch (cause) {
        if (isProfileServiceError(cause)) throw cause;
        throw redactedInvokeError("command_failed", "profile_rename", cause);
      }
    },

    async remove(input) {
      try {
        return decodeSelectResult(
          await bridge.invoke("profile_remove", {
            request: { profileId: input.profileId },
          }),
        );
      } catch (cause) {
        if (isProfileServiceError(cause)) throw cause;
        throw redactedInvokeError("command_failed", "profile_remove", cause);
      }
    },

    async select(input) {
      try {
        return decodeSelectResult(
          await bridge.invoke("profile_select", {
            request: { profileId: input.profileId },
          }),
        );
      } catch (cause) {
        if (isProfileServiceError(cause)) throw cause;
        throw redactedInvokeError("command_failed", "profile_select", cause);
      }
    },

    async health() {
      try {
        return decodeHealth(await bridge.invoke("profile_health"));
      } catch (cause) {
        if (isProfileServiceError(cause)) throw cause;
        throw redactedInvokeError("command_failed", "profile_health", cause);
      }
    },
  };
}
