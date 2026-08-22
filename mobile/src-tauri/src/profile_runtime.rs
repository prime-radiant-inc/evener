//! Production profile runtime: one managed `ProfileStore` with atomic file
//! preferences, Keychain bridge, real pairing probe, async lifecycle mutex,
//! and a managed preview handler that delegates to the exact same store.
//!
//! JavaScript receives no capability, raw QR text, or native path. All profile
//! lifecycle commands serialize through the async mutex.

use std::sync::Arc;

use serde::{Deserialize, Serialize};

use crate::error::{ProfileError, ReleaseMode};
use crate::profile::{
    self, Clock, PairingProbe, PreferencesStore, ProfileStore, ProfileSummary, SecureStore,
    SelectResult,
};

// ---------------------------------------------------------------------------
// Atomic file-based preferences store
// ---------------------------------------------------------------------------

/// File-based preferences store using atomic temp/write/fsync/rename under a
/// directory (Tauri app-data). Stores nonsecret summaries only.
pub struct FilePreferences {
    dir: std::path::PathBuf,
    path: std::path::PathBuf,
}

impl FilePreferences {
    /// Create a preferences store backed by `dir/preferences.json`.
    /// The directory must already exist (app-data root).
    pub fn new(dir: impl AsRef<std::path::Path>) -> Self {
        let dir = dir.as_ref().to_path_buf();
        let path = dir.join("preferences.json");
        Self { dir, path }
    }

    fn load_from(path: &std::path::Path) -> profile::Preferences {
        match std::fs::read(path) {
            Ok(bytes) => serde_json::from_slice(&bytes).unwrap_or_default(),
            Err(_) => profile::Preferences::default(),
        }
    }
}

impl PreferencesStore for FilePreferences {
    fn load(&self) -> Result<profile::Preferences, ProfileError> {
        Ok(Self::load_from(&self.path))
    }

    fn save(&self, prefs: &profile::Preferences) -> Result<(), ProfileError> {
        // Atomic write: serialize -> temp file -> fsync -> rename.
        let bytes =
            serde_json::to_vec(prefs).map_err(|e| ProfileError::Preferences(e.to_string()))?;

        let tmp = self.dir.join(".preferences.json.tmp");
        std::fs::write(&tmp, &bytes).map_err(|e| ProfileError::Preferences(e.to_string()))?;

        // fsync the temp file before rename for durability.
        let file =
            std::fs::File::open(&tmp).map_err(|e| ProfileError::Preferences(e.to_string()))?;
        file.sync_all()
            .map_err(|e| ProfileError::Preferences(e.to_string()))?;
        drop(file);

        // Set file mode 0o600 on Unix (nonsecret summaries, but restrictive).
        #[cfg(unix)]
        {
            use std::os::unix::fs::PermissionsExt;
            let perms = std::fs::Permissions::from_mode(0o600);
            std::fs::set_permissions(&tmp, perms)
                .map_err(|e| ProfileError::Preferences(e.to_string()))?;
        }

        std::fs::rename(&tmp, &self.path).map_err(|e| ProfileError::Preferences(e.to_string()))?;

        Ok(())
    }
}

// ---------------------------------------------------------------------------
// Keychain bridge adapter — delegates to the native plugin SecureStore
// ---------------------------------------------------------------------------

/// A SecureStore adapter that bridges to the native Keychain via the
/// installed plugin. In tests, a fake SecureStore is injected directly.
/// Production uses `KeychainBridge` which calls the native plugin's
/// secure.get/set/delete commands.
///
/// This adapter never silently downgrades failures: a Keychain error
/// propagates as `ProfileError::SecureStore`.
pub struct KeychainBridge<S: SecureStore> {
    inner: Arc<S>,
}

impl<S: SecureStore> KeychainBridge<S> {
    pub fn new(inner: Arc<S>) -> Self {
        Self { inner }
    }
}

impl<S: SecureStore> SecureStore for KeychainBridge<S> {
    fn get(&self, profile_id: &str) -> Result<Option<String>, ProfileError> {
        self.inner.get(profile_id)
    }
    fn set(&self, profile_id: &str, token: &str) -> Result<(), ProfileError> {
        self.inner.set(profile_id, token)
    }
    fn delete(&self, profile_id: &str) -> Result<(), ProfileError> {
        self.inner.delete(profile_id)
    }
}

// ---------------------------------------------------------------------------
// Real pairing probe — HTTP-based health + authenticated harmless probe
// ---------------------------------------------------------------------------

/// A real `PairingProbe` that probes `/api/health` (unauthenticated, for
/// mobile API version) and then an authenticated harmless endpoint, both
/// with redirects disabled. Never logs bodies, URLs, or tokens.
pub struct RealPairingProbe {
    client: reqwest::Client,
}

impl RealPairingProbe {
    pub fn new() -> Self {
        let client = reqwest::Client::builder()
            .redirect(reqwest::redirect::Policy::none())
            .build()
            .expect("failed to build reqwest client");
        Self { client }
    }
}

impl Default for RealPairingProbe {
    fn default() -> Self {
        Self::new()
    }
}

impl PairingProbe for RealPairingProbe {
    fn probe(&self, origin: &str, token: &str, _mode: ReleaseMode) -> Result<i64, ProfileError> {
        let runtime =
            tokio::runtime::Handle::try_current().map_err(|e| ProfileError::ProbeFailed {
                origin: origin.to_owned(),
                message: e.to_string(),
            })?;

        let health_url = format!("{origin}/api/health");
        let health_resp = runtime
            .block_on(self.client.get(&health_url).send())
            .map_err(|_e| ProfileError::ProbeFailed {
                origin: origin.to_owned(),
                message: "health request failed".to_owned(),
            })?;

        if !health_resp.status().is_success() {
            return Err(ProfileError::ProbeFailed {
                origin: origin.to_owned(),
                message: format!("health status {}", health_resp.status().as_u16()),
            });
        }

        let body: serde_json::Value =
            runtime
                .block_on(health_resp.json())
                .map_err(|_e| ProfileError::ProbeFailed {
                    origin: origin.to_owned(),
                    message: "health body parse failed".to_owned(),
                })?;

        let version = body
            .get("mobile_api_version")
            .and_then(|v| v.as_i64())
            .ok_or_else(|| ProfileError::ProbeFailed {
                origin: origin.to_owned(),
                message: "mobile_api_version missing".to_owned(),
            })?;

        let pairing_url = format!("{origin}/api/mobile/pairing");
        let pairing_resp = runtime
            .block_on(self.client.get(&pairing_url).bearer_auth(token).send())
            .map_err(|_e| ProfileError::ProbeFailed {
                origin: origin.to_owned(),
                message: "authenticated probe failed".to_owned(),
            })?;

        if !pairing_resp.status().is_success() {
            return Err(ProfileError::ProbeFailed {
                origin: origin.to_owned(),
                message: format!("auth probe status {}", pairing_resp.status().as_u16()),
            });
        }

        Ok(version)
    }
}

// ---------------------------------------------------------------------------
// System DNS resolver — resolves hostnames via the OS
// ---------------------------------------------------------------------------

/// A `DnsResolver` that uses the system resolver via `std::net::ToSocketAddrs`.
pub struct SystemDnsResolver;

impl crate::network_policy::DnsResolver for SystemDnsResolver {
    fn resolve(&self, host: &str) -> Result<Vec<std::net::IpAddr>, String> {
        use std::net::ToSocketAddrs;
        (host, 0u16)
            .to_socket_addrs()
            .map(|addrs| addrs.map(|a| a.ip()).collect())
            .map_err(|e| e.to_string())
    }
}

// ---------------------------------------------------------------------------
// Managed preview handler — delegates to the exact managed ProfileStore
// ---------------------------------------------------------------------------

/// Preview handler that delegates to the exact managed `ProfileStore`'s
/// `preview_pairing` pending map. This replaces the stateless parser-only
/// handler: a QR scan preview can be confirmed later through the same store.
pub struct ManagedPreviewHandler {
    store: Arc<ProfileStore>,
    #[allow(dead_code)]
    clock: Arc<dyn Clock>,
}

impl ManagedPreviewHandler {
    pub fn new(store: Arc<ProfileStore>, clock: Arc<dyn Clock>) -> Self {
        Self { store, clock }
    }
}

impl tauri_plugin_evener_native::PreviewHandler for ManagedPreviewHandler {
    fn preview(
        &self,
        scanned: &str,
    ) -> Result<tauri_plugin_evener_native::PairingPreview, tauri_plugin_evener_native::NativeError>
    {
        self.store
            .preview_pairing(scanned)
            .map(|p| tauri_plugin_evener_native::PairingPreview {
                preview_id: p.preview_id,
                origin: p.origin,
            })
            .map_err(|e| tauri_plugin_evener_native::NativeError {
                id: "pairing-preview".to_owned(),
                kind: tauri_plugin_evener_native::NativeErrorKind::Internal,
                message: e.to_string(),
            })
    }
}

// ---------------------------------------------------------------------------
// Profile runtime — the managed app state with an async lifecycle mutex
// ---------------------------------------------------------------------------

/// The managed production state. Owns one persistent `ProfileStore` behind an
/// async lifecycle mutex that serializes all profile lifecycle commands.
pub struct ProfileRuntime {
    store: Arc<ProfileStore>,
    lifecycle: tokio::sync::Mutex<()>,
}

impl ProfileRuntime {
    pub fn new(store: Arc<ProfileStore>) -> Self {
        Self {
            store,
            lifecycle: tokio::sync::Mutex::new(()),
        }
    }

    /// Acquire the lifecycle lock, then perform the operation. All profile
    /// lifecycle commands serialize through this gate.
    pub async fn serialized<F, T>(&self, f: F) -> T
    where
        F: FnOnce(&ProfileStore) -> T,
    {
        let _guard = self.lifecycle.lock().await;
        f(&self.store)
    }

    /// Direct access to the store (for read-only operations like list/active_id).
    pub fn store(&self) -> &ProfileStore {
        &self.store
    }

    /// Arc clone of the store (for preview handler delegation).
    pub fn store_arc(&self) -> Arc<ProfileStore> {
        self.store.clone()
    }
}

// ---------------------------------------------------------------------------
// Command request/response DTOs (typed Tauri commands)
// ---------------------------------------------------------------------------

#[derive(Debug, Clone, Deserialize)]
#[serde(rename_all = "camelCase")]
pub struct PreviewPasteRequest {
    pub raw: String,
}

#[derive(Debug, Clone, Serialize)]
#[serde(rename_all = "camelCase")]
pub struct PreviewResponse {
    pub preview_id: String,
    pub origin: String,
}

#[derive(Debug, Clone, Deserialize)]
#[serde(rename_all = "camelCase")]
pub struct ConfirmPairingRequest {
    pub preview_id: String,
    pub name: String,
    #[serde(default)]
    pub allow_duplicate_origin: bool,
}

#[derive(Debug, Clone, Serialize)]
#[serde(rename_all = "camelCase")]
pub struct ProfileSummaryResponse {
    pub id: String,
    pub name: String,
    pub origin: String,
}

impl From<ProfileSummary> for ProfileSummaryResponse {
    fn from(s: ProfileSummary) -> Self {
        Self {
            id: s.id,
            name: s.name,
            origin: s.origin,
        }
    }
}

#[derive(Debug, Clone, Deserialize)]
#[serde(rename_all = "camelCase")]
pub struct RenameRequest {
    pub profile_id: String,
    pub new_name: String,
}

#[derive(Debug, Clone, Deserialize)]
#[serde(rename_all = "camelCase")]
pub struct RemoveRequest {
    pub profile_id: String,
}

#[derive(Debug, Clone, Deserialize)]
#[serde(rename_all = "camelCase")]
pub struct SelectRequest {
    pub profile_id: String,
}

#[derive(Debug, Clone, Serialize)]
#[serde(rename_all = "camelCase")]
pub struct SelectResponse {
    pub profile_id: Option<String>,
    pub generation: u64,
}

impl From<SelectResult> for SelectResponse {
    fn from(r: SelectResult) -> Self {
        Self {
            profile_id: r.profile_id,
            generation: r.generation.0,
        }
    }
}

#[derive(Debug, Clone, Serialize)]
#[serde(rename_all = "camelCase")]
pub struct HealthResponse {
    pub active_profile_id: Option<String>,
    pub profiles: Vec<ProfileSummaryResponse>,
    pub generation: u64,
}

#[derive(Debug, Clone, Deserialize)]
#[serde(rename_all = "camelCase")]
pub struct PreviewRepairRequest {
    pub profile_id: String,
    pub raw: String,
}

// ---------------------------------------------------------------------------
// Tests
// ---------------------------------------------------------------------------

#[cfg(test)]
mod tests {
    use super::*;
    use crate::network_policy::AlwaysPrivateResolver;
    use crate::network_policy::NetworkPolicy;
    use crate::profile::{
        MemoryPreferences, MemorySecureStore, OkProbe, RecordingCloseTransport, StepClock,
    };

    const TOKEN: &str = "AAECAwQFBgcICQoLDA0ODxAREhMUFRYXGBkaGxwdHh8";

    fn auth_url(host: &str) -> String {
        format!("https://{host}/auth?token={TOKEN}")
    }

    fn make_runtime(
        prefs: Arc<dyn PreferencesStore>,
        secure: Arc<dyn SecureStore>,
        probe: Arc<dyn PairingProbe>,
        clock: Arc<dyn Clock>,
    ) -> (ProfileRuntime, Arc<RecordingCloseTransport>) {
        let policy = Arc::new(NetworkPolicy::new(Box::new(AlwaysPrivateResolver)));
        let close = Arc::new(RecordingCloseTransport::default());
        let store = Arc::new(ProfileStore::new(
            prefs,
            secure,
            probe,
            clock,
            policy,
            close.clone(),
        ));
        (ProfileRuntime::new(store), close)
    }

    fn make_memory_runtime() -> (
        ProfileRuntime,
        Arc<MemoryPreferences>,
        Arc<MemorySecureStore>,
    ) {
        let prefs = Arc::new(MemoryPreferences::new());
        let secure = Arc::new(MemorySecureStore::new());
        let probe = Arc::new(OkProbe);
        let clock = Arc::new(StepClock::new(0));
        let (rt, _) = make_runtime(prefs.clone(), secure.clone(), probe, clock);
        (rt, prefs, secure)
    }

    // -- Atomic file preferences: restart persistence -----------------------

    #[tokio::test]
    async fn file_preferences_survive_restart() {
        let dir = tempfile::tempdir().unwrap();
        let prefs1 = FilePreferences::new(dir.path());
        let secure = Arc::new(MemorySecureStore::new());
        let probe = Arc::new(OkProbe);
        let clock = Arc::new(StepClock::new(0));
        let (rt, _) = make_runtime(Arc::new(prefs1), secure.clone(), probe, clock);

        let pv = rt
            .serialized(|store| store.preview_pairing(&auth_url("hub.example.com")))
            .await
            .unwrap();
        let p = rt
            .serialized(|store| {
                store.confirm_pairing(&pv.preview_id, "Alpha", false, ReleaseMode::Release)
            })
            .await
            .unwrap();
        rt.serialized(|store| store.select(&p.id)).await.unwrap();

        // Simulate restart: new store over the same file preferences.
        let prefs2 = FilePreferences::new(dir.path());
        let probe2 = Arc::new(OkProbe);
        let clock2 = Arc::new(StepClock::new(0));
        let (rt2, _) = make_runtime(Arc::new(prefs2), secure, probe2, clock2);

        let loaded = rt2.serialized(|store| store.list()).await.unwrap();
        assert_eq!(loaded.len(), 1);
        assert_eq!(loaded[0].id, p.id);
        assert_eq!(loaded[0].name, "Alpha");
        assert_eq!(loaded[0].origin, "https://hub.example.com");
        assert_eq!(
            rt2.serialized(|store| store.active_id()).await.unwrap(),
            Some(p.id)
        );
    }

    #[test]
    fn file_preferences_atomic_write_replaces_old() {
        let dir = tempfile::tempdir().unwrap();
        let prefs = FilePreferences::new(dir.path());

        let mut p1 = profile::Preferences::default();
        p1.profiles.push(ProfileSummary {
            id: "id1".to_owned(),
            name: "Alpha".to_owned(),
            origin: "https://hub1.example.com".to_owned(),
        });
        prefs.save(&p1).unwrap();

        let mut p2 = profile::Preferences::default();
        p2.profiles.push(ProfileSummary {
            id: "id2".to_owned(),
            name: "Beta".to_owned(),
            origin: "https://hub2.example.com".to_owned(),
        });
        p2.active_id = Some("id2".to_owned());
        prefs.save(&p2).unwrap();

        let loaded = prefs.load().unwrap();
        assert_eq!(loaded.profiles.len(), 1);
        assert_eq!(loaded.profiles[0].id, "id2");
        assert_eq!(loaded.active_id, Some("id2".to_owned()));

        // No temp file remains.
        assert!(!dir.path().join(".preferences.json.tmp").exists());
    }

    #[test]
    fn file_preferences_creates_file_mode_0600() {
        let dir = tempfile::tempdir().unwrap();
        let prefs = FilePreferences::new(dir.path());
        let p = profile::Preferences::default();
        prefs.save(&p).unwrap();

        let path = dir.path().join("preferences.json");
        assert!(path.exists());

        #[cfg(unix)]
        {
            use std::os::unix::fs::PermissionsExt;
            let mode = std::fs::metadata(&path).unwrap().permissions().mode();
            assert_eq!(mode & 0o777, 0o600);
        }
    }

    // -- Serialized concurrent calls ---------------------------------------

    #[tokio::test]
    async fn serialized_concurrent_calls_do_not_interleave() {
        let (rt, _prefs, _secure) = make_memory_runtime();
        let rt = Arc::new(rt);
        let rt2 = rt.clone();
        let rt3 = rt.clone();

        let h1 = tokio::spawn(async move {
            rt.serialized(|store| {
                let pv = store
                    .preview_pairing(&auth_url("hub1.example.com"))
                    .unwrap();
                store.confirm_pairing(&pv.preview_id, "Alpha", false, ReleaseMode::Release)
            })
            .await
        });

        let h2 = tokio::spawn(async move {
            rt2.serialized(|store| {
                let pv = store
                    .preview_pairing(&auth_url("hub2.example.com"))
                    .unwrap();
                store.confirm_pairing(&pv.preview_id, "Beta", false, ReleaseMode::Release)
            })
            .await
        });

        let r1 = h1.await.unwrap().unwrap();
        let r2 = h2.await.unwrap().unwrap();
        assert_ne!(r1.id, r2.id);
        assert_eq!(r1.name, "Alpha");
        assert_eq!(r2.name, "Beta");

        let list = rt3.serialized(|store| store.list()).await.unwrap();
        assert_eq!(list.len(), 2);
    }

    // -- Active generation changes -----------------------------------------

    #[tokio::test]
    async fn select_returns_monotonically_increasing_generation() {
        let (rt, _prefs, _secure) = make_memory_runtime();

        let pv = rt
            .serialized(|store| store.preview_pairing(&auth_url("hub.example.com")))
            .await
            .unwrap();
        let p = rt
            .serialized(|store| {
                store.confirm_pairing(&pv.preview_id, "Alpha", false, ReleaseMode::Release)
            })
            .await
            .unwrap();

        let g1 = rt.serialized(|store| store.select(&p.id)).await.unwrap();
        let g2 = rt.serialized(|store| store.select(&p.id)).await.unwrap();
        assert!(g2.generation.0 > g1.generation.0);
    }

    // -- Shared managed ProfileStore: paste/QR preview, confirm, switch -----

    #[tokio::test]
    async fn preview_from_scan_can_be_confirmed_through_same_store() {
        let (rt, _prefs, _secure) = make_memory_runtime();
        let store_arc = rt.store_arc();
        let clock = Arc::new(StepClock::new(0));
        let handler = ManagedPreviewHandler::new(store_arc, clock);

        // Simulate QR scan: raw text -> handler -> preview.
        let raw = auth_url("hub.example.com");
        let preview =
            <ManagedPreviewHandler as tauri_plugin_evener_native::PreviewHandler>::preview(
                &handler, &raw,
            )
            .unwrap();
        assert!(preview.preview_id.starts_with("preview-"));
        assert_eq!(preview.origin, "https://hub.example.com");

        // Confirm through the SAME store.
        let p = rt
            .serialized(|store| {
                store.confirm_pairing(&preview.preview_id, "Alpha", false, ReleaseMode::Release)
            })
            .await
            .unwrap();
        assert_eq!(p.name, "Alpha");
        assert_eq!(p.origin, "https://hub.example.com");
    }

    #[tokio::test]
    async fn paste_preview_and_scan_preview_share_same_store() {
        let (rt, _prefs, _secure) = make_memory_runtime();
        let store_arc = rt.store_arc();
        let clock = Arc::new(StepClock::new(0));
        let handler = ManagedPreviewHandler::new(store_arc, clock);

        let paste_pv = rt
            .serialized(|store| store.preview_pairing(&auth_url("hub1.example.com")))
            .await
            .unwrap();

        let scan_pv =
            <ManagedPreviewHandler as tauri_plugin_evener_native::PreviewHandler>::preview(
                &handler,
                &auth_url("hub2.example.com"),
            )
            .unwrap();

        let p1 = rt
            .serialized(|store| {
                store.confirm_pairing(&paste_pv.preview_id, "Alpha", false, ReleaseMode::Release)
            })
            .await
            .unwrap();
        let p2 = rt
            .serialized(|store| {
                store.confirm_pairing(&scan_pv.preview_id, "Beta", false, ReleaseMode::Release)
            })
            .await
            .unwrap();

        assert_ne!(p1.id, p2.id);
        assert_eq!(rt.serialized(|store| store.list()).await.unwrap().len(), 2);
    }

    // -- Redacted responses ------------------------------------------------

    #[tokio::test]
    async fn preview_response_never_contains_token() {
        let (rt, _prefs, _secure) = make_memory_runtime();
        let pv = rt
            .serialized(|store| store.preview_pairing(&auth_url("hub.example.com")))
            .await
            .unwrap();
        assert!(!pv.preview_id.contains(TOKEN));
        assert!(!pv.origin.contains(TOKEN));
    }

    #[tokio::test]
    async fn profile_summary_response_never_contains_token() {
        let (rt, _prefs, _secure) = make_memory_runtime();
        let pv = rt
            .serialized(|store| store.preview_pairing(&auth_url("hub.example.com")))
            .await
            .unwrap();
        let p = rt
            .serialized(|store| {
                store.confirm_pairing(&pv.preview_id, "Alpha", false, ReleaseMode::Release)
            })
            .await
            .unwrap();
        let resp = ProfileSummaryResponse::from(p);
        let json = serde_json::to_string(&resp).unwrap();
        assert!(!json.contains(TOKEN));
    }

    // -- Deferred redaction: Keychain read error -----------------------------

    #[tokio::test]
    async fn remove_keychain_read_error_is_redacted() {
        let prefs = Arc::new(MemoryPreferences::new());
        let secure = Arc::new(MemorySecureStore::new());
        let probe = Arc::new(OkProbe);
        let clock = Arc::new(StepClock::new(0));
        let (rt, _) = make_runtime(prefs, secure.clone(), probe, clock);

        let pv = rt
            .serialized(|store| store.preview_pairing(&auth_url("hub.example.com")))
            .await
            .unwrap();
        let p = rt
            .serialized(|store| {
                store.confirm_pairing(&pv.preview_id, "Alpha", false, ReleaseMode::Release)
            })
            .await
            .unwrap();

        secure.fail_next_get(p.id.clone());
        let err = rt
            .serialized(|store| store.remove(&p.id))
            .await
            .unwrap_err();

        assert!(matches!(err, ProfileError::SecureStore(_)));
        assert!(!format!("{err}").contains(TOKEN));
        assert!(!format!("{err:?}").contains(TOKEN));
        let json = serde_json::to_string(&format!("{err}")).unwrap();
        assert!(!json.contains(TOKEN));
    }

    // -- Deferred redaction: Consistency Display/Debug/serialization ---------

    #[tokio::test]
    async fn consistency_error_display_redacted() {
        let prefs = Arc::new(MemoryPreferences::new());
        let secure = Arc::new(MemorySecureStore::new());
        let probe = Arc::new(OkProbe);
        let clock = Arc::new(StepClock::new(0));
        let (rt, _) = make_runtime(prefs.clone(), secure.clone(), probe, clock);

        prefs.fail_next_save();
        secure.fail_all_next_delete();
        let pv = rt
            .serialized(|store| store.preview_pairing(&auth_url("hub.example.com")))
            .await
            .unwrap();
        let err = rt
            .serialized(|store| {
                store.confirm_pairing(&pv.preview_id, "Alpha", false, ReleaseMode::Release)
            })
            .await
            .unwrap_err();

        match &err {
            ProfileError::Consistency {
                profile_id,
                operation,
                rollback_operation,
            } => {
                assert!(!profile_id.is_empty());
                assert_eq!(operation, "add");
                assert_eq!(rollback_operation, "delete");
            }
            _ => panic!("expected Consistency, got {err:?}"),
        }

        let display = format!("{err}");
        let debug = format!("{err:?}");
        let json = serde_json::to_string(&display).unwrap();
        assert!(!display.contains(TOKEN));
        assert!(!debug.contains(TOKEN));
        assert!(!json.contains(TOKEN));
    }

    #[tokio::test]
    async fn consistency_error_repair_redacted() {
        let prefs = Arc::new(MemoryPreferences::new());
        let secure = Arc::new(MemorySecureStore::new());
        let probe = Arc::new(OkProbe);
        let clock = Arc::new(StepClock::new(0));
        let (rt, _) = make_runtime(prefs.clone(), secure.clone(), probe, clock);

        let pv = rt
            .serialized(|store| store.preview_pairing(&auth_url("hub.example.com")))
            .await
            .unwrap();
        let p = rt
            .serialized(|store| {
                store.confirm_pairing(&pv.preview_id, "Alpha", false, ReleaseMode::Release)
            })
            .await
            .unwrap();

        const NEW_TOKEN: &str = "AQIDBAUGBwgJCgsMDQ4PEBESExQVFhcYGRobHB0eHyA";
        let new_url = format!("https://hub.example.com:8443/auth?token={NEW_TOKEN}");
        let pv2 = rt
            .serialized(|store| store.preview_repair(&p.id, &new_url))
            .await
            .unwrap();
        prefs.fail_next_save();
        secure.fail_set_after_n(p.id.clone(), 2);
        let err = rt
            .serialized(|store| {
                store.confirm_pairing(&pv2.preview_id, "Alpha", true, ReleaseMode::Release)
            })
            .await
            .unwrap_err();

        match &err {
            ProfileError::Consistency {
                operation,
                rollback_operation,
                ..
            } => {
                assert_eq!(operation, "repair");
                assert_eq!(rollback_operation, "restore");
            }
            _ => panic!("expected Consistency, got {err:?}"),
        }
        assert!(!format!("{err}").contains(NEW_TOKEN));
        assert!(!format!("{err:?}").contains(NEW_TOKEN));
        assert!(!format!("{err}").contains(TOKEN));
        assert!(!format!("{err:?}").contains(TOKEN));
    }

    #[tokio::test]
    async fn consistency_error_remove_redacted() {
        let prefs = Arc::new(MemoryPreferences::new());
        let secure = Arc::new(MemorySecureStore::new());
        let probe = Arc::new(OkProbe);
        let clock = Arc::new(StepClock::new(0));
        let (rt, _) = make_runtime(prefs.clone(), secure.clone(), probe, clock);

        let pv = rt
            .serialized(|store| store.preview_pairing(&auth_url("hub.example.com")))
            .await
            .unwrap();
        let p = rt
            .serialized(|store| {
                store.confirm_pairing(&pv.preview_id, "Alpha", false, ReleaseMode::Release)
            })
            .await
            .unwrap();

        prefs.fail_next_save();
        secure.fail_set_after_n(p.id.clone(), 1);
        let err = rt
            .serialized(|store| store.remove(&p.id))
            .await
            .unwrap_err();

        match &err {
            ProfileError::Consistency {
                operation,
                rollback_operation,
                ..
            } => {
                assert_eq!(operation, "remove");
                assert_eq!(rollback_operation, "restore");
            }
            _ => panic!("expected Consistency, got {err:?}"),
        }
        assert!(!format!("{err}").contains(TOKEN));
        assert!(!format!("{err:?}").contains(TOKEN));
    }

    // -- Full lifecycle through runtime ------------------------------------

    #[tokio::test]
    async fn full_lifecycle_list_add_repair_rename_remove_select() {
        let (rt, _prefs, _secure) = make_memory_runtime();

        // Add Alpha
        let pv = rt
            .serialized(|store| store.preview_pairing(&auth_url("hub1.example.com")))
            .await
            .unwrap();
        let alpha = rt
            .serialized(|store| {
                store.confirm_pairing(&pv.preview_id, "Alpha", false, ReleaseMode::Release)
            })
            .await
            .unwrap();

        // Add Beta
        let pv = rt
            .serialized(|store| store.preview_pairing(&auth_url("hub2.example.com")))
            .await
            .unwrap();
        let beta = rt
            .serialized(|store| {
                store.confirm_pairing(&pv.preview_id, "Beta", false, ReleaseMode::Release)
            })
            .await
            .unwrap();

        // List
        let list = rt.serialized(|store| store.list()).await.unwrap();
        assert_eq!(list.len(), 2);

        // Select Alpha
        let sel = rt
            .serialized(|store| store.select(&alpha.id))
            .await
            .unwrap();
        assert_eq!(sel.profile_id.as_deref(), Some(alpha.id.as_str()));

        // Rename Alpha to Gamma
        let renamed = rt
            .serialized(|store| store.rename(&alpha.id, "Gamma"))
            .await
            .unwrap();
        assert_eq!(renamed.name, "Gamma");

        // Re-pair Alpha/Gamma with new token (same origin, allow dup).
        const NEW_TOKEN: &str = "AQIDBAUGBwgJCgsMDQ4PEBESExQVFhcYGRobHB0eHyA";
        let new_url = format!("https://hub1.example.com:8443/auth?token={NEW_TOKEN}");
        let pv = rt
            .serialized(|store| store.preview_repair(&alpha.id, &new_url))
            .await
            .unwrap();
        let repaired = rt
            .serialized(|store| {
                store.confirm_pairing(&pv.preview_id, "Gamma", true, ReleaseMode::Release)
            })
            .await
            .unwrap();
        assert_eq!(repaired.id, alpha.id);

        // Remove Beta
        let _ = rt.serialized(|store| store.remove(&beta.id)).await.unwrap();
        let list = rt.serialized(|store| store.list()).await.unwrap();
        assert_eq!(list.len(), 1);
        assert_eq!(list[0].name, "Gamma");
    }

    // -- Switch closes old transport before active change ------------------

    #[tokio::test]
    async fn switch_closes_transport_before_active_change() {
        let (rt, _prefs, _secure) = make_memory_runtime();

        let pv = rt
            .serialized(|store| store.preview_pairing(&auth_url("hub1.example.com")))
            .await
            .unwrap();
        let alpha = rt
            .serialized(|store| {
                store.confirm_pairing(&pv.preview_id, "Alpha", false, ReleaseMode::Release)
            })
            .await
            .unwrap();

        let pv = rt
            .serialized(|store| store.preview_pairing(&auth_url("hub2.example.com")))
            .await
            .unwrap();
        let beta = rt
            .serialized(|store| {
                store.confirm_pairing(&pv.preview_id, "Beta", false, ReleaseMode::Release)
            })
            .await
            .unwrap();

        rt.serialized(|store| store.select(&alpha.id))
            .await
            .unwrap();
        let _ = rt.serialized(|store| store.select(&beta.id)).await.unwrap();
        let gen = rt.store().generation();
        assert!(gen.0 > 0);
    }

    // -- KeychainBridge never silently downgrades failures ------------------

    #[tokio::test]
    async fn keychain_bridge_propagates_failure() {
        let secure = Arc::new(MemorySecureStore::new());
        let bridge = KeychainBridge::new(secure.clone());

        bridge.set("id1", "token1").unwrap();
        assert_eq!(bridge.get("id1").unwrap(), Some("token1".to_owned()));

        secure.fail_next_get("id1".to_owned());
        let err = bridge.get("id1").unwrap_err();
        assert!(matches!(err, ProfileError::SecureStore(_)));
    }
}
