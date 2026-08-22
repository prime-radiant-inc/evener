use std::sync::{Arc, Mutex};

use serde::de::DeserializeOwned;
use tauri::{plugin::PluginApi, AppHandle, Runtime};

use crate::models::*;

pub fn init<R: Runtime, C: DeserializeOwned>(
    app: &AppHandle<R>,
    _api: PluginApi<R, C>,
) -> crate::Result<EvenerNative<R>> {
    Ok(EvenerNative {
        handle: app.clone(),
        preview: Mutex::new(None),
    })
}

/// Access to the evener-native APIs.
pub struct EvenerNative<R: Runtime> {
    handle: AppHandle<R>,
    preview: Mutex<Option<Arc<PreviewCoordinator>>>,
}

impl<R: Runtime> EvenerNative<R> {
    pub fn set_preview_coordinator(&self, coordinator: Arc<PreviewCoordinator>) {
        *self.preview.lock().unwrap() = Some(coordinator);
    }

    pub fn preview_coordinator(&self) -> Option<Arc<PreviewCoordinator>> {
        self.preview.lock().unwrap().clone()
    }

    pub fn ping(&self, payload: PingRequest) -> crate::Result<PingResponse> {
        Ok(PingResponse {
            value: payload.value,
        })
    }

    /// When a preview coordinator is installed, the scan path delegates to
    /// it. Desktop has no camera scanner, so the command returns
    /// `pairing_unavailable` for the scan trigger; the app crate's paste
    /// pairing goes through `ProfileStore::preview_pairing` directly.
    pub fn scan_and_preview_pairing(
        &self,
        _payload: ScanAndPreviewRequest,
    ) -> crate::Result<ScanAndPreviewResponse> {
        let _ = self.handle.clone();
        Ok(ScanAndPreviewResponse {
            version: crate::NATIVE_BRIDGE_VERSION,
            response_type: "error".to_owned(),
            error: NativeError {
                id: "scan-and-preview".to_owned(),
                kind: NativeErrorKind::PairingUnavailable,
                message: "Pairing scan is unavailable on this platform".to_owned(),
            },
        })
    }

    /// Secure store: desktop has no Keychain. Returns empty/not-stored.
    /// Production mobile uses the Swift Keychain via run_mobile_plugin.
    pub fn secure_get(&self, _payload: SecureGetRequest) -> crate::Result<SecureGetResponse> {
        Ok(SecureGetResponse { present: false })
    }

    pub fn secure_set(&self, _payload: SecureSetRequest) -> crate::Result<SecureSetResponse> {
        Ok(SecureSetResponse { stored: false })
    }

    pub fn secure_delete(
        &self,
        _payload: SecureDeleteRequest,
    ) -> crate::Result<SecureDeleteResponse> {
        Ok(SecureDeleteResponse { deleted: false })
    }
}
