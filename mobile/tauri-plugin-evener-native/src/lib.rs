use std::sync::Arc;

use tauri::{
    plugin::{Builder, TauriPlugin},
    Manager, Runtime,
};

pub use models::*;

#[cfg(desktop)]
mod desktop;
#[cfg(mobile)]
mod mobile;

mod commands;
mod error;
mod models;

pub use error::{Error, Result};

#[cfg(desktop)]
use desktop::EvenerNative;
#[cfg(mobile)]
use mobile::EvenerNative;

/// Extensions to [`tauri::App`], [`tauri::AppHandle`] and [`tauri::Window`] to access the evener-native APIs.
pub trait EvenerNativeExt<R: Runtime> {
    fn evener_native(&self) -> &EvenerNative<R>;
}

impl<R: Runtime, T: Manager<R>> crate::EvenerNativeExt<R> for T {
    fn evener_native(&self) -> &EvenerNative<R> {
        self.state::<EvenerNative<R>>().inner()
    }
}

/// Initializes the plugin with a preview handler. When a profile service is
/// installed (the app crate provides a [`PreviewHandler`] that parses via
/// `PairingUrl`), `scanAndPreviewPairing` delegates to it instead of returning
/// `pairing_unavailable`.
pub fn init_with_preview_handler<R: Runtime>(handler: Arc<dyn PreviewHandler>) -> TauriPlugin<R> {
    Builder::new("evener-native")
        .invoke_handler(tauri::generate_handler![
            commands::ping,
            commands::scan_and_preview_pairing
        ])
        .setup(move |app, api| {
            #[cfg(mobile)]
            let evener_native = mobile::init(app, api)?;
            #[cfg(desktop)]
            let evener_native = desktop::init(app, api)?;
            evener_native
                .set_preview_coordinator(Arc::new(PreviewCoordinator::new(handler.clone())));
            app.manage(evener_native);
            Ok(())
        })
        .build()
}

/// Initializes the plugin without a preview handler. `scanAndPreviewPairing`
/// returns `pairing_unavailable` until a profile service is installed.
pub fn init<R: Runtime>() -> TauriPlugin<R> {
    Builder::new("evener-native")
        .invoke_handler(tauri::generate_handler![
            commands::ping,
            commands::scan_and_preview_pairing
        ])
        .setup(|app, api| {
            #[cfg(mobile)]
            let evener_native = mobile::init(app, api)?;
            #[cfg(desktop)]
            let evener_native = desktop::init(app, api)?;
            app.manage(evener_native);
            Ok(())
        })
        .build()
}
