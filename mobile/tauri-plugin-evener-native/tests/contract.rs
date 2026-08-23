use serde_json::json;
use tauri_plugin_evener_native::{
    decode_command, decode_event, decode_response, ContractFixture, NativeCommand, NativeEvent,
    NativeResponse, NATIVE_BRIDGE_VERSION,
};

const FIXTURE: &str = include_str!("../ios/Tests/PluginTests/Fixtures/contract-v1.json");

// ---------------------------------------------------------------------------
// Pin the exact public model shapes that cross to JavaScript via serde.
// The TS transport adapter must match these raw shapes — not the versioned
// NativeResponse union. These tests prevent the fake from drifting from Rust.
// ---------------------------------------------------------------------------

use tauri_plugin_evener_native::{
    ContentSizeGetResponse, HapticPerformResponse, NativeError, NativeErrorKind,
    ScanAndPreviewResponse,
};

#[test]
fn haptic_perform_response_serializes_to_completed_boolean() {
    let response = HapticPerformResponse { completed: true };
    let json = serde_json::to_value(&response).unwrap();
    assert_eq!(json, json!({ "completed": true }));
    // No version or type field — the TS adapter must decode {completed:true}.
    assert!(json.get("version").is_none());
    assert!(json.get("type").is_none());
}

#[test]
fn content_size_get_response_serializes_to_category_string() {
    let response = ContentSizeGetResponse {
        category: "extraExtraLarge".to_owned(),
    };
    let json = serde_json::to_value(&response).unwrap();
    assert_eq!(json, json!({ "category": "extraExtraLarge" }));
    assert!(json.get("version").is_none());
    assert!(json.get("type").is_none());
}

#[test]
fn scan_and_preview_response_serializes_versioned_pairing_preview() {
    let response = ScanAndPreviewResponse::preview("pv-1", "https://hub.example.com:8443");
    let json = serde_json::to_value(&response).unwrap();
    assert_eq!(
        json,
        json!({
            "version": NATIVE_BRIDGE_VERSION,
            "type": "pairing.preview",
            "previewId": "pv-1",
            "origin": "https://hub.example.com:8443"
        })
    );
    // error field is skipped when None.
    assert!(json.get("error").is_none());
}

#[test]
fn scan_and_preview_response_serializes_versioned_error() {
    let response = ScanAndPreviewResponse::error(NativeError {
        id: "scan-and-preview".to_owned(),
        kind: NativeErrorKind::PairingUnavailable,
        message: "Pairing scan is unavailable on this platform".to_owned(),
    });
    let json = serde_json::to_value(&response).unwrap();
    assert_eq!(
        json,
        json!({
            "version": NATIVE_BRIDGE_VERSION,
            "type": "error",
            "error": {
                "id": "scan-and-preview",
                "kind": "pairing_unavailable",
                "message": "Pairing scan is unavailable on this platform"
            }
        })
    );
    // previewId and origin are skipped when None.
    assert!(json.get("previewId").is_none());
    assert!(json.get("origin").is_none());
}

#[test]
fn decodes_every_v1_fixture_variant() {
    let fixture: ContractFixture = serde_json::from_str(FIXTURE).expect("fixture is JSON");
    assert_eq!(fixture.bridge_version, NATIVE_BRIDGE_VERSION);

    let commands: Vec<NativeCommand> = fixture
        .commands
        .into_iter()
        .map(decode_command)
        .collect::<Result<_, _>>()
        .expect("all command variants decode");
    assert!(matches!(
        commands.as_slice(),
        [
            NativeCommand::SecureGet { .. },
            NativeCommand::SecureSet { .. },
            NativeCommand::SecureDelete { .. },
            NativeCommand::PairingScanAndPreview { .. },
            NativeCommand::PermissionRequest { .. },
            NativeCommand::SpeechStart { .. },
            NativeCommand::SpeechStop { .. },
            NativeCommand::SynthesisSpeak { .. },
            NativeCommand::SynthesisStop { .. },
            NativeCommand::HapticPerform { .. },
            NativeCommand::ClipboardPaste { .. },
            NativeCommand::ContentSizeGet { .. },
            NativeCommand::VoicePermissions { .. },
            NativeCommand::VoiceStart { .. },
            NativeCommand::VoiceStop { .. },
            NativeCommand::VoiceSpeak { .. },
            NativeCommand::VoiceStopSpeaking { .. },
            NativeCommand::VoiceSetRate { .. },
        ]
    ));

    let responses: Vec<NativeResponse> = fixture
        .responses
        .into_iter()
        .map(decode_response)
        .collect::<Result<_, _>>()
        .expect("all response variants decode");
    assert!(matches!(
        responses.as_slice(),
        [
            NativeResponse::SecureState { .. },
            NativeResponse::SecureUpdated { .. },
            NativeResponse::SecureDeleted { .. },
            NativeResponse::PairingPreview { .. },
            NativeResponse::PermissionStatus { .. },
            NativeResponse::SpeechReady { .. },
            NativeResponse::SpeechStopped { .. },
            NativeResponse::SynthesisStarted { .. },
            NativeResponse::SynthesisStopped { .. },
            NativeResponse::HapticCompleted { .. },
            NativeResponse::ClipboardPasted { .. },
            NativeResponse::ContentSizeValue { .. },
            NativeResponse::VoicePermissionsResponse { .. },
            NativeResponse::VoiceReady { .. },
            NativeResponse::VoiceStopped { .. },
            NativeResponse::VoiceQueued { .. },
            NativeResponse::VoiceSpeakingStopped { .. },
            NativeResponse::VoiceRateSet { .. },
            NativeResponse::Error { .. },
        ]
    ));

    let events: Vec<NativeEvent> = fixture
        .events
        .into_iter()
        .map(decode_event)
        .collect::<Result<_, _>>()
        .expect("all event variants decode");
    assert!(matches!(
        events.as_slice(),
        [
            NativeEvent::LifecycleChanged { .. },
            NativeEvent::SpeechPartial { .. },
            NativeEvent::SpeechFinal { .. },
            NativeEvent::BargeIn { .. },
            NativeEvent::VoiceLevel { .. },
            NativeEvent::VoicePartial { .. },
            NativeEvent::VoiceFinal { .. },
            NativeEvent::VoiceSpeechStarted { .. },
            NativeEvent::VoiceSpeechFinished { .. },
            NativeEvent::VoiceBargeIn { .. },
            NativeEvent::VoiceInterrupted { .. },
            NativeEvent::VoiceErrorEvent { .. },
        ]
    ));
}

#[test]
fn rejects_unknown_versions_and_discriminators() {
    assert!(decode_command(json!({ "version": 2, "type": "secure.get" })).is_err());
    assert!(decode_command(json!({ "version": 1, "type": "secure.export" })).is_err());

    assert!(
        decode_response(json!({ "version": 2, "type": "secure.state", "present": true })).is_err()
    );
    assert!(decode_response(
        json!({ "version": 1, "type": "secure.value", "capability": "secret" })
    )
    .is_err());
    assert!(
        decode_event(json!({ "version": 2, "type": "lifecycle.changed", "state": "active" }))
            .is_err()
    );
    assert!(
        decode_event(json!({ "version": 1, "type": "lifecycle.future", "state": "active" }))
            .is_err()
    );
}

#[test]
fn rejects_unknown_and_secret_bearing_response_fields() {
    assert!(decode_response(json!({
      "version": 1,
      "type": "secure.state",
      "present": true,
      "capability": "must-not-cross"
    }))
    .is_err());
    assert!(decode_response(json!({
      "version": 1,
      "type": "error",
      "error": {
        "id": "error-id",
        "kind": "internal",
        "message": "redacted",
        "token": "must-not-cross"
      }
    }))
    .is_err());
}
