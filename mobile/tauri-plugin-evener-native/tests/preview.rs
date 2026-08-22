use std::sync::{Arc, Mutex};

use tauri_plugin_evener_native::{
    NativeError, PairingPreview, PreviewCoordinator, PreviewHandler, SensitiveScannedCode,
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
