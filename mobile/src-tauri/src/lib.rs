#[cfg_attr(mobile, tauri::mobile_entry_point)]
pub fn run() {
    use std::sync::Arc;

    use tauri::Manager;
    use tauri_plugin_evener_native::EvenerNativeExt;

    // Construct production adapters. The app owns one persistent ProfileStore
    // backed by atomic file preferences (app-data), a Keychain bridge to the
    // native plugin, a real HTTP pairing probe, system DNS, and a recording
    // close-transport callback (the AppWire manager is wired in slice B).
    //
    // The managed preview handler delegates to the exact same ProfileStore,
    // so a QR scan preview can be confirmed later through the same store.
    tauri::Builder::default()
        .plugin(tauri_plugin_evener_native::init_with_preview_handler(
            // The handler is set in setup() after the ProfileStore is created,
            // because it needs the app-data path. We pass a placeholder here;
            // the plugin's setup hook replaces it.
            Arc::new(PlaceholderPreviewHandler),
        ))
        .setup(|app| {
            // Get the app-data directory for atomic file preferences.
            let app_data = app.path().app_data_dir().expect("app-data dir unavailable");
            std::fs::create_dir_all(&app_data).expect("create app-data dir");

            // File-based preferences (atomic temp/write/fsync/rename).
            let prefs: Arc<dyn profile::PreferencesStore> =
                Arc::new(profile_runtime::FilePreferences::new(&app_data));

            // Keychain bridge: delegates to the native plugin's secure store.
            // On desktop this returns empty; on mobile it calls Swift Keychain.
            let secure: Arc<dyn profile::SecureStore> =
                Arc::new(profile_runtime::KeychainBridge::new(Arc::new(
                    native::NativeSecureStore::new(app.handle().clone()),
                )));

            // Real HTTP pairing probe (redirects disabled).
            let probe: Arc<dyn profile::PairingProbe> =
                Arc::new(profile_runtime::RealPairingProbe::new());

            // System clock.
            let clock: Arc<dyn profile::Clock> = Arc::new(SystemClock);

            // Network policy with system DNS resolver.
            let policy = Arc::new(network_policy::NetworkPolicy::new(Box::new(
                profile_runtime::SystemDnsResolver,
            )));

            // Close-transport callback (AppWire manager wired in slice B).
            let close: Arc<dyn profile::CloseTransport> = Arc::new(NoopCloseTransport);

            // One managed ProfileStore.
            let store = Arc::new(profile::ProfileStore::new(
                prefs, secure, probe, clock, policy, close,
            ));

            // Managed runtime with async lifecycle mutex.
            let runtime = profile_runtime::ProfileRuntime::new(store.clone());

            // Managed preview handler backed by the exact same store.
            let handler = Arc::new(profile_runtime::ManagedPreviewHandler::new(
                store,
                Arc::new(SystemClock),
            ));

            // Replace the placeholder coordinator with the real one.
            let native = app.evener_native();
            native.set_preview_coordinator(Arc::new(
                tauri_plugin_evener_native::PreviewCoordinator::new(handler),
            ));

            // Register the profile runtime as managed state.
            app.manage(runtime);

            Ok(())
        })
        .invoke_handler(tauri::generate_handler![
            commands::profile_list,
            commands::profile_preview_paste,
            commands::profile_preview_repair,
            commands::profile_confirm_pairing,
            commands::profile_rename,
            commands::profile_remove,
            commands::profile_select,
            commands::profile_health,
            commands::hub_http_request,
            commands::appwire_open,
            commands::appwire_send,
            commands::appwire_close,
        ])
        .run(tauri::generate_context!())
        .expect("error while running tauri application");
}

pub mod commands;
pub mod error;
pub mod network_policy;
pub mod pairing;
pub mod profile;
pub mod profile_runtime;

use tauri_plugin_evener_native::PreviewHandler;

/// Placeholder preview handler used only during plugin initialization.
/// Replaced in `setup()` with the managed `ProfileRuntime`-backed handler.
struct PlaceholderPreviewHandler;

impl PreviewHandler for PlaceholderPreviewHandler {
    fn preview(
        &self,
        _scanned: &str,
    ) -> Result<tauri_plugin_evener_native::PairingPreview, tauri_plugin_evener_native::NativeError>
    {
        Err(tauri_plugin_evener_native::NativeError {
            id: "not-initialized".to_owned(),
            kind: tauri_plugin_evener_native::NativeErrorKind::Internal,
            message: "preview handler not yet initialized".to_owned(),
        })
    }
}

/// System clock using `std::time::SystemTime`.
struct SystemClock;

impl profile::Clock for SystemClock {
    fn now_secs(&self) -> u64 {
        std::time::SystemTime::now()
            .duration_since(std::time::UNIX_EPOCH)
            .map(|d| d.as_secs())
            .unwrap_or(0)
    }
}

/// No-op close transport. Replaced by the AppWire manager in slice B.
struct NoopCloseTransport;

impl profile::CloseTransport for NoopCloseTransport {
    fn close_current(&self) -> profile::ProfileGeneration {
        profile::ProfileGeneration(0)
    }
}

/// Native secure store bridge: delegates to the Tauri plugin's Keychain.
mod native {
    use tauri::{AppHandle, Runtime};
    use tauri_plugin_evener_native::{EvenerNativeExt, SecureDeleteRequest, SecureSetRequest};

    use crate::error::ProfileError;
    use crate::profile::SecureStore;

    pub struct NativeSecureStore<R: Runtime> {
        app: AppHandle<R>,
    }

    impl<R: Runtime> NativeSecureStore<R> {
        pub fn new(app: AppHandle<R>) -> Self {
            Self { app }
        }
    }

    impl<R: Runtime> SecureStore for NativeSecureStore<R> {
        fn get(&self, profile_id: &str) -> Result<Option<String>, ProfileError> {
            // The Keychain stores tokens. We query presence; if present, we
            // return the token. The native plugin returns whether the item
            // exists, not the token itself (tokens stay in native memory).
            //
            // Actually, the bridge needs the token to inject into HTTP
            // requests. The native plugin's secureGet returns present/not,
            // not the token. This is a design constraint: the app crate
            // needs the token for HTTP bearer auth, but the native bridge
            // contract returns only present/not-present.
            //
            // For now, this returns an error indicating the bridge is not
            // yet wired for token retrieval. The full token retrieval path
            // is completed in slice B with the HTTP transport.
            let _ = profile_id;
            Err(ProfileError::SecureStore(
                "native secure store not yet wired for token retrieval".to_owned(),
            ))
        }

        fn set(&self, profile_id: &str, token: &str) -> Result<(), ProfileError> {
            let native = self.app.evener_native();
            native
                .secure_set(SecureSetRequest {
                    profile_id: profile_id.to_owned(),
                    capability: token.to_owned(),
                })
                .map_err(|e| ProfileError::SecureStore(e.to_string()))?;
            Ok(())
        }

        fn delete(&self, profile_id: &str) -> Result<(), ProfileError> {
            let native = self.app.evener_native();
            native
                .secure_delete(SecureDeleteRequest {
                    profile_id: profile_id.to_owned(),
                })
                .map_err(|e| ProfileError::SecureStore(e.to_string()))?;
            Ok(())
        }
    }
}

// Keep the old parser-only handler tests as a regression check that the
// pairing parser itself is still correct, even though run() no longer uses
// the parser-only handler.
#[cfg(test)]
mod tests {
    use tauri_plugin_evener_native::{
        NativeError, NativeErrorKind, PairingPreview, PreviewHandler,
    };

    const TOKEN: &str = "AAECAwQFBgcICQoLDA0ODxAREhMUFRYXGBkaGxwdHh8";

    /// Parser-only handler kept for regression: verifies PairingUrl::parse
    /// still works correctly.
    struct ParserOnlyHandler;

    impl PreviewHandler for ParserOnlyHandler {
        fn preview(&self, scanned: &str) -> Result<PairingPreview, NativeError> {
            match crate::pairing::PairingUrl::parse(scanned) {
                Ok(p) => Ok(PairingPreview {
                    preview_id: format!("preview-{}", uuid::Uuid::new_v4().simple()),
                    origin: p.origin().to_owned(),
                }),
                Err(e) => Err(NativeError {
                    id: "pairing-parse".to_owned(),
                    kind: NativeErrorKind::Internal,
                    message: e.to_string(),
                }),
            }
        }
    }

    #[test]
    fn parser_only_handler_parses_valid_url() {
        let handler = ParserOnlyHandler;
        let raw = format!("https://hub.example.com/auth?token={TOKEN}");
        let preview = handler.preview(&raw).unwrap();
        assert!(preview.preview_id.starts_with("preview-"));
        assert_eq!(preview.origin, "https://hub.example.com");
        assert!(!preview.preview_id.contains(TOKEN));
    }

    #[test]
    fn parser_only_handler_rejects_invalid_url() {
        let handler = ParserOnlyHandler;
        let err = handler.preview("not a url").unwrap_err();
        assert_eq!(err.kind, NativeErrorKind::Internal);
        assert!(!err.message.contains(TOKEN));
    }

    #[test]
    fn parser_only_handler_never_exposes_token() {
        let handler = ParserOnlyHandler;
        let raw = format!("https://hub.example.com/auth?token={TOKEN}");
        let preview = handler.preview(&raw).unwrap();
        let json = serde_json::to_string(&preview).unwrap();
        assert!(!json.contains(TOKEN));
    }
}
