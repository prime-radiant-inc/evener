use serde_json::json;
use tauri_plugin_evener_native::{
    decode_command, decode_event, decode_response, ContractFixture, NativeCommand, NativeEvent,
    NativeResponse, NATIVE_BRIDGE_VERSION,
};

const FIXTURE: &str = include_str!("../ios/Tests/PluginTests/Fixtures/contract-v1.json");

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
            NativeCommand::ContentSizeGet { .. },
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
            NativeResponse::ContentSizeValue { .. },
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
