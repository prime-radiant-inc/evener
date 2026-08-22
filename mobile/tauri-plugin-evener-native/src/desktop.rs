use serde::de::DeserializeOwned;
use tauri::{plugin::PluginApi, AppHandle, Runtime};

use crate::models::*;

pub fn init<R: Runtime, C: DeserializeOwned>(
    app: &AppHandle<R>,
    _api: PluginApi<R, C>,
) -> crate::Result<EvenerNative<R>> {
    Ok(EvenerNative(app.clone()))
}

/// Access to the evener-native APIs.
pub struct EvenerNative<R: Runtime>(AppHandle<R>);

impl<R: Runtime> EvenerNative<R> {
    pub fn ping(&self, payload: PingRequest) -> crate::Result<PingResponse> {
        Ok(PingResponse {
            value: payload.value,
        })
    }

    /// Desktop fallback: pairing is unavailable until the iOS backend is wired
    /// in Task 5. Returns the structured error, never a fake profile/token.
    pub fn scan_and_preview_pairing(
        &self,
        _payload: ScanAndPreviewRequest,
    ) -> crate::Result<ScanAndPreviewResponse> {
        Ok(ScanAndPreviewResponse {
            version: crate::NATIVE_BRIDGE_VERSION,
            response_type: "error".to_owned(),
            error: NativeError {
                id: "scan-and-preview".to_owned(),
                kind: NativeErrorKind::PairingUnavailable,
                message: "Pairing is unavailable".to_owned(),
            },
        })
    }
}
