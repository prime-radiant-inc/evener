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
use crate::http_transport::HubHttp;
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
    /// The shared reqwest client (redirects disabled). Reused across
    /// requests; per-request origin/token come from the active profile.
    http_client: reqwest::Client,
}

impl TransportState {
    pub fn new(
        policy: Arc<NetworkPolicy>,
        mode: ReleaseMode,
        diagnostics: Arc<Diagnostics>,
        appwire: Arc<AppwireManager>,
        secure: Arc<dyn SecureStore>,
    ) -> Self {
        let http_client = reqwest::Client::builder()
            .redirect(reqwest::redirect::Policy::none())
            .build()
            .expect("failed to build reqwest client");
        Self {
            policy,
            mode,
            diagnostics,
            appwire,
            secure,
            http_client,
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

    /// Clear diagnostics entries for a removed profile. Called by the
    /// `profile_remove` command after a successful removal so that a
    /// removed profile's diagnostic history is deleted per spec.
    pub fn clear_profile_diagnostics(&self, profile_id: &str) {
        self.diagnostics.clear_for_profile(profile_id);
    }

    /// Build (or rebuild) a HubHttp for the active profile using the shared
    /// reqwest client and diagnostics. Called by the HTTP command after the
    /// lifecycle mutex has released the origin and token.
    pub fn make_http(&self, origin: String, token: String) -> HubHttp {
        HubHttp::with_client(
            origin,
            token,
            self.policy.clone(),
            self.mode,
            self.http_client.clone(),
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

/// CloseTransport callback that closes the AppWire manager's current
/// connection before the active profile changes. Registered as the
/// ProfileStore's close callback in `lib.rs`.
pub struct AppwireCloseTransport {
    appwire: Arc<AppwireManager>,
}

impl AppwireCloseTransport {
    pub fn new(appwire: Arc<AppwireManager>) -> Self {
        Self { appwire }
    }
}

impl crate::profile::CloseTransport for AppwireCloseTransport {
    fn close_current(&self) -> crate::profile::ProfileGeneration {
        let gen = self.appwire.close_current();
        crate::profile::ProfileGeneration(gen)
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
        let appwire = Arc::new(AppwireManager::new());
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

        diagnostics.record(DiagnosticEntry {
            timestamp: 1,
            profile_id: Some("p-removed".to_owned()),
            operation: "http_request".to_owned(),
            status: StatusClass::Success,
            byte_count: 0,
            generation: 1,
            error_id: None,
        });
        diagnostics.record(DiagnosticEntry {
            timestamp: 2,
            profile_id: Some("p-kept".to_owned()),
            operation: "http_request".to_owned(),
            status: StatusClass::Success,
            byte_count: 0,
            generation: 1,
            error_id: None,
        });

        transport.clear_profile_diagnostics("p-removed");

        let snap = transport.diagnostics().snapshot();
        assert_eq!(snap.len(), 1);
        assert_eq!(snap[0].profile_id.as_deref(), Some("p-kept"));
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
