use std::sync::{Arc, Mutex};

use serde::de::DeserializeOwned;
use tauri::{
    plugin::{PluginApi, PluginHandle},
    AppHandle, Runtime,
};

use crate::models::*;

#[cfg(target_os = "ios")]
tauri::ios_plugin_binding!(init_plugin_evener_native);

// initializes the Kotlin or Swift plugin classes
pub fn init<R: Runtime, C: DeserializeOwned>(
    _app: &AppHandle<R>,
    api: PluginApi<R, C>,
) -> crate::Result<EvenerNative<R>> {
    #[cfg(target_os = "android")]
    let handle = api.register_android_plugin("", "ExamplePlugin")?;
    #[cfg(target_os = "ios")]
    let handle = api.register_ios_plugin(init_plugin_evener_native)?;
    Ok(EvenerNative {
        handle,
        preview: Mutex::new(None),
    })
}

/// Access to the evener-native APIs.
pub struct EvenerNative<R: Runtime> {
    handle: PluginHandle<R>,
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
        self.handle
            .run_mobile_plugin("ping", payload)
            .map_err(Into::into)
    }

    /// On mobile, the Swift scanner captures the QR text and returns it to
    /// Rust. When a preview coordinator is installed, the command delegates
    /// parsing to it; JavaScript receives only `{previewId, origin}`. Until a
    /// coordinator is installed, the Swift layer returns
    /// `pairing_unavailable`.
    pub fn scan_and_preview_pairing(
        &self,
        payload: ScanAndPreviewRequest,
    ) -> crate::Result<ScanAndPreviewResponse> {
        self.handle
            .run_mobile_plugin("scanAndPreviewPairing", payload)
            .map_err(Into::into)
    }

    /// Secure store: delegates to the Swift Keychain via the mobile plugin.
    pub fn secure_get(&self, payload: SecureGetRequest) -> crate::Result<SecureGetResponse> {
        self.handle
            .run_mobile_plugin("secureGet", payload)
            .map_err(Into::into)
    }

    pub fn secure_set(&self, payload: SecureSetRequest) -> crate::Result<SecureSetResponse> {
        self.handle
            .run_mobile_plugin("secureSet", payload)
            .map_err(Into::into)
    }

    pub fn secure_delete(
        &self,
        payload: SecureDeleteRequest,
    ) -> crate::Result<SecureDeleteResponse> {
        self.handle
            .run_mobile_plugin("secureDelete", payload)
            .map_err(Into::into)
    }
}
