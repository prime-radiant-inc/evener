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
    Ok(EvenerNative(handle))
}

/// Access to the evener-native APIs.
pub struct EvenerNative<R: Runtime>(PluginHandle<R>);

impl<R: Runtime> EvenerNative<R> {
    pub fn ping(&self, payload: PingRequest) -> crate::Result<PingResponse> {
        self.0
            .run_mobile_plugin("ping", payload)
            .map_err(Into::into)
    }

    pub fn scan_and_preview_pairing(
        &self,
        payload: ScanAndPreviewRequest,
    ) -> crate::Result<ScanAndPreviewResponse> {
        self.0
            .run_mobile_plugin("scanAndPreviewPairing", payload)
            .map_err(Into::into)
    }
}
