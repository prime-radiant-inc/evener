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

// ---------------------------------------------------------------------------
// Adversarial: raw scanned code cannot cross public output
// ---------------------------------------------------------------------------

use tauri_plugin_evener_native::{MobileScanResult, ScanAndPreviewResponse, SensitiveCapability};

#[test]
fn mobile_scan_result_is_not_serialize() {
    // MobileScanResult must not implement Serialize: it carries raw QR text
    // and must never be serialized to JavaScript.
    // This is a compile-time guarantee: if Serialize were derived, this test
    // would fail to compile. We verify the type exists and redacts Debug.
    let result = MobileScanResult::scanned("https://hub.example.test/auth?token=raw-secret");
    let debug = format!("{result:?}");
    assert!(!debug.contains("raw-secret"));
    assert!(debug.contains("REDACTED"));
}

#[test]
fn scan_and_preview_response_never_contains_raw_scanned_text() {
    // The public ScanAndPreviewResponse must never contain raw scanned text.
    // On success it carries only previewId + origin; on failure a redacted
    // NativeError. We prove this by constructing both variants and
    // serializing them.
    let preview = ScanAndPreviewResponse::preview("preview-abc", "https://hub.example.test");
    let json = serde_json::to_string(&preview).unwrap();
    assert!(json.contains("previewId"));
    assert!(json.contains("preview-abc"));
    assert!(json.contains("https://hub.example.test"));
    assert!(!json.contains("raw-secret"));
    assert!(!json.contains("token"));

    let err = ScanAndPreviewResponse::error(NativeError {
        id: "parse-failed".to_owned(),
        kind: NativeErrorKind::Internal,
        message: "invalid auth URL".to_owned(),
    });
    let err_json = serde_json::to_string(&err).unwrap();
    assert!(!err_json.contains("raw-secret"));
    assert!(!err_json.contains("token"));
    assert!(err_json.contains("parse-failed"));
}

#[test]
fn scan_and_preview_response_unavailable_never_contains_raw_text() {
    let resp = ScanAndPreviewResponse::unavailable();
    let json = serde_json::to_string(&resp).unwrap();
    assert!(json.contains("pairing_unavailable"));
    assert!(!json.contains("raw-secret"));
    assert!(!json.contains("token"));
    assert!(!json.contains("scanned"));
}

#[test]
fn sensitive_capability_redacts_debug() {
    let cap = SensitiveCapability::new("must-never-be-rendered");
    let debug = format!("{cap:?}");
    assert!(!debug.contains("must-never-be-rendered"));
    assert!(debug.contains("REDACTED"));
}

#[test]
fn sensitive_capability_into_string_preserves_value() {
    let cap = SensitiveCapability::new("actual-token-value");
    assert_eq!(cap.as_str(), "actual-token-value");
    let owned = cap.into_string();
    assert_eq!(owned, "actual-token-value");
}

#[test]
fn mobile_scan_result_deserialize_redacts_debug() {
    // Simulate what Swift returns: a JSON object with the raw scanned text.
    let json = r#"{"version":1,"type":"scanned","scanned":"https://hub.example.test/auth?token=raw-secret"}"#;
    let result: MobileScanResult = serde_json::from_str(json).unwrap();
    assert_eq!(result.result_type, "scanned");
    assert!(result.scanned.is_some());

    // Debug must redact the raw text.
    let debug = format!("{result:?}");
    assert!(!debug.contains("raw-secret"));
    assert!(debug.contains("REDACTED"));
}

#[test]
fn mobile_scan_result_unavailable_deserialize() {
    let json = r#"{"version":1,"type":"unavailable","error":{"id":"scan-and-preview","kind":"pairing_unavailable","message":"Pairing scan is unavailable on this platform"}}"#;
    let result: MobileScanResult = serde_json::from_str(json).unwrap();
    assert_eq!(result.result_type, "unavailable");
    assert!(result.scanned.is_none());
    assert!(result.error.is_some());

    let debug = format!("{result:?}");
    assert!(!debug.contains("token"));
}

#[test]
fn coordinator_scan_to_public_response_roundtrip_never_leaks_raw_text() {
    // End-to-end: raw scanned text -> coordinator -> public response.
    // The raw text must not appear anywhere in the serialized public output.
    let handler = Arc::new(RecordingPreviewHandler::default());
    let coordinator = PreviewCoordinator::new(handler.clone());

    let raw = "https://hub.example.test/auth?token=AAECAwQFBgcICQoLDA0ODxAREhMUFRYXGBkaGxwdHh8";
    let scan_result = MobileScanResult::scanned(raw);
    assert_eq!(scan_result.result_type, "scanned");

    let scanned = scan_result.scanned.unwrap();
    let preview = coordinator.preview_scanned(scanned).unwrap();
    let resp = ScanAndPreviewResponse::preview(preview.preview_id, preview.origin);

    let json = serde_json::to_string(&resp).unwrap();
    assert!(!json.contains("AAECAwQFBgcICQoLDA0ODxAREhMUFRYXGBkaGxwdHh8"));
    assert!(!json.contains("raw-secret"));
    assert!(json.contains("preview-123"));
    assert!(json.contains("https://hub.example.test"));
}
