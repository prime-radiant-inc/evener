use std::sync::{Arc, Mutex};

use tauri_plugin_evener_native::{
    NativeError, NativeErrorKind, PairingPreview, PreviewCoordinator, PreviewHandler,
    SensitiveScannedCode,
};

#[derive(Default)]
struct RecordingPreviewHandler {
    scanned: Mutex<Vec<String>>,
}

impl PreviewHandler for RecordingPreviewHandler {
    fn preview(&self, scanned: &str) -> Result<PairingPreview, NativeError> {
        self.scanned.lock().unwrap().push(scanned.to_owned());
        Ok(PairingPreview {
            preview_id: "preview-123".to_owned(),
            origin: "https://hub.example.test".to_owned(),
        })
    }
}

#[test]
fn scanned_text_stays_inside_rust_and_only_preview_crosses_to_javascript() {
    let handler = Arc::new(RecordingPreviewHandler::default());
    let coordinator = PreviewCoordinator::new(handler.clone());

    let preview = coordinator
        .preview_scanned(SensitiveScannedCode::new(
            "https://hub.example.test/auth?token=raw-secret",
        ))
        .expect("injected handler returns preview");

    assert_eq!(
        handler.scanned.lock().unwrap().as_slice(),
        ["https://hub.example.test/auth?token=raw-secret"]
    );
    assert_eq!(preview.preview_id, "preview-123");
    assert_eq!(preview.origin, "https://hub.example.test");

    let serialized = serde_json::to_string(&preview).unwrap();
    assert!(!serialized.contains("raw-secret"));
}

#[test]
fn scanned_text_is_redacted_from_debug_output() {
    let scanned = SensitiveScannedCode::new("https://hub.example.test/auth?token=raw-secret");
    assert_eq!(format!("{scanned:?}"), "SensitiveScannedCode([REDACTED])");
}

#[test]
fn installed_coordinator_converts_raw_scan_to_preview() {
    // When a PreviewCoordinator is installed, raw scanned text is converted
    // to a {previewId, origin} preview. The raw text never appears in output.
    let handler = Arc::new(RecordingPreviewHandler::default());
    let coordinator = PreviewCoordinator::new(handler);

    let raw = "https://hub.example.test/auth?token=AAECAwQFBgcICQoLDA0ODxAREhMUFRYXGBkaGxwdHh8";
    let preview = coordinator
        .preview_scanned(SensitiveScannedCode::new(raw))
        .expect("coordinator returns preview");

    assert_eq!(preview.preview_id, "preview-123");
    assert_eq!(preview.origin, "https://hub.example.test");
    let json = serde_json::to_string(&preview).unwrap();
    assert!(!json.contains("AAECAwQFBgcICQoLDA0ODxAREhMUFRYXGBkaGxwdHh8"));
}

#[test]
fn coordinator_handler_rejects_invalid_scan_text() {
    struct RejectHandler;
    impl PreviewHandler for RejectHandler {
        fn preview(&self, _scanned: &str) -> Result<PairingPreview, NativeError> {
            Err(NativeError {
                id: "parse-failed".to_owned(),
                kind: NativeErrorKind::Internal,
                message: "invalid auth URL".to_owned(),
            })
        }
    }

    let coordinator = PreviewCoordinator::new(Arc::new(RejectHandler));
    let err = coordinator
        .preview_scanned(SensitiveScannedCode::new("not a url"))
        .unwrap_err();
    assert_eq!(err.id, "parse-failed");
    assert!(!err.message.contains("not a url"));
}

#[test]
fn no_coordinator_returns_unavailable_error() {
    // When no PreviewCoordinator is installed, the scan command returns
    // a structured pairing_unavailable error.
    let err = NativeError {
        id: "scan-and-preview".to_owned(),
        kind: NativeErrorKind::PairingUnavailable,
        message: "Pairing scan is unavailable on this platform".to_owned(),
    };
    assert_eq!(err.kind, NativeErrorKind::PairingUnavailable);
    let json = serde_json::to_string(&err).unwrap();
    assert!(json.contains("pairing_unavailable"));
    assert!(!json.contains("token"));
}
