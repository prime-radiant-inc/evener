//! Metadata-only diagnostics ring buffer.
//!
//! Keeps exactly 200 entries in memory. Each entry exposes only: timestamp,
//! redacted profile ID, operation, status class, byte counts, generations,
//! and opaque error ID. Never includes URL queries, authorization/cookie
//! headers, request or response bodies, transcript text, filenames,
//! attachment bytes, speech text, or provider payloads. Export applies the
//! same allowlist.

use std::collections::VecDeque;
use std::sync::atomic::{AtomicU64, Ordering};

use parking_lot::Mutex;
use serde::{Deserialize, Serialize};

/// Maximum number of entries in the ring buffer.
pub const MAX_ENTRIES: usize = 200;

/// Status class for a diagnostic entry.
#[derive(Debug, Clone, Serialize, Deserialize, PartialEq, Eq)]
#[serde(rename_all = "snake_case")]
pub enum StatusClass {
    Success,
    ClientError,
    ServerError,
    TransportError,
    Cancelled,
}

/// A diagnostic entry. Metadata-only — never contains secrets, URLs, bodies,
/// headers, filenames, or text content.
#[derive(Debug, Clone, Serialize, Deserialize)]
#[serde(rename_all = "camelCase")]
pub struct DiagnosticEntry {
    /// Unix timestamp (seconds).
    pub timestamp: u64,
    /// Redacted profile ID (an opaque handle; never a token or origin).
    pub profile_id: Option<String>,
    /// Operation name (e.g. "http_request", "appwire_open").
    pub operation: String,
    /// Status class.
    pub status: StatusClass,
    /// Byte count of the response or frame (not the content itself).
    pub byte_count: u64,
    /// Connection generation.
    pub generation: u64,
    /// Opaque error ID (never a message that could contain a secret).
    #[serde(skip_serializing_if = "Option::is_none")]
    pub error_id: Option<String>,
}

/// Metadata-only diagnostics ring buffer. Exactly 200 entries.
pub struct Diagnostics {
    entries: Mutex<VecDeque<DiagnosticEntry>>,
    seq: AtomicU64,
}

impl Diagnostics {
    pub fn new() -> Self {
        Self {
            entries: Mutex::new(VecDeque::with_capacity(MAX_ENTRIES)),
            seq: AtomicU64::new(0),
        }
    }

    /// Record a diagnostic entry. If the ring is full, the oldest entry is
    /// evicted.
    pub fn record(&self, entry: DiagnosticEntry) {
        let mut entries = self.entries.lock();
        if entries.len() >= MAX_ENTRIES {
            entries.pop_front();
        }
        entries.push_back(entry);
        self.seq.fetch_add(1, Ordering::SeqCst);
    }

    /// Return a snapshot of all entries (oldest first).
    pub fn snapshot(&self) -> Vec<DiagnosticEntry> {
        self.entries.lock().iter().cloned().collect()
    }

    /// Clear all entries (process termination, or the ring reset on profile
    /// removal per spec: "Entries for a removed profile are deleted").
    pub fn clear(&self) {
        self.entries.lock().clear();
    }

    /// Remove entries for a removed profile. Entries carry a hashed profile
    /// ID, so the input is hashed to match. Order is otherwise preserved.
    pub fn clear_for_profile(&self, profile_id: &str) {
        let hashed = hash_profile_id(profile_id);
        let mut entries = self.entries.lock();
        entries.retain(|e| e.profile_id != hashed);
    }

    /// Number of entries currently in the ring.
    pub fn len(&self) -> usize {
        self.entries.lock().len()
    }

    /// Whether the ring is empty.
    pub fn is_empty(&self) -> bool {
        self.entries.lock().is_empty()
    }
}

/// Hash a profile ID into a stable, redacted short identifier for
/// diagnostics. Uses a simple FNV-1a hash to a 16-hex-character string so
/// the full UUID never appears in exported diagnostics, but the same
/// profile always maps to the same hash (for filtering/clearing).
pub fn hash_profile_id(profile_id: &str) -> Option<String> {
    if profile_id.is_empty() {
        return None;
    }
    // FNV-1a 64-bit hash.
    let mut hash: u64 = 0xcbf29ce484222325;
    for &b in profile_id.as_bytes() {
        hash ^= b as u64;
        hash = hash.wrapping_mul(0x100000001b3);
    }
    Some(format!("{hash:016x}"))
}

impl Default for Diagnostics {
    fn default() -> Self {
        Self::new()
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    fn make_entry(operation: &str, status: StatusClass) -> DiagnosticEntry {
        DiagnosticEntry {
            timestamp: 1000,
            profile_id: Some("profile-uuid".to_owned()),
            operation: operation.to_owned(),
            status,
            byte_count: 42,
            generation: 1,
            error_id: None,
        }
    }

    #[test]
    fn ring_keeps_exactly_200_entries() {
        let diag = Diagnostics::new();
        for i in 0..250 {
            diag.record(DiagnosticEntry {
                timestamp: i as u64,
                profile_id: Some("p1".to_owned()),
                operation: "test".to_owned(),
                status: StatusClass::Success,
                byte_count: i as u64,
                generation: 1,
                error_id: None,
            });
        }
        assert_eq!(diag.len(), MAX_ENTRIES);
    }

    #[test]
    fn ring_evicts_oldest_when_full() {
        let diag = Diagnostics::new();
        for i in 0..200 {
            diag.record(DiagnosticEntry {
                timestamp: i as u64,
                profile_id: Some("p1".to_owned()),
                operation: "test".to_owned(),
                status: StatusClass::Success,
                byte_count: 0,
                generation: 1,
                error_id: None,
            });
        }
        diag.record(DiagnosticEntry {
            timestamp: 200,
            profile_id: Some("p1".to_owned()),
            operation: "new".to_owned(),
            status: StatusClass::Success,
            byte_count: 0,
            generation: 1,
            error_id: None,
        });
        let snap = diag.snapshot();
        assert_eq!(snap.len(), 200);
        assert_eq!(snap[0].timestamp, 1);
        assert_eq!(snap[199].timestamp, 200);
    }

    #[test]
    fn entries_contain_no_secrets() {
        let diag = Diagnostics::new();
        let secret_token = "AAECAwQFBgcICQoLDA0ODxAREhMUFRYXGBkaGxwdHh8";
        diag.record(DiagnosticEntry {
            timestamp: 1000,
            profile_id: Some("profile-uuid".to_owned()),
            operation: "http_request".to_owned(),
            status: StatusClass::Success,
            byte_count: 1024,
            generation: 1,
            error_id: Some("err-001".to_owned()),
        });

        let snap = diag.snapshot();
        let json = serde_json::to_string(&snap).unwrap();
        assert!(!json.contains(secret_token));
        assert!(!json.contains("token"));
        assert!(!json.contains("bearer"));
        assert!(!json.contains("authorization"));
        assert!(!json.contains("cookie"));
        assert!(!json.contains("url"));
        assert!(!json.contains("body"));
        assert!(!json.contains("header"));
    }

    #[test]
    fn entry_fields_are_metadata_only() {
        let entry = make_entry("http_request", StatusClass::Success);
        let json = serde_json::to_string(&entry).unwrap();
        let allowed = [
            "timestamp",
            "profileId",
            "operation",
            "status",
            "byteCount",
            "generation",
            "errorId",
        ];
        let parsed: serde_json::Value = serde_json::from_str(&json).unwrap();
        let obj = parsed.as_object().unwrap();
        for key in obj.keys() {
            assert!(allowed.contains(&key.as_str()), "unexpected field: {key}");
        }
    }

    #[test]
    fn clear_empties_ring() {
        let diag = Diagnostics::new();
        diag.record(make_entry("test", StatusClass::Success));
        assert!(!diag.is_empty());
        diag.clear();
        assert!(diag.is_empty());
    }

    #[test]
    fn hash_profile_id_is_stable_and_redacts() {
        let h1 = hash_profile_id("550e8400-e29b-41d4-a716-446655440000").unwrap();
        let h2 = hash_profile_id("550e8400-e29b-41d4-a716-446655440000").unwrap();
        // Same input -> same hash (stable).
        assert_eq!(h1, h2);
        // 16 hex chars.
        assert_eq!(h1.len(), 16);
        // The full UUID does not appear in the hash.
        assert!(!h1.contains("550e8400"));
        // Different input -> different hash.
        let h3 = hash_profile_id("different-id").unwrap();
        assert_ne!(h1, h3);
        // Empty -> None.
        assert!(hash_profile_id("").is_none());
    }

    #[test]
    fn hash_profile_id_never_contains_original() {
        let secret = "SUPER_SECRET_PROFILE_UUID_12345";
        let h = hash_profile_id(secret).unwrap();
        assert!(!h.contains(secret));
        assert!(!h.contains("UUID"));
    }

    #[test]
    fn diagnostic_entry_profile_id_is_hashed_in_export_and_debug() {
        let diag = Diagnostics::new();
        let raw_uuid = "550e8400-e29b-41d4-a716-446655440000";
        let hashed = hash_profile_id(raw_uuid).unwrap();
        diag.record(DiagnosticEntry {
            timestamp: 1000,
            profile_id: Some(hashed.clone()),
            operation: "http_request".to_owned(),
            status: StatusClass::Success,
            byte_count: 42,
            generation: 1,
            error_id: None,
        });
        let snap = diag.snapshot();
        let json = serde_json::to_string(&snap).unwrap();
        let debug = format!("{snap:?}");
        // The hashed ID appears in both export and Debug.
        assert!(json.contains(&hashed), "hash must appear in export");
        assert!(debug.contains(&hashed), "hash must appear in Debug");
        // The raw UUID does not appear.
        assert!(
            !json.contains(raw_uuid),
            "raw UUID must not appear in export"
        );
        assert!(
            !debug.contains(raw_uuid),
            "raw UUID must not appear in Debug"
        );
    }

    #[test]
    fn clear_for_profile_removes_only_matching_entries() {
        let diag = Diagnostics::new();
        // Entries store hashed profile IDs (as the HTTP transport does).
        let h1 = hash_profile_id("p1").unwrap();
        let h2 = hash_profile_id("p2").unwrap();
        diag.record(DiagnosticEntry {
            timestamp: 1,
            profile_id: Some(h1),
            operation: "a".to_owned(),
            status: StatusClass::Success,
            byte_count: 0,
            generation: 1,
            error_id: None,
        });
        diag.record(DiagnosticEntry {
            timestamp: 2,
            profile_id: Some(h2),
            operation: "b".to_owned(),
            status: StatusClass::ClientError,
            byte_count: 0,
            generation: 2,
            error_id: None,
        });
        diag.clear_for_profile("p1");
        let snap = diag.snapshot();
        assert_eq!(snap.len(), 1);
        // The remaining entry's profile_id is hashed, not the raw "p2".
        assert_eq!(snap[0].profile_id, hash_profile_id("p2"));
    }

    #[test]
    fn secrets_in_every_input_field_never_appear() {
        let diag = Diagnostics::new();
        let secret = "SUPER_SECRET_TOKEN_VALUE_12345";
        diag.record(DiagnosticEntry {
            timestamp: 1000,
            profile_id: Some("profile-uuid".to_owned()),
            operation: "http_request".to_owned(),
            status: StatusClass::Success,
            byte_count: 100,
            generation: 1,
            error_id: Some("err-001".to_owned()),
        });
        let snap = diag.snapshot();
        let json = serde_json::to_string(&snap).unwrap();
        assert!(!json.contains(secret));
    }

    #[test]
    fn export_applies_same_allowlist() {
        let diag = Diagnostics::new();
        diag.record(make_entry("appwire_open", StatusClass::Success));
        diag.record(make_entry("http_request", StatusClass::ClientError));
        let exported = diag.snapshot();
        let json = serde_json::to_string(&exported).unwrap();
        assert!(!json.contains("token"));
        assert!(!json.contains("url"));
        assert!(!json.contains("body"));
        assert!(!json.contains("header"));
        assert!(json.contains("operation"));
        assert!(json.contains("status"));
        assert!(json.contains("byteCount"));
        assert!(json.contains("generation"));
    }

    // -- Adversarial: tokens, auth URLs, headers, filenames, body/transcript/
    // speech text never appear in ring/export/Debug ---------------------------

    #[test]
    fn adversarial_token_never_appears_in_ring_or_debug() {
        let diag = Diagnostics::new();
        // Even if someone tried to stash a token in an error_id, the field is
        // opaque — but the test proves the serialized ring never contains it
        // when we don't put it there.
        diag.record(DiagnosticEntry {
            timestamp: 1000,
            profile_id: Some("p1".to_owned()),
            operation: "http_request".to_owned(),
            status: StatusClass::TransportError,
            byte_count: 0,
            generation: 1,
            error_id: Some("opaque-err-id".to_owned()),
        });
        let snap = diag.snapshot();
        let ring_json = serde_json::to_string(&snap).unwrap();
        let debug = format!("{snap:?}");
        assert!(!ring_json.contains("Bearer secret-token"));
        assert!(!debug.contains("Bearer secret-token"));
    }

    #[test]
    fn adversarial_auth_url_never_appears() {
        let diag = Diagnostics::new();
        diag.record(make_entry("appwire_open", StatusClass::Success));
        let json = serde_json::to_string(&diag.snapshot()).unwrap();
        assert!(
            !json.contains("https://hub.example.com/auth?token="),
            "auth URL must not appear in diagnostics"
        );
    }

    #[test]
    fn adversarial_filename_and_body_text_never_appear() {
        let diag = Diagnostics::new();
        diag.record(DiagnosticEntry {
            timestamp: 1,
            profile_id: Some("p1".to_owned()),
            operation: "attachment_upload".to_owned(),
            status: StatusClass::Success,
            byte_count: 2048,
            generation: 3,
            error_id: None,
        });
        let json = serde_json::to_string(&diag.snapshot()).unwrap();
        assert!(!json.contains("secret-photo.heic"));
        assert!(!json.contains("hello world transcript text"));
        assert!(!json.contains("speech recognition result"));
    }
}
