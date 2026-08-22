use tauri::{command, AppHandle, Runtime};

use crate::models::*;
use crate::EvenerNativeExt;
use crate::Result;

/// Perform a haptic feedback pattern. Delegates to the Swift
/// `UIFeedbackGenerator` on mobile; a no-op on desktop. Never carries a
/// secret or credential.
#[command]
pub(crate) async fn haptic_perform<R: Runtime>(
    app: AppHandle<R>,
    payload: HapticPerformRequest,
) -> Result<HapticPerformResponse> {
    app.evener_native().haptic_perform(payload)
}

/// Get the current `UIContentSizeCategory`. Returns a semantic category
/// string that the renderer maps to a bounded type scale. On desktop,
/// returns "large".
#[command]
pub(crate) async fn content_size_get<R: Runtime>(
    app: AppHandle<R>,
    payload: ContentSizeGetRequest,
) -> Result<ContentSizeGetResponse> {
    app.evener_native().content_size_get(payload)
}

#[command]
pub(crate) async fn ping<R: Runtime>(
    app: AppHandle<R>,
    payload: PingRequest,
) -> Result<PingResponse> {
    app.evener_native().ping(payload)
}

/// Scan and preview pairing. On mobile, Swift captures the QR text and
/// returns it to Rust via a private `MobileScanResult` (Deserialize-only,
/// redacted Debug). When a `PreviewCoordinator` is installed, Rust wraps the
/// raw text in `SensitiveScannedCode`, feeds it through the coordinator, and
/// returns only `{previewId, origin}` to JS. When no coordinator is
/// installed, returns structured `pairing_unavailable`.
#[command]
pub(crate) async fn scan_and_preview_pairing<R: Runtime>(
    app: AppHandle<R>,
    payload: ScanAndPreviewRequest,
) -> Result<ScanAndPreviewResponse> {
    let native = app.evener_native();

    // If a coordinator is installed, the scan path delegates to it.
    // On desktop the coordinator is bound but only the mobile branch
    // consumes it (no camera on desktop).
    if let Some(coordinator) = native.preview_coordinator() {
        // On mobile, ask Swift to scan and return raw text via the private
        // MobileScanResult type. On desktop, there is no camera scanner.
        #[cfg(mobile)]
        {
            let scan_result = native.scan_raw_text(payload);
            return Ok(convert_scan_result(scan_result, &coordinator));
        }
        #[cfg(desktop)]
        {
            let _ = payload;
            let _ = coordinator;
            // Desktop has no camera scanner even with a coordinator installed.
            // The paste path is the primary desktop flow.
            return Ok(ScanAndPreviewResponse::unavailable());
        }
    }

    // No coordinator installed: return structured unavailable.
    Ok(ScanAndPreviewResponse::unavailable())
}

/// Convert the private Swift scan result to a public response. Raw scanned
/// text is fed through the coordinator and never crosses to the output.
#[cfg_attr(desktop, allow(dead_code))]
fn convert_scan_result(
    scan_result: crate::Result<MobileScanResult>,
    coordinator: &PreviewCoordinator,
) -> ScanAndPreviewResponse {
    match scan_result {
        Ok(result) => match result.result_type.as_str() {
            "scanned" => {
                let Some(scanned) = result.scanned else {
                    return ScanAndPreviewResponse::error(NativeError {
                        id: "scan-and-preview".to_owned(),
                        kind: NativeErrorKind::Internal,
                        message: "scanner returned no scanned text".to_owned(),
                    });
                };
                match coordinator.preview_scanned(scanned) {
                    Ok(preview) => {
                        ScanAndPreviewResponse::preview(preview.preview_id, preview.origin)
                    }
                    Err(e) => ScanAndPreviewResponse::error(e),
                }
            }
            "unavailable" => ScanAndPreviewResponse::unavailable(),
            _ => {
                let err = result.error.unwrap_or_else(|| NativeError {
                    id: "scan-and-preview".to_owned(),
                    kind: NativeErrorKind::Internal,
                    message: "unknown scan result type".to_owned(),
                });
                ScanAndPreviewResponse::error(err)
            }
        },
        Err(e) => ScanAndPreviewResponse::error(NativeError {
            id: "scan-failed".to_owned(),
            kind: NativeErrorKind::Scanner,
            message: e.to_string(),
        }),
    }
}
