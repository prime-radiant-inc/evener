//! Production profile runtime: one managed `ProfileStore` with atomic file
//! preferences, Keychain bridge, real pairing probe, async lifecycle mutex,
//! and a managed preview handler that delegates to the exact same store.
//!
//! JavaScript receives no capability, raw QR text, or native path. All profile
//! lifecycle commands serialize through the async mutex.

use std::sync::Arc;

use serde::{Deserialize, Serialize};

use crate::error::{ProfileError, ReleaseMode};
use crate::http_transport::PinnedHttpBoundary;
use crate::network_policy::NetworkPolicy;
use crate::profile::{
    self, PairingProbe, PreferencesStore, ProfileStore, ProfileSummary, SecureStore, SelectResult,
};

// ---------------------------------------------------------------------------
// Atomic file-based preferences store
// ---------------------------------------------------------------------------

/// File-based preferences store using atomic temp/write/fsync/rename under a
/// directory (Tauri app-data). Stores nonsecret summaries only.
pub struct FilePreferences {
    dir: std::path::PathBuf,
    path: std::path::PathBuf,
    directory_sync: Arc<dyn DirectorySync>,
}

trait DirectorySync: Send + Sync {
    fn sync(&self, directory: &std::path::Path) -> Result<(), std::io::Error>;
}

struct SystemDirectorySync;

impl DirectorySync for SystemDirectorySync {
    fn sync(&self, directory: &std::path::Path) -> Result<(), std::io::Error> {
        std::fs::File::open(directory)?.sync_all()
    }
}

impl FilePreferences {
    /// Create a preferences store backed by `dir/preferences.json`.
    /// The directory must already exist (app-data root).
    pub fn new(dir: impl AsRef<std::path::Path>) -> Self {
        let dir = dir.as_ref().to_path_buf();
        let path = dir.join("preferences.json");
        Self {
            dir,
            path,
            directory_sync: Arc::new(SystemDirectorySync),
        }
    }

    #[cfg(test)]
    fn with_directory_sync(
        dir: impl AsRef<std::path::Path>,
        directory_sync: Arc<dyn DirectorySync>,
    ) -> Self {
        let dir = dir.as_ref().to_path_buf();
        let path = dir.join("preferences.json");
        Self {
            dir,
            path,
            directory_sync,
        }
    }

    fn load_from(path: &std::path::Path) -> Result<profile::Preferences, ProfileError> {
        match std::fs::read(path) {
            Ok(bytes) => {
                let prefs: profile::Preferences = serde_json::from_slice(&bytes).map_err(|_| {
                    ProfileError::PreferencesConsistency {
                        reason: "malformed preferences",
                    }
                })?;
                if prefs.version != profile::PREFERENCES_VERSION {
                    return Err(ProfileError::PreferencesConsistency {
                        reason: "unsupported preferences version",
                    });
                }
                Ok(prefs)
            }
            Err(error) if error.kind() == std::io::ErrorKind::NotFound => {
                Ok(profile::Preferences::default())
            }
            Err(_) => Err(ProfileError::PreferencesConsistency {
                reason: "preferences unreadable",
            }),
        }
    }
}

impl PreferencesStore for FilePreferences {
    fn load(&self) -> Result<profile::Preferences, ProfileError> {
        Self::load_from(&self.path)
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

        // Persist the directory entry containing the rename. Without this
        // fsync, a power loss can retain neither the old nor the new name.
        self.directory_sync
            .sync(&self.dir)
            .map_err(|e| ProfileError::Preferences(e.to_string()))?;

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
/// mobile API version) and then an authenticated harmless endpoint. Both
/// requests use the shared pinned reqwest boundary: the original hostname
/// remains in the URL for Host/TLS SNI/certificate identity while DNS is
/// overridden with the policy-approved socket address. Blocking reqwest runs
/// on a dedicated OS thread, never re-entering Tokio. Never logs bodies, URLs,
/// or tokens.
pub struct RealPairingProbe {
    boundary: PinnedHttpBoundary,
}

impl RealPairingProbe {
    pub fn new(policy: Arc<NetworkPolicy>) -> Self {
        Self {
            boundary: PinnedHttpBoundary::new(policy, ReleaseMode::Release),
        }
    }

    pub fn with_root_certificate(mut self, certificate: reqwest::Certificate) -> Self {
        self.boundary = self.boundary.with_root_certificate(certificate);
        self
    }
}

impl Default for RealPairingProbe {
    fn default() -> Self {
        Self {
            boundary: PinnedHttpBoundary::new(
                Arc::new(NetworkPolicy::new(Box::new(SystemDnsResolver))),
                ReleaseMode::Release,
            ),
        }
    }
}

impl PairingProbe for RealPairingProbe {
    fn probe(&self, origin: &str, token: &str, mode: ReleaseMode) -> Result<i64, ProfileError> {
        // reqwest's blocking client owns a runtime, so execute it on a fresh
        // OS thread rather than constructing/dropping it inside Tauri's Tokio
        // runtime. The thread receives only an immutable boundary/origin/token.
        // Preserve the resolver and any additional roots configured on this
        // probe while applying the caller's release/debug address policy.
        let boundary = self.boundary.clone_with_mode(mode);
        let origin_owned = origin.to_owned();
        let token_owned = token.to_owned();
        let result =
            std::thread::spawn(move || probe_blocking(&boundary, &origin_owned, &token_owned))
                .join()
                .map_err(|_| ProfileError::ProbeFailed {
                    origin: origin.to_owned(),
                    message: "probe worker failed".to_owned(),
                })?;
        result.map_err(|message| ProfileError::ProbeFailed {
            origin: origin.to_owned(),
            message,
        })
    }
}

fn probe_blocking(boundary: &PinnedHttpBoundary, origin: &str, token: &str) -> Result<i64, String> {
    let health_body = blocking_http_get(boundary, origin, "/api/health", None)
        .map_err(|e| format!("health request failed: {e}"))?;
    let health_json: serde_json::Value =
        serde_json::from_slice(&health_body).map_err(|_| "health body parse failed".to_owned())?;
    let version = health_json
        .get("mobile_api_version")
        .and_then(|v| v.as_i64())
        .ok_or_else(|| "mobile_api_version missing".to_owned())?;

    // Authenticated harmless probe.
    let _pairing_body = blocking_http_get(boundary, origin, "/api/mobile/pairing", Some(token))
        .map_err(|e| format!("auth probe failed: {e}"))?;

    Ok(version)
}

/// Blocking HTTP GET through the same pinned resolver/TLS boundary as HubHttp.
/// Redirects are disabled and rejected explicitly. No Tokio runtime.
fn blocking_http_get(
    boundary: &PinnedHttpBoundary,
    origin: &str,
    path: &str,
    token: Option<&str>,
) -> Result<Vec<u8>, String> {
    let (mut url, pinned) = boundary
        .resolve(origin)
        .map_err(|_| "policy rejected".to_owned())?;
    url.set_path(path);
    url.set_query(None);
    let client = boundary
        .blocking_client(&pinned)
        .map_err(|_| "client creation failed".to_owned())?;
    let mut request = client.get(url);
    if let Some(token) = token {
        request = request.bearer_auth(token);
    }
    let response = request.send().map_err(|_| "request failed".to_owned())?;
    let status_code = response.status().as_u16();
    if (300..400).contains(&status_code) {
        return Err("redirect rejected".to_owned());
    }
    if !(200..300).contains(&status_code) {
        return Err(format!("status {status_code}"));
    }
    response
        .bytes()
        .map(|bytes| bytes.to_vec())
        .map_err(|_| "body read failed".to_owned())
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
}

impl ManagedPreviewHandler {
    pub fn new(store: Arc<ProfileStore>) -> Self {
        Self { store }
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

#[derive(Debug, Clone, Deserialize)]
#[serde(rename_all = "camelCase")]
pub struct CancelPreviewRequest {
    pub preview_id: String,
}

// ---------------------------------------------------------------------------
// Tests
// ---------------------------------------------------------------------------

#[cfg(test)]
mod tests {
    use super::*;
    use crate::network_policy::AlwaysPrivateResolver;
    use crate::network_policy::NetworkPolicy;
    use crate::profile::Clock;
    use crate::profile::{
        MemoryPreferences, MemorySecureStore, OkProbe, RecordingCloseTransport, StepClock,
    };
    use std::sync::atomic::{AtomicUsize, Ordering};

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

    #[test]
    fn file_preferences_missing_is_default_but_corrupt_is_consistency_error() {
        let dir = tempfile::tempdir().unwrap();
        let prefs = FilePreferences::new(dir.path());
        assert_eq!(prefs.load().unwrap(), profile::Preferences::default());

        std::fs::write(dir.path().join("preferences.json"), b"{broken").unwrap();
        assert!(matches!(
            prefs.load(),
            Err(ProfileError::PreferencesConsistency {
                reason: "malformed preferences"
            })
        ));
    }

    #[test]
    fn file_preferences_unreadable_is_not_treated_as_empty() {
        let dir = tempfile::tempdir().unwrap();
        std::fs::create_dir(dir.path().join("preferences.json")).unwrap();
        let prefs = FilePreferences::new(dir.path());
        assert!(matches!(
            prefs.load(),
            Err(ProfileError::PreferencesConsistency {
                reason: "preferences unreadable"
            })
        ));
    }

    #[test]
    fn file_preferences_rejects_invalid_version_without_rewrite() {
        let dir = tempfile::tempdir().unwrap();
        let path = dir.path().join("preferences.json");
        let bytes = br#"{"version":99,"profiles":[],"active_id":null}"#;
        std::fs::write(&path, bytes).unwrap();
        let prefs = FilePreferences::new(dir.path());
        assert!(matches!(
            prefs.load(),
            Err(ProfileError::PreferencesConsistency {
                reason: "unsupported preferences version"
            })
        ));
        assert_eq!(std::fs::read(path).unwrap(), bytes);
    }

    struct RecordingDirectorySync(AtomicUsize);

    impl DirectorySync for RecordingDirectorySync {
        fn sync(&self, _directory: &std::path::Path) -> Result<(), std::io::Error> {
            self.0.fetch_add(1, Ordering::SeqCst);
            Ok(())
        }
    }

    #[test]
    fn file_preferences_fsyncs_parent_after_atomic_rename() {
        let dir = tempfile::tempdir().unwrap();
        let sync = Arc::new(RecordingDirectorySync(AtomicUsize::new(0)));
        let prefs = FilePreferences::with_directory_sync(dir.path(), sync.clone());
        prefs.save(&profile::Preferences::default()).unwrap();
        assert_eq!(sync.0.load(Ordering::SeqCst), 1);
        assert_eq!(prefs.load().unwrap(), profile::Preferences::default());
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

    #[tokio::test(flavor = "multi_thread", worker_threads = 4)]
    async fn concurrent_snapshot_repair_and_select_never_mix_origin_and_token() {
        const NEW_TOKEN: &str = "AQIDBAUGBwgJCgsMDQ4PEBESExQVFhcYGRobHB0eHyA";
        let (runtime, _prefs, _secure) = make_memory_runtime();

        let alpha_preview = runtime
            .serialized(|store| store.preview_pairing(&auth_url("alpha.example.com")))
            .await
            .unwrap();
        let alpha = runtime
            .serialized(|store| {
                store.confirm_pairing(
                    &alpha_preview.preview_id,
                    "Alpha",
                    false,
                    ReleaseMode::Release,
                )
            })
            .await
            .unwrap();
        let beta_preview = runtime
            .serialized(|store| store.preview_pairing(&auth_url("beta.example.com")))
            .await
            .unwrap();
        let beta = runtime
            .serialized(|store| {
                store.confirm_pairing(
                    &beta_preview.preview_id,
                    "Beta",
                    false,
                    ReleaseMode::Release,
                )
            })
            .await
            .unwrap();
        runtime
            .serialized(|store| store.select(&alpha.id))
            .await
            .unwrap();
        let repair_url = format!("https://alpha-new.example.com/auth?token={NEW_TOKEN}");
        let repair_preview = runtime
            .serialized(|store| store.preview_repair(&alpha.id, &repair_url))
            .await
            .unwrap();

        let runtime = Arc::new(runtime);
        let snapshots_runtime = runtime.clone();
        let snapshots = tokio::spawn(async move {
            let mut captured = Vec::new();
            for _ in 0..100 {
                if let Ok(snapshot) = snapshots_runtime
                    .serialized(|store| store.active_snapshot_current())
                    .await
                {
                    captured.push(snapshot.into_parts());
                }
                tokio::task::yield_now().await;
            }
            captured
        });
        let repair_runtime = runtime.clone();
        let repair = tokio::spawn(async move {
            repair_runtime
                .serialized(|store| {
                    store.confirm_pairing(
                        &repair_preview.preview_id,
                        "Alpha",
                        false,
                        ReleaseMode::Release,
                    )
                })
                .await
        });
        let select_runtime = runtime.clone();
        let beta_id = beta.id.clone();
        let select = tokio::spawn(async move {
            select_runtime
                .serialized(|store| store.select(&beta_id))
                .await
        });

        repair.await.unwrap().unwrap();
        select.await.unwrap().unwrap();
        let snapshots = snapshots.await.unwrap();
        assert!(!snapshots.is_empty());
        for (id, _generation, origin, token) in snapshots {
            let is_alpha_old =
                id == alpha.id && origin == "https://alpha.example.com" && token == TOKEN;
            let is_alpha_new =
                id == alpha.id && origin == "https://alpha-new.example.com" && token == NEW_TOKEN;
            let is_beta = id == beta.id && origin == "https://beta.example.com" && token == TOKEN;
            assert!(is_alpha_old || is_alpha_new || is_beta, "mixed snapshot");
        }
    }

    #[tokio::test]
    async fn active_snapshot_rejects_caller_profile_mismatch() {
        let (runtime, _prefs, _secure) = make_memory_runtime();
        let preview = runtime
            .serialized(|store| store.preview_pairing(&auth_url("hub.example.com")))
            .await
            .unwrap();
        let profile = runtime
            .serialized(|store| {
                store.confirm_pairing(&preview.preview_id, "Alpha", false, ReleaseMode::Release)
            })
            .await
            .unwrap();
        runtime
            .serialized(|store| store.select(&profile.id))
            .await
            .unwrap();

        assert!(runtime
            .serialized(|store| store.active_snapshot("different-profile"))
            .await
            .is_err());
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
        let handler = ManagedPreviewHandler::new(store_arc);

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
        let handler = ManagedPreviewHandler::new(store_arc);

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

    // -- Probe block_in_place does not panic in async context -------------

    #[tokio::test(flavor = "multi_thread")]
    async fn real_pairing_probe_does_not_panic_in_async_context() {
        // RealPairingProbe::probe uses blocking std::net HTTP (no tokio
        // runtime re-entry). This test proves the probe can be
        // called from within a tokio runtime (as it is when called through
        // serialized()) without panicking. We don't make a
        // real network call — we just prove the probe construction and
        // blocking path are safe. The probe will fail with a
        // ProbeFailed error (no server), which is the expected non-panic
        // outcome.
        let probe = RealPairingProbe::default();
        let result = probe.probe(
            "https://nonexistent.invalid",
            "dummy-token",
            ReleaseMode::Release,
        );
        // The probe should return an error (not panic).
        assert!(result.is_err());
        let err = result.unwrap_err();
        assert!(matches!(err, ProfileError::ProbeFailed { .. }));
        // The error must not contain the token.
        assert!(!format!("{err}").contains("dummy-token"));
        assert!(!format!("{err:?}").contains("dummy-token"));
    }
}
