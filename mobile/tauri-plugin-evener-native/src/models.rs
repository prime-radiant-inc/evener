use serde::{Deserialize, Serialize};
use serde_json::Value;
use std::fmt;
use std::sync::Arc;

// ---------------------------------------------------------------------------
// Bridge version
// ---------------------------------------------------------------------------

pub const NATIVE_BRIDGE_VERSION: u8 = 1;

// ---------------------------------------------------------------------------
// Ping (generated scaffold command kept for plugin wiring)
// ---------------------------------------------------------------------------

#[derive(Debug, Deserialize, Serialize)]
#[serde(rename_all = "camelCase")]
pub struct PingRequest {
    pub value: Option<String>,
}

#[derive(Debug, Clone, Default, Deserialize, Serialize)]
#[serde(rename_all = "camelCase")]
pub struct PingResponse {
    pub value: Option<String>,
}

// ---------------------------------------------------------------------------
// scanAndPreviewPairing command args/response
// ---------------------------------------------------------------------------

#[derive(Debug, Default, Deserialize, Serialize)]
#[serde(rename_all = "camelCase")]
pub struct ScanAndPreviewRequest {}

// ---------------------------------------------------------------------------
// hapticPerform command
// ---------------------------------------------------------------------------

#[derive(Debug, Clone, Deserialize, Serialize)]
#[serde(rename_all = "camelCase")]
pub struct HapticPerformRequest {
    pub kind: String,
}

#[derive(Debug, Clone, Deserialize, Serialize)]
#[serde(rename_all = "camelCase")]
pub struct HapticPerformResponse {
    pub completed: bool,
}

// ---------------------------------------------------------------------------
// clipboardPaste command
// ---------------------------------------------------------------------------

#[derive(Debug, Clone, Default, Deserialize, Serialize)]
#[serde(rename_all = "camelCase")]
pub struct ClipboardPasteRequest {}

#[derive(Debug, Clone, Deserialize, Serialize)]
#[serde(rename_all = "camelCase")]
pub struct ClipboardPasteResponse {
    pub text: String,
}

// ---------------------------------------------------------------------------
// contentSizeGet command
// ---------------------------------------------------------------------------

#[derive(Debug, Clone, Deserialize, Serialize)]
#[serde(rename_all = "camelCase")]
pub struct ContentSizeGetRequest {}

#[derive(Debug, Clone, Deserialize, Serialize)]
#[serde(rename_all = "camelCase")]
pub struct ContentSizeGetResponse {
    pub category: String,
}

/// Public response to the `scanAndPreviewPairing` command. This is the only
/// scan data that crosses to JavaScript. On success `response_type` is
/// `"pairing.preview"` with `preview_id` and `origin`. On failure it is
/// `"error"` with a redacted `NativeError`. Raw scanned text never appears
/// in this type.
#[derive(Debug, Clone, Deserialize, Serialize)]
#[serde(rename_all = "camelCase")]
pub struct ScanAndPreviewResponse {
    pub version: u8,
    #[serde(rename = "type")]
    pub response_type: String,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub preview_id: Option<String>,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub origin: Option<String>,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub error: Option<NativeError>,
}

impl ScanAndPreviewResponse {
    /// Construct a successful `pairing.preview` response.
    pub fn preview(preview_id: impl Into<String>, origin: impl Into<String>) -> Self {
        Self {
            version: NATIVE_BRIDGE_VERSION,
            response_type: "pairing.preview".to_owned(),
            preview_id: Some(preview_id.into()),
            origin: Some(origin.into()),
            error: None,
        }
    }

    /// Construct an `error` response with a redacted error.
    pub fn error(err: NativeError) -> Self {
        Self {
            version: NATIVE_BRIDGE_VERSION,
            response_type: "error".to_owned(),
            preview_id: None,
            origin: None,
            error: Some(err),
        }
    }

    /// Construct a `pairing_unavailable` error response.
    pub fn unavailable() -> Self {
        Self::error(NativeError {
            id: "scan-and-preview".to_owned(),
            kind: NativeErrorKind::PairingUnavailable,
            message: "Pairing scan is unavailable on this platform".to_owned(),
        })
    }
}

// ---------------------------------------------------------------------------
// Error kind
// ---------------------------------------------------------------------------

#[derive(Debug, Clone, Serialize, Deserialize, PartialEq, Eq)]
#[serde(rename_all = "snake_case")]
pub enum NativeErrorKind {
    PairingUnavailable,
    Internal,
    SecureStore,
    Scanner,
    PermissionDenied,
    Unsupported,
}

// ---------------------------------------------------------------------------
// NativeError — redacted, no token fields
// ---------------------------------------------------------------------------

#[derive(Debug, Clone, Serialize, Deserialize, PartialEq, Eq)]
#[serde(deny_unknown_fields)]
pub struct NativeError {
    pub id: String,
    pub kind: NativeErrorKind,
    pub message: String,
}

// ---------------------------------------------------------------------------
// MobileScanResult — private internal Swift→Rust scan result.
// Deserialize-only (never Serialize), redacted Debug. Raw QR text stays
// Swift→Rust and never crosses to JavaScript.
// ---------------------------------------------------------------------------

/// Internal scan result returned by the Swift scanner to Rust. This type is
/// never serialized back to JavaScript: it is Deserialize-only with a
/// redacted Debug impl so the raw scanned code cannot leak through logs or
/// public output.
#[derive(Debug, Deserialize)]
#[serde(rename_all = "camelCase")]
pub struct MobileScanResult {
    /// Bridge version (must be 1).
    pub version: u8,
    /// "scanned" when the scanner captured text; "unavailable" when no
    /// scanner/permission; "error" on failure.
    #[serde(rename = "type")]
    pub result_type: String,
    /// Raw scanned text — present only when `result_type == "scanned"`.
    /// Wrapped in [`SensitiveScannedCode`] so Debug redacts it.
    #[serde(default)]
    pub scanned: Option<SensitiveScannedCode>,
    /// Structured error — present only when `result_type == "error"` or
    /// `"unavailable"`.
    #[serde(default)]
    pub error: Option<NativeError>,
}

impl MobileScanResult {
    /// Construct a "scanned" result carrying raw text.
    pub fn scanned(raw: impl Into<String>) -> Self {
        Self {
            version: NATIVE_BRIDGE_VERSION,
            result_type: "scanned".to_owned(),
            scanned: Some(SensitiveScannedCode::new(raw)),
            error: None,
        }
    }

    /// Construct an "unavailable" result (no camera/permission).
    pub fn unavailable() -> Self {
        Self {
            version: NATIVE_BRIDGE_VERSION,
            result_type: "unavailable".to_owned(),
            scanned: None,
            error: Some(NativeError {
                id: "scan-and-preview".to_owned(),
                kind: NativeErrorKind::PairingUnavailable,
                message: "Pairing scan is unavailable on this platform".to_owned(),
            }),
        }
    }

    /// Construct an "error" result.
    pub fn error(err: NativeError) -> Self {
        Self {
            version: NATIVE_BRIDGE_VERSION,
            result_type: "error".to_owned(),
            scanned: None,
            error: Some(err),
        }
    }
}

// ---------------------------------------------------------------------------
// SecureGetCapabilityResponse — internal Rust↔Swift token retrieval.
// Deserialize-only (never Serialize), redacted Debug. The Keychain capability
// never crosses to JavaScript.
// ---------------------------------------------------------------------------

/// Internal response from the Swift Keychain `load` operation. This type is
/// never serialized to JavaScript: it is Deserialize-only with a redacted
/// Debug impl so the capability cannot leak.
#[derive(Debug, Deserialize)]
#[serde(rename_all = "camelCase")]
pub struct SecureGetCapabilityResponse {
    /// The stored capability/token, or None if not present.
    #[serde(default)]
    pub capability: Option<SensitiveCapability>,
}

/// A capability token that redacts its Debug output. Used only in internal
/// Rust↔Swift bridge types that must not serialize to JavaScript.
pub struct SensitiveCapability(String);

impl SensitiveCapability {
    pub fn new(raw: impl Into<String>) -> Self {
        Self(raw.into())
    }

    pub fn as_str(&self) -> &str {
        &self.0
    }

    pub fn into_string(self) -> String {
        self.0
    }
}

impl fmt::Debug for SensitiveCapability {
    fn fmt(&self, f: &mut fmt::Formatter<'_>) -> fmt::Result {
        f.write_str("SensitiveCapability([REDACTED])")
    }
}

impl<'de> Deserialize<'de> for SensitiveCapability {
    fn deserialize<D>(deserializer: D) -> Result<Self, D::Error>
    where
        D: serde::Deserializer<'de>,
    {
        let s = String::deserialize(deserializer)?;
        Ok(Self(s))
    }
}

// ---------------------------------------------------------------------------
// NativeCommand
// ---------------------------------------------------------------------------

#[derive(Debug, Clone, Serialize, Deserialize, PartialEq)]
#[serde(tag = "type", rename_all = "camelCase", deny_unknown_fields)]
pub enum NativeCommand {
    #[serde(rename = "secure.get")]
    SecureGet {
        version: u8,
        #[serde(rename = "profileId")]
        profile_id: String,
    },
    #[serde(rename = "secure.set")]
    SecureSet {
        version: u8,
        #[serde(rename = "profileId")]
        profile_id: String,
        capability: String,
    },
    #[serde(rename = "secure.delete")]
    SecureDelete {
        version: u8,
        #[serde(rename = "profileId")]
        profile_id: String,
    },
    #[serde(rename = "pairing.scanAndPreview")]
    PairingScanAndPreview { version: u8 },
    #[serde(rename = "permission.request")]
    PermissionRequest { version: u8, kind: String },
    #[serde(rename = "speech.start")]
    SpeechStart { version: u8 },
    #[serde(rename = "speech.stop")]
    SpeechStop { version: u8 },
    #[serde(rename = "synthesis.speak")]
    SynthesisSpeak { version: u8, text: String },
    #[serde(rename = "synthesis.stop")]
    SynthesisStop { version: u8 },
    #[serde(rename = "haptic.perform")]
    HapticPerform { version: u8, kind: String },
    #[serde(rename = "contentSize.get")]
    ContentSizeGet { version: u8 },
}

// ---------------------------------------------------------------------------
// NativeResponse
// ---------------------------------------------------------------------------

#[derive(Debug, Clone, Serialize, Deserialize, PartialEq)]
#[serde(tag = "type", rename_all = "camelCase", deny_unknown_fields)]
pub enum NativeResponse {
    #[serde(rename = "secure.state")]
    SecureState { version: u8, present: bool },
    #[serde(rename = "secure.updated")]
    SecureUpdated { version: u8, stored: bool },
    #[serde(rename = "secure.deleted")]
    SecureDeleted { version: u8, deleted: bool },
    #[serde(rename = "pairing.preview")]
    PairingPreview {
        version: u8,
        #[serde(rename = "previewId")]
        preview_id: String,
        origin: String,
    },
    #[serde(rename = "permission.status")]
    PermissionStatus {
        version: u8,
        kind: String,
        granted: bool,
    },
    #[serde(rename = "speech.ready")]
    SpeechReady { version: u8 },
    #[serde(rename = "speech.stopped")]
    SpeechStopped { version: u8 },
    #[serde(rename = "synthesis.started")]
    SynthesisStarted { version: u8 },
    #[serde(rename = "synthesis.stopped")]
    SynthesisStopped { version: u8 },
    #[serde(rename = "haptic.completed")]
    HapticCompleted { version: u8 },
    #[serde(rename = "contentSize.value")]
    ContentSizeValue { version: u8, category: String },
    #[serde(rename = "error")]
    Error { version: u8, error: NativeError },
}

// ---------------------------------------------------------------------------
// NativeEvent
// ---------------------------------------------------------------------------

#[derive(Debug, Clone, Serialize, Deserialize, PartialEq)]
#[serde(tag = "type", rename_all = "camelCase", deny_unknown_fields)]
pub enum NativeEvent {
    #[serde(rename = "lifecycle.changed")]
    LifecycleChanged { version: u8, state: String },
    #[serde(rename = "speech.partial")]
    SpeechPartial { version: u8, text: String },
    #[serde(rename = "speech.final")]
    SpeechFinal { version: u8, text: String },
    #[serde(rename = "barge.in")]
    BargeIn { version: u8 },
}

// ---------------------------------------------------------------------------
// Contract fixture
// ---------------------------------------------------------------------------

#[derive(Debug, Deserialize)]
#[serde(rename_all = "camelCase")]
pub struct ContractFixture {
    pub bridge_version: u8,
    pub commands: Vec<Value>,
    pub responses: Vec<Value>,
    pub events: Vec<Value>,
}

// ---------------------------------------------------------------------------
// Decode error
// ---------------------------------------------------------------------------

#[derive(Debug, thiserror::Error)]
pub enum DecodeError {
    #[error(transparent)]
    Json(#[from] serde_json::Error),
    #[error("unsupported bridge version: {0}")]
    UnsupportedVersion(u8),
}

// ---------------------------------------------------------------------------
// Decoders — fail closed on unknown version, type, or field
// ---------------------------------------------------------------------------

fn check_version(value: &Value) -> Result<u8, DecodeError> {
    let v = value
        .get("version")
        .ok_or(DecodeError::UnsupportedVersion(0))?;
    let n = v.as_u64().ok_or(DecodeError::UnsupportedVersion(0))?;
    if n != NATIVE_BRIDGE_VERSION as u64 {
        return Err(DecodeError::UnsupportedVersion(n as u8));
    }
    Ok(n as u8)
}

pub fn decode_command(value: Value) -> Result<NativeCommand, DecodeError> {
    check_version(&value)?;
    serde_json::from_value::<NativeCommand>(value).map_err(DecodeError::from)
}

pub fn decode_response(value: Value) -> Result<NativeResponse, DecodeError> {
    check_version(&value)?;
    serde_json::from_value::<NativeResponse>(value).map_err(DecodeError::from)
}

pub fn decode_event(value: Value) -> Result<NativeEvent, DecodeError> {
    check_version(&value)?;
    serde_json::from_value::<NativeEvent>(value).map_err(DecodeError::from)
}

// ---------------------------------------------------------------------------
// Pairing preview (the only data that crosses to JavaScript)
// ---------------------------------------------------------------------------

#[derive(Debug, Clone, Serialize, Deserialize, PartialEq)]
#[serde(rename_all = "camelCase")]
pub struct PairingPreview {
    pub preview_id: String,
    pub origin: String,
}

// ---------------------------------------------------------------------------
// SensitiveScannedCode — raw QR text stays Swift→Rust only, never Debug
// ---------------------------------------------------------------------------

pub struct SensitiveScannedCode(String);

impl SensitiveScannedCode {
    pub fn new(raw: impl Into<String>) -> Self {
        Self(raw.into())
    }

    pub fn as_str(&self) -> &str {
        &self.0
    }
}

impl fmt::Debug for SensitiveScannedCode {
    fn fmt(&self, f: &mut fmt::Formatter<'_>) -> fmt::Result {
        f.write_str("SensitiveScannedCode([REDACTED])")
    }
}

impl<'de> Deserialize<'de> for SensitiveScannedCode {
    fn deserialize<D>(deserializer: D) -> Result<Self, D::Error>
    where
        D: serde::Deserializer<'de>,
    {
        let s = String::deserialize(deserializer)?;
        Ok(Self(s))
    }
}

// ---------------------------------------------------------------------------
// Secure store request/response DTOs for the app Keychain bridge
// ---------------------------------------------------------------------------

#[derive(Debug, Clone, Deserialize, Serialize)]
#[serde(rename_all = "camelCase")]
pub struct SecureGetRequest {
    pub profile_id: String,
}

#[derive(Debug, Clone, Deserialize, Serialize)]
#[serde(rename_all = "camelCase")]
pub struct SecureGetResponse {
    pub present: bool,
}

#[derive(Debug, Clone, Deserialize, Serialize)]
#[serde(rename_all = "camelCase")]
pub struct SecureSetRequest {
    pub profile_id: String,
    pub capability: String,
}

#[derive(Debug, Clone, Deserialize, Serialize)]
#[serde(rename_all = "camelCase")]
pub struct SecureSetResponse {
    pub stored: bool,
}

#[derive(Debug, Clone, Deserialize, Serialize)]
#[serde(rename_all = "camelCase")]
pub struct SecureDeleteRequest {
    pub profile_id: String,
}

#[derive(Debug, Clone, Deserialize, Serialize)]
#[serde(rename_all = "camelCase")]
pub struct SecureDeleteResponse {
    pub deleted: bool,
}

// ---------------------------------------------------------------------------
// Preview handler — injected so tests can substitute
// ---------------------------------------------------------------------------

pub trait PreviewHandler: Send + Sync {
    fn preview(&self, scanned: &str) -> Result<PairingPreview, NativeError>;
}

// ---------------------------------------------------------------------------
// PreviewCoordinator — holds the handler, redacts scanned text from output
// ---------------------------------------------------------------------------

pub struct PreviewCoordinator {
    handler: Arc<dyn PreviewHandler>,
}

impl PreviewCoordinator {
    pub fn new(handler: Arc<dyn PreviewHandler>) -> Self {
        Self { handler }
    }

    pub fn preview_scanned(
        &self,
        scanned: SensitiveScannedCode,
    ) -> Result<PairingPreview, NativeError> {
        self.handler.preview(scanned.as_str())
    }

    /// Returns a reference to the handler so the app can delegate preview
    /// calls directly (e.g. from paste pairing).
    pub fn handler(&self) -> &Arc<dyn PreviewHandler> {
        &self.handler
    }
}
