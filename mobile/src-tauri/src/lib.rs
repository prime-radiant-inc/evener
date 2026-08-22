#[cfg_attr(mobile, tauri::mobile_entry_point)]
pub fn run() {
    use std::sync::Arc;

    // The app owns the pairing policy. The injected PreviewHandler parses the
    // scanned/pasted auth URL via PairingUrl and holds the secret in native
    // memory, returning only an opaque preview ID and normalized origin to
    // JavaScript. Production no longer returns pairing_unavailable when a
    // profile service (this handler) is installed.
    let handler = Arc::new(AppPreviewHandler);
    tauri::Builder::default()
        .plugin(tauri_plugin_evener_native::init_with_preview_handler(
            handler,
        ))
        .run(tauri::generate_context!())
        .expect("error while running tauri application");
}

pub mod error;
pub mod network_policy;
pub mod pairing;
pub mod profile;

use tauri_plugin_evener_native::{NativeError, NativeErrorKind, PairingPreview, PreviewHandler};

/// App-level preview handler: parses a scanned/pasted auth URL via
/// [`pairing::PairingUrl`] and returns only the opaque preview ID and
/// normalized origin. The raw token never crosses to JavaScript.
pub struct AppPreviewHandler;

impl PreviewHandler for AppPreviewHandler {
    fn preview(&self, scanned: &str) -> Result<PairingPreview, NativeError> {
        match pairing::PairingUrl::parse(scanned) {
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

/// The app injects this handler into the plugin so `scanAndPreviewPairing`
/// uses the real pairing parser instead of returning `pairing_unavailable`.
#[cfg(test)]
mod tests {
    use super::*;

    const TOKEN: &str = "AAECAwQFBgcICQoLDA0ODxAREhMUFRYXGBkaGxwdHh8";

    #[test]
    fn app_preview_handler_parses_valid_url() {
        let handler = AppPreviewHandler;
        let raw = format!("https://hub.example.com/auth?token={TOKEN}");
        let preview = handler.preview(&raw).unwrap();
        assert!(preview.preview_id.starts_with("preview-"));
        assert_eq!(preview.origin, "https://hub.example.com");
        assert!(!preview.preview_id.contains(TOKEN));
    }

    #[test]
    fn app_preview_handler_rejects_invalid_url() {
        let handler = AppPreviewHandler;
        let err = handler.preview("not a url").unwrap_err();
        assert_eq!(err.kind, NativeErrorKind::Internal);
        assert!(!err.message.contains(TOKEN));
    }

    #[test]
    fn app_preview_handler_never_exposes_token() {
        let handler = AppPreviewHandler;
        let raw = format!("https://hub.example.com/auth?token={TOKEN}");
        let preview = handler.preview(&raw).unwrap();
        let json = serde_json::to_string(&preview).unwrap();
        assert!(!json.contains(TOKEN));
    }
}
