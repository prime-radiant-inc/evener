import { invoke } from '@tauri-apps/api/core'

// ---------------------------------------------------------------------------
// Bridge version
// ---------------------------------------------------------------------------

export const NATIVE_BRIDGE_VERSION = 1 as const

// ---------------------------------------------------------------------------
// Redacted error — no token fields
// ---------------------------------------------------------------------------

export interface NativeError {
  readonly id: string
  readonly kind: string
  readonly message: string
}

// ---------------------------------------------------------------------------
// scanAndPreviewPairing — returns only {previewId, origin} or a structured error
// ---------------------------------------------------------------------------

export interface PairingPreview {
  readonly previewId: string
  readonly origin: string
}

export interface PairingUnavailable {
  readonly version: number
  readonly type: 'error'
  readonly error: NativeError
}

export type ScanAndPreviewResult = PairingPreview | PairingUnavailable

/**
 * Scan a Hub QR code and preview the pairing. Raw QR text travels
 * Swift→Rust only; JavaScript receives only an opaque preview ID and
 * normalized origin. Until Task 5 wires real pairing, production returns
 * a structured `pairing_unavailable` error.
 */
export async function scanAndPreviewPairing(): Promise<ScanAndPreviewResult> {
  return await invoke<ScanAndPreviewResult>('plugin:evener-native|scan_and_preview_pairing', {
    payload: {},
  })
}

// ---------------------------------------------------------------------------
// Ping (scaffold health check)
// ---------------------------------------------------------------------------

export async function ping(value: string): Promise<string | null> {
  return await invoke<{value?: string}>('plugin:evener-native|ping', {
    payload: {
      value,
    },
  }).then((r) => (r.value ? r.value : null));
}
