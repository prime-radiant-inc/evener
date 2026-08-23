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

    /// Desktop has no camera scanner, so the scan command returns
    /// `pairing_unavailable` for the scan trigger; the app crate's paste
    /// pairing goes through `ProfileStore::preview_pairing` directly.
    pub fn scan_raw_text(
        &self,
        _payload: ScanAndPreviewRequest,
    ) -> crate::Result<MobileScanResult> {
        let _ = self.handle.clone();
        Ok(MobileScanResult::unavailable())
    }

    /// Secure store: desktop has no Keychain. Returns empty/not-stored.
    /// Production mobile uses the Swift Keychain via run_mobile_plugin.
    pub fn secure_get(&self, _payload: SecureGetRequest) -> crate::Result<SecureGetResponse> {
        Ok(SecureGetResponse { present: false })
    }

    /// Desktop has no Keychain, so capability retrieval returns None.
    pub fn secure_get_capability(
        &self,
        _payload: SecureGetRequest,
    ) -> crate::Result<SecureGetCapabilityResponse> {
        Ok(SecureGetCapabilityResponse { capability: None })
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

    /// Desktop has no haptic hardware; return completed: true (no-op).
    pub fn haptic_perform(
        &self,
        _payload: HapticPerformRequest,
    ) -> crate::Result<HapticPerformResponse> {
        Ok(HapticPerformResponse { completed: true })
    }

    /// Desktop clipboard paste. Returns an empty string.
    pub fn clipboard_paste(
        &self,
        _payload: ClipboardPasteRequest,
    ) -> crate::Result<ClipboardPasteResponse> {
        Ok(ClipboardPasteResponse {
            text: "".to_owned(),
        })
    }

    /// Desktop has no Dynamic Type. return the default "large" category.
    pub fn content_size_get(
        &self,
        _payload: ContentSizeGetRequest,
    ) -> crate::Result<ContentSizeGetResponse> {
        Ok(ContentSizeGetResponse {
            category: "large".to_owned(),
        })
    }
}
