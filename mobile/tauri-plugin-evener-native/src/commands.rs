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

#[command]
pub(crate) async fn scan_and_preview_pairing<R: Runtime>(
    app: AppHandle<R>,
    payload: ScanAndPreviewRequest,
) -> Result<ScanAndPreviewResponse> {
    app.evener_native().scan_and_preview_pairing(payload)
}
