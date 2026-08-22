//! Managed transport state: owns the shared `NetworkPolicy`, `Diagnostics`,
//! `AppwireManager`, and the active `HubHttp` factory. One instance is managed
//! by Tauri and shared by all HTTP/AppWire commands.
//!
//! The async lifecycle mutex is held only around the brief state transition
//! (looking up the active profile origin/generation and retrieving the
//! token) — never across a network await. HTTP and AppWire network calls run
//! outside the mutex.

use std::sync::Arc;

use crate::appwire_transport::AppwireManager;
use crate::diagnostics::Diagnostics;
use crate::error::ReleaseMode;
use crate::http_transport::{HttpRequestRegistry, HubHttp};
use crate::network_policy::NetworkPolicy;
use crate::profile::SecureStore;

/// Managed transport state. Owns the shared policy, diagnostics, appwire
/// manager, and the secure store bridge (for token retrieval). Constructed
/// once in `lib.rs` and registered as Tauri managed state.
pub struct TransportState {
    /// Shared network policy (re-resolves on every connection).
    policy: Arc<NetworkPolicy>,
    /// Release mode for address validation.
    mode: ReleaseMode,
    /// Shared diagnostics ring (200 metadata-only entries).
    diagnostics: Arc<Diagnostics>,
    /// One AppWire socket manager for the active profile.
    appwire: Arc<AppwireManager>,
    /// Secure store bridge: retrieves the active profile's token from the
    /// native Keychain adapter. Never exposes the token to JavaScript.
    secure: Arc<dyn SecureStore>,
    /// Prepared raw bodies and live HTTP cancellation handles.
    http_requests: HttpRequestRegistry,
}

impl TransportState {
    pub fn new(
        policy: Arc<NetworkPolicy>,
        mode: ReleaseMode,
        diagnostics: Arc<Diagnostics>,
        appwire: Arc<AppwireManager>,
        secure: Arc<dyn SecureStore>,
    ) -> Self {
        Self {
            policy,
            mode,
            diagnostics,
            appwire,
            secure,
            http_requests: HttpRequestRegistry::new(),
        }
    }

    /// Borrow the shared diagnostics ring.
    pub fn diagnostics(&self) -> &Diagnostics {
        &self.diagnostics
    }

    /// Borrow the AppWire manager.
    pub fn appwire(&self) -> &AppwireManager {
        &self.appwire
    }

    /// Borrow the HTTP request/cancellation registry.
    pub fn http_requests(&self) -> &HttpRequestRegistry {
        &self.http_requests
    }

    /// Clear diagnostics entries for a removed profile. Called by the
    /// `profile_remove` command after a successful removal so that a
    /// removed profile's diagnostic history is deleted per spec.
    pub fn clear_profile_diagnostics(&self, profile_id: &str) {
        self.diagnostics.clear_for_profile(profile_id);
    }

    /// Build a HubHttp for one immutable active snapshot. Each request creates
    /// a resolver-pinned client because the approved address set is refreshed
    /// on every connection boundary; diagnostics remain shared.
    pub fn make_http(&self, origin: String, token: String) -> HubHttp {
        HubHttp::with_diagnostics(
            origin,
            token,
            self.policy.clone(),
            self.mode,
            self.diagnostics.clone(),
        )
    }

    /// Retrieve the active profile's token from the native Keychain adapter.
    /// Called outside the lifecycle mutex.
    pub fn get_token(
        &self,
        profile_id: &str,
    ) -> Result<Option<String>, crate::error::ProfileError> {
        self.secure.get(profile_id)
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use crate::diagnostics::{DiagnosticEntry, StatusClass};

    /// A removed profile's diagnostic entries must be deleted; other
    /// profiles' entries are preserved. This is the contract the
    /// `profile_remove` command relies on.
    #[tokio::test]
    async fn clear_profile_diagnostics_removes_only_matching_entries() {
        let policy = Arc::new(NetworkPolicy::new(Box::new(
            crate::profile_runtime::SystemDnsResolver,
        )));
        let diagnostics = Arc::new(Diagnostics::new());
        let appwire = Arc::new(AppwireManager::new(policy.clone(), ReleaseMode::Release));
        let secure: Arc<dyn crate::profile::SecureStore> = Arc::new(
            crate::profile_runtime::KeychainBridge::new(Arc::new(TestNoopSecureStore)),
        );
        let transport = TransportState::new(
            policy,
            ReleaseMode::Release,
            diagnostics.clone(),
            appwire,
            secure,
        );

        // Entries store hashed profile IDs (as the HTTP transport does).
        let h_removed = crate::diagnostics::hash_profile_id("p-removed").unwrap();
        let h_kept = crate::diagnostics::hash_profile_id("p-kept").unwrap();
        diagnostics.record(DiagnosticEntry {
            timestamp: 1,
            profile_id: Some(h_removed),
            operation: "http_request".to_owned(),
            status: StatusClass::Success,
            byte_count: 0,
            generation: 1,
            error_id: None,
        });
        diagnostics.record(DiagnosticEntry {
            timestamp: 2,
            profile_id: Some(h_kept),
            operation: "http_request".to_owned(),
            status: StatusClass::Success,
            byte_count: 0,
            generation: 1,
            error_id: None,
        });

        transport.clear_profile_diagnostics("p-removed");

        let snap = transport.diagnostics().snapshot();
        assert_eq!(snap.len(), 1);
        assert_eq!(
            snap[0].profile_id,
            crate::diagnostics::hash_profile_id("p-kept")
        );
    }

    /// A no-op secure store for TransportState construction in tests.
    struct TestNoopSecureStore;

    impl crate::profile::SecureStore for TestNoopSecureStore {
        fn get(&self, _profile_id: &str) -> Result<Option<String>, crate::error::ProfileError> {
            Ok(None)
        }
        fn set(&self, _profile_id: &str, _token: &str) -> Result<(), crate::error::ProfileError> {
            Ok(())
        }
        fn delete(&self, _profile_id: &str) -> Result<(), crate::error::ProfileError> {
            Ok(())
        }
    }
}
