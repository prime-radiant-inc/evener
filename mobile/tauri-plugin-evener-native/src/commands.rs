use tauri::{command, AppHandle, Runtime};

use crate::models::*;
use crate::EvenerNativeExt;
use crate::Result;

#[command]
pub(crate) async fn ping<R: Runtime>(
    app: AppHandle<R>,
    payload: PingRequest,
) -> Result<PingResponse> {
    app.evener_native().ping(payload)
}

/// Scan and preview pairing. On mobile, Swift captures the QR text and
/// returns it to Rust. When a `PreviewCoordinator` is installed, Rust
/// converts the raw text to a `SensitiveScannedCode`, feeds it through
/// the coordinator, and returns only `{previewId, origin}` to JS.
/// When no coordinator is installed, returns structured `pairing_unavailable`.
#[command]
pub(crate) async fn scan_and_preview_pairing<R: Runtime>(
    app: AppHandle<R>,
    payload: ScanAndPreviewRequest,
) -> Result<ScanAndPreviewResponse> {
    let native = app.evener_native();

    // Check if a coordinator is installed.
    if let Some(_coordinator) = native.preview_coordinator() {
        // On mobile, ask Swift to scan and return raw text. On desktop,
        // there is no camera, so we return pairing_unavailable.
        #[cfg(mobile)]
        {
            // The Swift side returns a ScanAndPreviewResponse with the raw
            // scanned text in the error.message field (a transport-only
            // channel — never returned to JS as-is). We then feed it through
            // the coordinator.
            //
            // Actually, the Swift scanner returns raw text via a dedicated
            // response. We call run_mobile_plugin which returns the raw text
            // from Swift, then convert and feed through the coordinator.
            let scan_result = native.scan_and_preview_pairing(payload);
            match scan_result {
                Ok(resp) => {
                    // Swift returned a structured response. If it contains
                    // raw text (in the error.message field as a transport
                    // channel), feed it through the coordinator.
                    if resp.response_type == "scanned" {
                        let scanned = SensitiveScannedCode::new(&resp.error.message);
                        match coordinator.preview_scanned(scanned) {
                            Ok(preview) => {
                                return Ok(ScanAndPreviewResponse {
                                    version: crate::NATIVE_BRIDGE_VERSION,
                                    response_type: "preview".to_owned(),
                                    error: NativeError {
                                        id: preview.preview_id,
                                        kind: NativeErrorKind::Internal,
                                        message: preview.origin,
                                    },
                                });
                            }
                            Err(e) => {
                                return Ok(ScanAndPreviewResponse {
                                    version: crate::NATIVE_BRIDGE_VERSION,
                                    response_type: "error".to_owned(),
                                    error: e,
                                });
                            }
                        }
                    }
                    // Swift returned a direct preview or error.
                    return Ok(resp);
                }
                Err(e) => {
                    return Ok(ScanAndPreviewResponse {
                        version: crate::NATIVE_BRIDGE_VERSION,
                        response_type: "error".to_owned(),
                        error: NativeError {
                            id: "scan-failed".to_owned(),
                            kind: NativeErrorKind::Scanner,
                            message: e.to_string(),
                        },
                    });
                }
            }
        }
        #[cfg(desktop)]
        {
            let _ = payload;
            // Desktop has no camera scanner even with a coordinator installed.
            // The paste path is the primary desktop flow.
            return Ok(ScanAndPreviewResponse {
                version: crate::NATIVE_BRIDGE_VERSION,
                response_type: "error".to_owned(),
                error: NativeError {
                    id: "scan-and-preview".to_owned(),
                    kind: NativeErrorKind::PairingUnavailable,
                    message: "Pairing scan is unavailable on this platform".to_owned(),
                },
            });
        }
    }

    // No coordinator installed: return structured unavailable.
    native.scan_and_preview_pairing(payload)
}
