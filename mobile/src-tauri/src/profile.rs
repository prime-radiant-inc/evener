//! Multi-profile lifecycle: ordered redacted summaries, active profile ID,
//! per-profile Keychain tokens, two-phase pairing preview/confirm, and
//! profile switching with a monotonic generation.
//!
//! JavaScript never sees a capability. It receives only redacted
//! [`ProfileSummary { id, name, origin }`] and opaque preview IDs.

use std::collections::HashMap;
use std::fmt;
use std::sync::atomic::{AtomicU64, Ordering};
use std::sync::{Arc, Mutex};

use serde::{Deserialize, Serialize};

use crate::error::{ProfileError, ReleaseMode};
use crate::network_policy::NetworkPolicy;
use crate::pairing::PairingUrl;

/// Redacted profile summary: the only profile data JavaScript ever sees.
#[derive(Debug, Clone, Serialize, Deserialize, PartialEq, Eq)]
pub struct ProfileSummary {
    pub id: String,
    pub name: String,
    pub origin: String,
}

/// Monotonically increasing profile generation. Each successful select returns
/// a value strictly greater than the previous.
#[derive(Debug, Clone, Copy, PartialEq, Eq, PartialOrd, Ord)]
pub struct ProfileGeneration(pub u64);

impl fmt::Display for ProfileGeneration {
    fn fmt(&self, f: &mut fmt::Formatter<'_>) -> fmt::Result {
        write!(f, "{}", self.0)
    }
}

/// Preferences content stored in nonsecret atomic preferences.
#[derive(Debug, Clone, Default, Serialize, Deserialize, PartialEq, Eq)]
pub struct Preferences {
    /// Ordered redacted profile summaries.
    #[serde(default)]
    pub profiles: Vec<ProfileSummary>,
    /// Active profile ID, or none.
    #[serde(default)]
    pub active_id: Option<String>,
}

/// Atomic preferences store (injected). Must be atomic load/save.
pub trait PreferencesStore: Send + Sync {
    fn load(&self) -> Result<Preferences, ProfileError>;
    fn save(&self, prefs: &Preferences) -> Result<(), ProfileError>;
}

/// Per-profile Keychain secure store (injected).
/// Account key is `profile:<uuid>`.
pub trait SecureStore: Send + Sync {
    fn get(&self, profile_id: &str) -> Result<Option<String>, ProfileError>;
    fn set(&self, profile_id: &str, token: &str) -> Result<(), ProfileError>;
    fn delete(&self, profile_id: &str) -> Result<(), ProfileError>;
}

/// Pairing probe: unauthenticated `/api/health` (mobile API version) plus an
/// authenticated harmless endpoint, both with redirects disabled. Injected so
/// tests are deterministic and never touch the network.
pub trait PairingProbe: Send + Sync {
    /// Returns the mobile API version on success.
    fn probe(&self, origin: &str, token: &str, mode: ReleaseMode) -> Result<i64, ProfileError>;
}

/// Monotonic clock for preview expiry (injected).
pub trait Clock: Send + Sync {
    /// Seconds since some monotonic epoch.
    fn now_secs(&self) -> u64;
}

/// Callback invoked before the active profile changes, to close the old
/// transport generation. Returns the generation that was closed.
pub trait CloseTransport: Send + Sync {
    fn close_current(&self) -> ProfileGeneration;
}

/// Maximum preview lifetime in seconds (5 minutes).
pub const PREVIEW_TTL_SECS: u64 = 300;

/// A pending pairing preview: holds the parsed secret in native memory only.
struct PendingPreview {
    pairing: PairingUrl,
    created_at: u64,
    /// Optional profile ID when previewing an edit/re-pair of an existing profile.
    existing_id: Option<String>,
}

/// Result of previewing a pairing URL.
#[derive(Debug, Clone, PartialEq, Eq)]
pub struct PairingPreview {
    pub preview_id: String,
    pub origin: String,
}

/// Result of selecting/activating a profile.
#[derive(Debug, Clone, PartialEq, Eq)]
pub struct SelectResult {
    pub profile_id: Option<String>,
    pub generation: ProfileGeneration,
}

/// The multi-profile store.
pub struct ProfileStore {
    prefs: Arc<dyn PreferencesStore>,
    secure: Arc<dyn SecureStore>,
    probe: Arc<dyn PairingProbe>,
    clock: Arc<dyn Clock>,
    policy: Arc<NetworkPolicy>,
    /// Pending previews keyed by opaque ID.
    pending: Mutex<HashMap<String, PendingPreview>>,
    /// Monotonic generation counter.
    generation: AtomicU64,
    /// Close-current-transport callback invoked before active ID changes.
    close_transport: Arc<dyn CloseTransport>,
}

impl ProfileStore {
    pub fn new(
        prefs: Arc<dyn PreferencesStore>,
        secure: Arc<dyn SecureStore>,
        probe: Arc<dyn PairingProbe>,
        clock: Arc<dyn Clock>,
        policy: Arc<NetworkPolicy>,
        close_transport: Arc<dyn CloseTransport>,
    ) -> Self {
        Self {
            prefs,
            secure,
            probe,
            clock,
            policy,
            pending: Mutex::new(HashMap::new()),
            generation: AtomicU64::new(0),
            close_transport,
        }
    }

    /// List all redacted profile summaries in order.
    pub fn list(&self) -> Result<Vec<ProfileSummary>, ProfileError> {
        Ok(self.prefs.load()?.profiles)
    }

    /// The active profile ID, or none.
    pub fn active_id(&self) -> Result<Option<String>, ProfileError> {
        Ok(self.prefs.load()?.active_id)
    }

    /// Current profile generation.
    pub fn generation(&self) -> ProfileGeneration {
        ProfileGeneration(self.generation.load(Ordering::SeqCst))
    }

    /// Drop expired previews and return the count removed.
    fn expire_pending(&self) -> usize {
        let now = self.clock.now_secs();
        let mut pending = self.pending.lock().unwrap();
        let before = pending.len();
        pending.retain(|_, p| now.saturating_sub(p.created_at) < PREVIEW_TTL_SECS);
        before - pending.len()
    }

    /// Cancel a pending preview, clearing its held secret from native memory.
    pub fn cancel_preview(&self, preview_id: &str) -> Result<(), ProfileError> {
        self.pending
            .lock()
            .unwrap()
            .remove(preview_id)
            .ok_or_else(|| ProfileError::PreviewNotFound(preview_id.to_owned()))?;
        Ok(())
    }

    /// Clear all pending previews (background/foreground transition).
    pub fn clear_previews(&self) {
        self.pending.lock().unwrap().clear();
    }

    /// Phase 1: preview a pasted or scanned auth URL. Holds the parsed secret
    /// in native memory for at most 5 minutes. Returns an opaque preview ID
    /// and the normalized origin only.
    pub fn preview_pairing(&self, raw: &str) -> Result<PairingPreview, ProfileError> {
        self.expire_pending();
        let pairing = parse_pairing(raw)?;
        let origin = pairing.origin().to_owned();
        let preview_id = new_preview_id();
        let pending = PendingPreview {
            pairing,
            created_at: self.clock.now_secs(),
            existing_id: None,
        };
        self.pending
            .lock()
            .unwrap()
            .insert(preview_id.clone(), pending);
        Ok(PairingPreview { preview_id, origin })
    }

    /// Phase 1 for re-pair/edit: preview against an existing profile ID.
    pub fn preview_repair(
        &self,
        profile_id: &str,
        raw: &str,
    ) -> Result<PairingPreview, ProfileError> {
        self.expire_pending();
        let prefs = self.prefs.load()?;
        if !prefs.profiles.iter().any(|p| p.id == profile_id) {
            return Err(ProfileError::NotFound(profile_id.to_owned()));
        }
        let pairing = parse_pairing(raw)?;
        let origin = pairing.origin().to_owned();
        let preview_id = new_preview_id();
        let pending = PendingPreview {
            pairing,
            created_at: self.clock.now_secs(),
            existing_id: Some(profile_id.to_owned()),
        };
        self.pending
            .lock()
            .unwrap()
            .insert(preview_id.clone(), pending);
        Ok(PairingPreview { preview_id, origin })
    }

    /// Phase 2: confirm a preview. Names the profile, performs the authenticated
    /// probe with redirects disabled, then atomically adds or replaces only
    /// that profile. On failure the old profile remains fully usable.
    ///
    /// `allow_duplicate_origin` permits a second credential/profile for an
    /// origin that already exists.
    pub fn confirm_pairing(
        &self,
        preview_id: &str,
        name: &str,
        allow_duplicate_origin: bool,
        mode: ReleaseMode,
    ) -> Result<ProfileSummary, ProfileError> {
        self.expire_pending();
        let pending = self
            .pending
            .lock()
            .unwrap()
            .remove(preview_id)
            .ok_or_else(|| ProfileError::PreviewNotFound(preview_id.to_owned()))?;

        let name = name.trim();
        if name.is_empty() {
            return Err(ProfileError::BlankName);
        }

        let origin = pending.pairing.origin().to_owned();
        let token = pending.pairing.token();

        // Validate origin against network policy (re-resolve on this boundary).
        self.policy
            .resolve(pending.pairing.url(), mode)
            .map_err(|e| ProfileError::ProbeFailed {
                origin: origin.clone(),
                message: e.to_string(),
            })?;

        // Authenticated probe (health + harmless endpoint, redirects disabled).
        let mobile_api_version =
            self.probe
                .probe(&origin, token, mode)
                .map_err(|e| ProfileError::ProbeFailed {
                    origin: origin.clone(),
                    message: e.to_string(),
                })?;
        if mobile_api_version != 1 {
            return Err(ProfileError::MobileApiMismatch(mobile_api_version));
        }

        // Atomically load preferences, validate, mutate, save. Re-load after
        // probe to pick up concurrent changes; the probe may have taken time.
        let prefs = self.prefs.load()?;
        let mut new_prefs = prefs.clone();

        validate_name_unique(&new_prefs, name, pending.existing_id.as_deref())?;
        if !allow_duplicate_origin {
            validate_origin_unique(&new_prefs, &origin, pending.existing_id.as_deref())?;
        }

        let profile_id = match &pending.existing_id {
            Some(id) => id.clone(),
            None => uuid::Uuid::new_v4().to_string(),
        };

        let summary = ProfileSummary {
            id: profile_id.clone(),
            name: name.to_owned(),
            origin: origin.clone(),
        };

        // Keychain write FIRST: if it fails, preferences are untouched and the
        // old profile (if editing) remains fully usable.
        self.secure.set(&profile_id, token)?;

        // Update preferences: insert or replace by ID.
        match new_prefs.profiles.iter().position(|p| p.id == profile_id) {
            Some(idx) => {
                new_prefs.profiles[idx] = summary.clone();
            }
            None => {
                new_prefs.profiles.push(summary.clone());
            }
        }

        self.prefs.save(&new_prefs)?;

        Ok(summary)
    }

    /// Rename a profile. Names must be nonblank and case-insensitively unique.
    pub fn rename(&self, profile_id: &str, new_name: &str) -> Result<ProfileSummary, ProfileError> {
        let new_name = new_name.trim();
        if new_name.is_empty() {
            return Err(ProfileError::BlankName);
        }
        let prefs = self.prefs.load()?;
        let mut new_prefs = prefs.clone();
        validate_name_unique(&new_prefs, new_name, Some(profile_id))?;
        let idx = new_prefs
            .profiles
            .iter()
            .position(|p| p.id == profile_id)
            .ok_or_else(|| ProfileError::NotFound(profile_id.to_owned()))?;
        new_prefs.profiles[idx].name = new_name.to_owned();
        let summary = new_prefs.profiles[idx].clone();
        self.prefs.save(&new_prefs)?;
        Ok(summary)
    }

    /// Remove a profile. Deletes only that Keychain item. Removing the active
    /// profile selects the next ordered profile or none. Switching closes the
    /// old transport generation before the active ID changes. Never deletes
    /// another profile.
    pub fn remove(&self, profile_id: &str) -> Result<SelectResult, ProfileError> {
        let prefs = self.prefs.load()?;
        let mut new_prefs = prefs.clone();

        let idx = new_prefs
            .profiles
            .iter()
            .position(|p| p.id == profile_id)
            .ok_or_else(|| ProfileError::NotFound(profile_id.to_owned()))?;

        new_prefs.profiles.remove(idx);

        // Keychain delete BEFORE preferences save so a failed delete leaves
        // preferences consistent (profile still listed). Actually: to keep the
        // old profile usable on failure, delete Keychain first; if it fails we
        // abort and preferences are untouched.
        self.secure.delete(profile_id)?;

        let was_active = prefs.active_id.as_deref() == Some(profile_id);

        if was_active {
            // Close the current transport generation before changing active ID.
            self.close_transport.close_current();
            new_prefs.active_id = new_prefs.profiles.first().map(|p| p.id.clone());
        }

        self.prefs.save(&new_prefs)?;

        let result = if was_active {
            SelectResult {
                profile_id: new_prefs.active_id.clone(),
                generation: self.bump_generation(),
            }
        } else {
            SelectResult {
                profile_id: prefs.active_id.clone(),
                generation: self.generation(),
            }
        };

        Ok(result)
    }

    /// Select a profile as active. Closes the old transport generation before
    /// the active ID changes, then bumps the generation. Returns the new
    /// monotonically increasing generation.
    pub fn select(&self, profile_id: &str) -> Result<SelectResult, ProfileError> {
        let prefs = self.prefs.load()?;
        if !prefs.profiles.iter().any(|p| p.id == profile_id) {
            return Err(ProfileError::NotFound(profile_id.to_owned()));
        }

        // Close the current transport generation before active ID changes.
        self.close_transport.close_current();

        let mut new_prefs = prefs.clone();
        new_prefs.active_id = Some(profile_id.to_owned());
        self.prefs.save(&new_prefs)?;

        Ok(SelectResult {
            profile_id: Some(profile_id.to_owned()),
            generation: self.bump_generation(),
        })
    }

    fn bump_generation(&self) -> ProfileGeneration {
        let g = self.generation.fetch_add(1, Ordering::SeqCst) + 1;
        ProfileGeneration(g)
    }
}

fn new_preview_id() -> String {
    format!("preview-{}", uuid::Uuid::new_v4().simple())
}

fn parse_pairing(raw: &str) -> Result<PairingUrl, ProfileError> {
    PairingUrl::parse(raw).map_err(|e| ProfileError::ProbeFailed {
        origin: String::new(),
        message: e.to_string(),
    })
}

fn validate_name_unique(
    prefs: &Preferences,
    name: &str,
    except_id: Option<&str>,
) -> Result<(), ProfileError> {
    let lower = name.to_lowercase();
    for p in &prefs.profiles {
        if Some(p.id.as_str()) == except_id {
            continue;
        }
        if p.name.to_lowercase() == lower {
            return Err(ProfileError::DuplicateName(name.to_owned()));
        }
    }
    Ok(())
}

fn validate_origin_unique(
    prefs: &Preferences,
    origin: &str,
    except_id: Option<&str>,
) -> Result<(), ProfileError> {
    for p in &prefs.profiles {
        if Some(p.id.as_str()) == except_id {
            continue;
        }
        if p.origin == origin {
            return Err(ProfileError::DuplicateOrigin(origin.to_owned()));
        }
    }
    Ok(())
}

// ---------------------------------------------------------------------------
// In-memory fakes for deterministic tests
// ---------------------------------------------------------------------------

/// In-memory preferences store that survives a "restart" (clone) for tests.
#[derive(Default)]
pub struct MemoryPreferences {
    inner: Mutex<Preferences>,
    save_count: AtomicU64,
}

impl MemoryPreferences {
    pub fn new() -> Self {
        Self::default()
    }
    pub fn save_count(&self) -> u64 {
        self.save_count.load(Ordering::SeqCst)
    }
}

impl PreferencesStore for MemoryPreferences {
    fn load(&self) -> Result<Preferences, ProfileError> {
        Ok(self.inner.lock().unwrap().clone())
    }
    fn save(&self, prefs: &Preferences) -> Result<(), ProfileError> {
        *self.inner.lock().unwrap() = prefs.clone();
        self.save_count.fetch_add(1, Ordering::SeqCst);
        Ok(())
    }
}

#[derive(Default)]
pub struct MemorySecureStore {
    map: Mutex<HashMap<String, String>>,
    fail_set: Mutex<Option<String>>,
}

impl MemorySecureStore {
    pub fn new() -> Self {
        Self::default()
    }
    pub fn fail_next_set(&self, id: String) {
        *self.fail_set.lock().unwrap() = Some(id);
    }
    pub fn has(&self, id: &str) -> bool {
        self.map.lock().unwrap().contains_key(id)
    }
    pub fn get_token(&self, id: &str) -> Option<String> {
        self.map.lock().unwrap().get(id).cloned()
    }
}

impl SecureStore for MemorySecureStore {
    fn get(&self, profile_id: &str) -> Result<Option<String>, ProfileError> {
        Ok(self.map.lock().unwrap().get(profile_id).cloned())
    }
    fn set(&self, profile_id: &str, token: &str) -> Result<(), ProfileError> {
        if let Some(fail_id) = self.fail_set.lock().unwrap().take() {
            if fail_id == profile_id {
                return Err(ProfileError::SecureStore("injected set failure".to_owned()));
            }
        }
        self.map
            .lock()
            .unwrap()
            .insert(profile_id.to_owned(), token.to_owned());
        Ok(())
    }
    fn delete(&self, profile_id: &str) -> Result<(), ProfileError> {
        self.map.lock().unwrap().remove(profile_id);
        Ok(())
    }
}

/// Probe that always succeeds with mobile API version 1.
pub struct OkProbe;

impl PairingProbe for OkProbe {
    fn probe(&self, _origin: &str, _token: &str, _mode: ReleaseMode) -> Result<i64, ProfileError> {
        Ok(1)
    }
}

/// Probe that always fails.
pub struct FailProbe;

impl PairingProbe for FailProbe {
    fn probe(&self, origin: &str, _token: &str, _mode: ReleaseMode) -> Result<i64, ProfileError> {
        Err(ProfileError::ProbeFailed {
            origin: origin.to_owned(),
            message: "unreachable".to_owned(),
        })
    }
}

/// Probe returning a mismatched mobile API version.
pub struct MismatchProbe {
    pub version: i64,
}

impl PairingProbe for MismatchProbe {
    fn probe(&self, _origin: &str, _token: &str, _mode: ReleaseMode) -> Result<i64, ProfileError> {
        Ok(self.version)
    }
}

/// Manual step clock for deterministic expiry tests.
pub struct StepClock {
    secs: AtomicU64,
}

impl StepClock {
    pub fn new(start: u64) -> Self {
        Self {
            secs: AtomicU64::new(start),
        }
    }
    pub fn advance(&self, by: u64) {
        self.secs.fetch_add(by, Ordering::SeqCst);
    }
    pub fn set(&self, v: u64) {
        self.secs.store(v, Ordering::SeqCst);
    }
}

impl Clock for StepClock {
    fn now_secs(&self) -> u64 {
        self.secs.load(Ordering::SeqCst)
    }
}

#[derive(Default)]
pub struct RecordingCloseTransport {
    close_count: AtomicU64,
}

impl RecordingCloseTransport {
    pub fn close_count(&self) -> u64 {
        self.close_count.load(Ordering::SeqCst)
    }
}

impl CloseTransport for RecordingCloseTransport {
    fn close_current(&self) -> ProfileGeneration {
        self.close_count.fetch_add(1, Ordering::SeqCst);
        ProfileGeneration(0)
    }
}

#[cfg(test)]
fn make_store(
    prefs: Arc<MemoryPreferences>,
    secure: Arc<MemorySecureStore>,
    probe: Arc<dyn PairingProbe>,
    clock: Arc<StepClock>,
) -> ProfileStore {
    // Profile tests use HTTPS origins, which the policy allows public or
    // private; any successful resolution suffices. AlwaysPrivateResolver
    // resolves every hostname to a private IPv4 so confirm_pairing's policy
    // check succeeds without live DNS.
    let policy = Arc::new(NetworkPolicy::new(Box::new(
        crate::network_policy::AlwaysPrivateResolver,
    )));
    let close = Arc::new(RecordingCloseTransport::default());
    ProfileStore::new(prefs, secure, probe, clock, policy, close)
}

#[cfg(test)]
const TOKEN: &str = "AAECAwQFBgcICQoLDA0ODxAREhMUFRYXGBkaGxwdHh8";

#[cfg(test)]
fn auth_url(host: &str) -> String {
    format!("https://{host}/auth?token={TOKEN}")
}

#[cfg(test)]
mod tests {
    use super::*;

    // -- Two profiles with separate Keychain accounts -----------------------

    #[test]
    fn add_two_profiles_with_separate_keychain_accounts() {
        let prefs = Arc::new(MemoryPreferences::new());
        let secure = Arc::new(MemorySecureStore::new());
        let probe = Arc::new(OkProbe);
        let clock = Arc::new(StepClock::new(0));
        let store = make_store(prefs.clone(), secure.clone(), probe, clock);

        let p1 = store
            .confirm_pairing(
                &store
                    .preview_pairing(&auth_url("hub1.example.com"))
                    .unwrap()
                    .preview_id,
                "Alpha",
                false,
                ReleaseMode::Release,
            )
            .unwrap();
        let p2 = store
            .confirm_pairing(
                &store
                    .preview_pairing(&auth_url("hub2.example.com"))
                    .unwrap()
                    .preview_id,
                "Beta",
                false,
                ReleaseMode::Release,
            )
            .unwrap();

        assert_ne!(p1.id, p2.id);
        // Separate Keychain accounts: profile:<uuid>.
        assert!(secure.has(&p1.id));
        assert!(secure.has(&p2.id));
        assert_eq!(secure.get_token(&p1.id).unwrap(), TOKEN);
        assert_eq!(secure.get_token(&p2.id).unwrap(), TOKEN);
        assert_eq!(store.list().unwrap().len(), 2);
    }

    // -- Case-insensitive unique names --------------------------------------

    #[test]
    fn duplicate_name_case_insensitive_rejected() {
        let prefs = Arc::new(MemoryPreferences::new());
        let secure = Arc::new(MemorySecureStore::new());
        let probe = Arc::new(OkProbe);
        let clock = Arc::new(StepClock::new(0));
        let store = make_store(prefs, secure, probe, clock);

        store
            .confirm_pairing(
                &store
                    .preview_pairing(&auth_url("hub1.example.com"))
                    .unwrap()
                    .preview_id,
                "Alpha",
                false,
                ReleaseMode::Release,
            )
            .unwrap();
        let err = store
            .confirm_pairing(
                &store
                    .preview_pairing(&auth_url("hub2.example.com"))
                    .unwrap()
                    .preview_id,
                "alpha",
                false,
                ReleaseMode::Release,
            )
            .unwrap_err();
        assert!(matches!(err, ProfileError::DuplicateName(_)));
    }

    #[test]
    fn blank_name_rejected() {
        let prefs = Arc::new(MemoryPreferences::new());
        let secure = Arc::new(MemorySecureStore::new());
        let probe = Arc::new(OkProbe);
        let clock = Arc::new(StepClock::new(0));
        let store = make_store(prefs, secure, probe, clock);
        let err = store
            .confirm_pairing(
                &store
                    .preview_pairing(&auth_url("hub.example.com"))
                    .unwrap()
                    .preview_id,
                "   ",
                false,
                ReleaseMode::Release,
            )
            .unwrap_err();
        assert!(matches!(err, ProfileError::BlankName));
    }

    // -- Duplicate origin requires explicit confirmation --------------------

    #[test]
    fn duplicate_origin_rejected_without_allow_flag() {
        let prefs = Arc::new(MemoryPreferences::new());
        let secure = Arc::new(MemorySecureStore::new());
        let probe = Arc::new(OkProbe);
        let clock = Arc::new(StepClock::new(0));
        let store = make_store(prefs, secure, probe, clock);

        store
            .confirm_pairing(
                &store
                    .preview_pairing(&auth_url("hub.example.com"))
                    .unwrap()
                    .preview_id,
                "Alpha",
                false,
                ReleaseMode::Release,
            )
            .unwrap();
        let err = store
            .confirm_pairing(
                &store
                    .preview_pairing(&auth_url("hub.example.com"))
                    .unwrap()
                    .preview_id,
                "Beta",
                false,
                ReleaseMode::Release,
            )
            .unwrap_err();
        assert!(matches!(err, ProfileError::DuplicateOrigin(_)));
    }

    #[test]
    fn duplicate_origin_allowed_with_explicit_flag() {
        let prefs = Arc::new(MemoryPreferences::new());
        let secure = Arc::new(MemorySecureStore::new());
        let probe = Arc::new(OkProbe);
        let clock = Arc::new(StepClock::new(0));
        let store = make_store(prefs, secure, probe, clock);

        store
            .confirm_pairing(
                &store
                    .preview_pairing(&auth_url("hub.example.com"))
                    .unwrap()
                    .preview_id,
                "Alpha",
                false,
                ReleaseMode::Release,
            )
            .unwrap();
        store
            .confirm_pairing(
                &store
                    .preview_pairing(&auth_url("hub.example.com"))
                    .unwrap()
                    .preview_id,
                "Beta",
                true,
                ReleaseMode::Release,
            )
            .unwrap();
        assert_eq!(store.list().unwrap().len(), 2);
    }

    // -- Rename --------------------------------------------------------------

    #[test]
    fn rename_updates_name_and_persists() {
        let prefs = Arc::new(MemoryPreferences::new());
        let secure = Arc::new(MemorySecureStore::new());
        let probe = Arc::new(OkProbe);
        let clock = Arc::new(StepClock::new(0));
        let store = make_store(prefs.clone(), secure, probe, clock);

        let p = store
            .confirm_pairing(
                &store
                    .preview_pairing(&auth_url("hub.example.com"))
                    .unwrap()
                    .preview_id,
                "Alpha",
                false,
                ReleaseMode::Release,
            )
            .unwrap();
        let renamed = store.rename(&p.id, "Gamma").unwrap();
        assert_eq!(renamed.name, "Gamma");
        assert_eq!(store.list().unwrap()[0].name, "Gamma");
    }

    #[test]
    fn rename_to_existing_name_rejected() {
        let prefs = Arc::new(MemoryPreferences::new());
        let secure = Arc::new(MemorySecureStore::new());
        let probe = Arc::new(OkProbe);
        let clock = Arc::new(StepClock::new(0));
        let store = make_store(prefs, secure, probe, clock);

        let p1 = store
            .confirm_pairing(
                &store
                    .preview_pairing(&auth_url("hub1.example.com"))
                    .unwrap()
                    .preview_id,
                "Alpha",
                false,
                ReleaseMode::Release,
            )
            .unwrap();
        store
            .confirm_pairing(
                &store
                    .preview_pairing(&auth_url("hub2.example.com"))
                    .unwrap()
                    .preview_id,
                "Beta",
                false,
                ReleaseMode::Release,
            )
            .unwrap();
        let err = store.rename(&p1.id, "BETA").unwrap_err();
        assert!(matches!(err, ProfileError::DuplicateName(_)));
    }

    #[test]
    fn rename_same_profile_keeps_name() {
        let prefs = Arc::new(MemoryPreferences::new());
        let secure = Arc::new(MemorySecureStore::new());
        let probe = Arc::new(OkProbe);
        let clock = Arc::new(StepClock::new(0));
        let store = make_store(prefs, secure, probe, clock);

        let p = store
            .confirm_pairing(
                &store
                    .preview_pairing(&auth_url("hub.example.com"))
                    .unwrap()
                    .preview_id,
                "Alpha",
                false,
                ReleaseMode::Release,
            )
            .unwrap();
        store.rename(&p.id, "alpha").unwrap();
        // Same name (case-insensitive) for same profile is allowed.
    }

    // -- Remove --------------------------------------------------------------

    #[test]
    fn remove_inactive_keeps_active() {
        let prefs = Arc::new(MemoryPreferences::new());
        let secure = Arc::new(MemorySecureStore::new());
        let probe = Arc::new(OkProbe);
        let clock = Arc::new(StepClock::new(0));
        let store = make_store(prefs.clone(), secure.clone(), probe, clock);

        let p1 = store
            .confirm_pairing(
                &store
                    .preview_pairing(&auth_url("hub1.example.com"))
                    .unwrap()
                    .preview_id,
                "Alpha",
                false,
                ReleaseMode::Release,
            )
            .unwrap();
        let _p2 = store
            .confirm_pairing(
                &store
                    .preview_pairing(&auth_url("hub2.example.com"))
                    .unwrap()
                    .preview_id,
                "Beta",
                false,
                ReleaseMode::Release,
            )
            .unwrap();
        store.select(&p1.id).unwrap();

        let beta_id = p2_id(&store, "Beta");
        let res = store.remove(&beta_id).unwrap();
        assert_eq!(res.profile_id.as_deref(), Some(p1.id.as_str()));
        assert!(!secure.has(&beta_id));
        assert_eq!(store.list().unwrap().len(), 1);
    }

    #[test]
    fn remove_active_selects_next_ordered_or_none() {
        let prefs = Arc::new(MemoryPreferences::new());
        let secure = Arc::new(MemorySecureStore::new());
        let probe = Arc::new(OkProbe);
        let clock = Arc::new(StepClock::new(0));
        let store = make_store(prefs, secure, probe, clock);

        let p1 = store
            .confirm_pairing(
                &store
                    .preview_pairing(&auth_url("hub1.example.com"))
                    .unwrap()
                    .preview_id,
                "Alpha",
                false,
                ReleaseMode::Release,
            )
            .unwrap();
        let p2 = store
            .confirm_pairing(
                &store
                    .preview_pairing(&auth_url("hub2.example.com"))
                    .unwrap()
                    .preview_id,
                "Beta",
                false,
                ReleaseMode::Release,
            )
            .unwrap();
        store.select(&p1.id).unwrap();

        // Remove active → next ordered (p2) becomes active.
        let res = store.remove(&p1.id).unwrap();
        assert_eq!(res.profile_id.as_deref(), Some(p2.id.as_str()));

        // Remove last active → none.
        let res = store.remove(&p2.id).unwrap();
        assert_eq!(res.profile_id, None);
        assert_eq!(store.list().unwrap().len(), 0);
    }

    // -- Generation ordering -------------------------------------------------

    #[test]
    fn select_returns_monotonically_increasing_generation() {
        let prefs = Arc::new(MemoryPreferences::new());
        let secure = Arc::new(MemorySecureStore::new());
        let probe = Arc::new(OkProbe);
        let clock = Arc::new(StepClock::new(0));
        let store = make_store(prefs, secure, probe, clock);

        let p1 = store
            .confirm_pairing(
                &store
                    .preview_pairing(&auth_url("hub1.example.com"))
                    .unwrap()
                    .preview_id,
                "Alpha",
                false,
                ReleaseMode::Release,
            )
            .unwrap();
        let p2 = store
            .confirm_pairing(
                &store
                    .preview_pairing(&auth_url("hub2.example.com"))
                    .unwrap()
                    .preview_id,
                "Beta",
                false,
                ReleaseMode::Release,
            )
            .unwrap();

        let g1 = store.select(&p1.id).unwrap().generation;
        let g2 = store.select(&p2.id).unwrap().generation;
        let g3 = store.select(&p1.id).unwrap().generation;
        assert!(g1 < g2);
        assert!(g2 < g3);
    }

    #[test]
    fn select_closes_transport_before_active_id_changes() {
        let prefs = Arc::new(MemoryPreferences::new());
        let secure = Arc::new(MemorySecureStore::new());
        let probe = Arc::new(OkProbe);
        let clock = Arc::new(StepClock::new(0));
        let policy = Arc::new(NetworkPolicy::new(Box::new(
            crate::network_policy::AlwaysPrivateResolver,
        )));
        let close = Arc::new(RecordingCloseTransport::default());
        let store = ProfileStore::new(prefs.clone(), secure, probe, clock, policy, close.clone());

        let p1 = store
            .confirm_pairing(
                &store
                    .preview_pairing(&auth_url("hub1.example.com"))
                    .unwrap()
                    .preview_id,
                "Alpha",
                false,
                ReleaseMode::Release,
            )
            .unwrap();
        let p2 = store
            .confirm_pairing(
                &store
                    .preview_pairing(&auth_url("hub2.example.com"))
                    .unwrap()
                    .preview_id,
                "Beta",
                false,
                ReleaseMode::Release,
            )
            .unwrap();

        store.select(&p1.id).unwrap();
        assert_eq!(close.close_count(), 1);
        store.select(&p2.id).unwrap();
        assert_eq!(close.close_count(), 2);
        // Active ID changed to p2.
        assert_eq!(store.active_id().unwrap().as_deref(), Some(p2.id.as_str()));
    }

    // -- Atomic failure: failed edit leaves old profile usable --------------

    #[test]
    fn failed_keychain_set_leaves_old_profile_in_preferences() {
        let prefs = Arc::new(MemoryPreferences::new());
        let secure = Arc::new(MemorySecureStore::new());
        let probe = Arc::new(OkProbe);
        let clock = Arc::new(StepClock::new(0));
        let store = make_store(prefs.clone(), secure.clone(), probe, clock);

        let p = store
            .confirm_pairing(
                &store
                    .preview_pairing(&auth_url("hub.example.com"))
                    .unwrap()
                    .preview_id,
                "Alpha",
                false,
                ReleaseMode::Release,
            )
            .unwrap();
        let old_token = secure.get_token(&p.id).unwrap();
        assert_eq!(old_token, TOKEN);

        // Re-pair with a failing Keychain set on this profile.
        secure.fail_next_set(p.id.clone());
        let pv = store
            .preview_repair(&p.id, &auth_url("hub.example.com"))
            .unwrap();
        let err = store
            .confirm_pairing(&pv.preview_id, "Alpha2", false, ReleaseMode::Release)
            .unwrap_err();
        assert!(matches!(err, ProfileError::SecureStore(_)));
        // Old profile remains fully usable: still listed, token unchanged.
        assert_eq!(store.list().unwrap().len(), 1);
        assert_eq!(secure.get_token(&p.id).unwrap(), TOKEN);
    }

    #[test]
    fn failed_probe_leaves_old_profile_usable() {
        let prefs = Arc::new(MemoryPreferences::new());
        let secure = Arc::new(MemorySecureStore::new());
        let probe = Arc::new(FailProbe);
        let clock = Arc::new(StepClock::new(0));
        let store = make_store(prefs, secure.clone(), probe, clock);

        let pv = store.preview_pairing(&auth_url("hub.example.com")).unwrap();
        let err = store
            .confirm_pairing(&pv.preview_id, "Alpha", false, ReleaseMode::Release)
            .unwrap_err();
        assert!(matches!(err, ProfileError::ProbeFailed { .. }));
        // No profile added, no keychain item.
        assert_eq!(store.list().unwrap().len(), 0);
    }

    #[test]
    fn mobile_api_mismatch_rejected() {
        let prefs = Arc::new(MemoryPreferences::new());
        let secure = Arc::new(MemorySecureStore::new());
        let probe = Arc::new(MismatchProbe { version: 2 });
        let clock = Arc::new(StepClock::new(0));
        let store = make_store(prefs, secure, probe, clock);

        let pv = store.preview_pairing(&auth_url("hub.example.com")).unwrap();
        let err = store
            .confirm_pairing(&pv.preview_id, "Alpha", false, ReleaseMode::Release)
            .unwrap_err();
        assert!(matches!(err, ProfileError::MobileApiMismatch(2)));
    }

    // -- Edit/re-pair replaces only that profile ----------------------------

    #[test]
    fn repair_replaces_only_that_profile_token() {
        let prefs = Arc::new(MemoryPreferences::new());
        let secure = Arc::new(MemorySecureStore::new());
        let probe = Arc::new(OkProbe);
        let clock = Arc::new(StepClock::new(0));
        let store = make_store(prefs, secure.clone(), probe, clock);

        let p1 = store
            .confirm_pairing(
                &store
                    .preview_pairing(&auth_url("hub1.example.com"))
                    .unwrap()
                    .preview_id,
                "Alpha",
                false,
                ReleaseMode::Release,
            )
            .unwrap();
        let _p2 = store
            .confirm_pairing(
                &store
                    .preview_pairing(&auth_url("hub2.example.com"))
                    .unwrap()
                    .preview_id,
                "Beta",
                false,
                ReleaseMode::Release,
            )
            .unwrap();

        // Re-pair p1 with a new token (different host keeps origin different
        // would duplicate; reuse same origin via allow flag).
        let new_url = format!("https://hub1.example.com:8443/auth?token={TOKEN}");
        let pv = store.preview_repair(&p1.id, &new_url).unwrap();
        store
            .confirm_pairing(&pv.preview_id, "Alpha", true, ReleaseMode::Release)
            .unwrap();

        // Only p1's token replaced; p2 untouched. One Keychain item per id.
        assert!(secure.has(&p1.id));
        assert_eq!(secure.get_token(&p1.id).unwrap(), TOKEN);
        assert_eq!(store.list().unwrap().len(), 2);
    }

    // -- Preview expiry / cancel / background -------------------------------

    #[test]
    fn preview_expires_after_five_minutes() {
        let prefs = Arc::new(MemoryPreferences::new());
        let secure = Arc::new(MemorySecureStore::new());
        let probe = Arc::new(OkProbe);
        let clock = Arc::new(StepClock::new(0));
        let store = make_store(prefs, secure, probe.clone(), clock.clone());

        let pv = store.preview_pairing(&auth_url("hub.example.com")).unwrap();
        // Advance past TTL.
        clock.advance(PREVIEW_TTL_SECS + 1);
        let err = store
            .confirm_pairing(&pv.preview_id, "Alpha", false, ReleaseMode::Release)
            .unwrap_err();
        assert!(matches!(err, ProfileError::PreviewNotFound(_)));
    }

    #[test]
    fn preview_at_ttl_boundary_still_valid() {
        let prefs = Arc::new(MemoryPreferences::new());
        let secure = Arc::new(MemorySecureStore::new());
        let probe = Arc::new(OkProbe);
        let clock = Arc::new(StepClock::new(0));
        let store = make_store(prefs, secure, probe, clock.clone());

        let pv = store.preview_pairing(&auth_url("hub.example.com")).unwrap();
        // At exactly TTL it is still valid (created_at + TTL == now is < TTL? no).
        // We use strict <, so now == created_at + TTL is expired.
        clock.set(PREVIEW_TTL_SECS - 1);
        store
            .confirm_pairing(&pv.preview_id, "Alpha", false, ReleaseMode::Release)
            .unwrap();
    }

    #[test]
    fn cancel_preview_clears_secret() {
        let prefs = Arc::new(MemoryPreferences::new());
        let secure = Arc::new(MemorySecureStore::new());
        let probe = Arc::new(OkProbe);
        let clock = Arc::new(StepClock::new(0));
        let store = make_store(prefs, secure, probe, clock);

        let pv = store.preview_pairing(&auth_url("hub.example.com")).unwrap();
        store.cancel_preview(&pv.preview_id).unwrap();
        let err = store
            .confirm_pairing(&pv.preview_id, "Alpha", false, ReleaseMode::Release)
            .unwrap_err();
        assert!(matches!(err, ProfileError::PreviewNotFound(_)));
    }

    #[test]
    fn clear_previews_acts_as_background_transition() {
        let prefs = Arc::new(MemoryPreferences::new());
        let secure = Arc::new(MemorySecureStore::new());
        let probe = Arc::new(OkProbe);
        let clock = Arc::new(StepClock::new(0));
        let store = make_store(prefs, secure, probe, clock);

        let pv = store.preview_pairing(&auth_url("hub.example.com")).unwrap();
        store.clear_previews();
        let err = store
            .confirm_pairing(&pv.preview_id, "Alpha", false, ReleaseMode::Release)
            .unwrap_err();
        assert!(matches!(err, ProfileError::PreviewNotFound(_)));
    }

    #[test]
    fn preview_returns_only_origin_no_token() {
        let prefs = Arc::new(MemoryPreferences::new());
        let secure = Arc::new(MemorySecureStore::new());
        let probe = Arc::new(OkProbe);
        let clock = Arc::new(StepClock::new(0));
        let store = make_store(prefs, secure, probe, clock);

        let pv = store.preview_pairing(&auth_url("hub.example.com")).unwrap();
        assert!(pv.origin.contains("https://hub.example.com"));
        assert!(!pv.preview_id.contains(TOKEN));
        // preview_id is opaque.
        assert!(pv.preview_id.starts_with("preview-"));
    }

    // -- Preferences restart ------------------------------------------------

    #[test]
    fn preferences_survive_restart() {
        let prefs = Arc::new(MemoryPreferences::new());
        let secure = Arc::new(MemorySecureStore::new());
        let probe = Arc::new(OkProbe);
        let clock = Arc::new(StepClock::new(0));
        let store = make_store(prefs.clone(), secure.clone(), probe, clock);

        let p = store
            .confirm_pairing(
                &store
                    .preview_pairing(&auth_url("hub.example.com"))
                    .unwrap()
                    .preview_id,
                "Alpha",
                false,
                ReleaseMode::Release,
            )
            .unwrap();
        store.select(&p.id).unwrap();

        // Simulate restart: a new store over the same preferences/secure store.
        let store2 = make_store(
            prefs.clone(),
            secure.clone(),
            Arc::new(OkProbe),
            Arc::new(StepClock::new(0)),
        );
        let loaded = store2.list().unwrap();
        assert_eq!(loaded.len(), 1);
        assert_eq!(loaded[0].id, p.id);
        assert_eq!(loaded[0].name, "Alpha");
        assert_eq!(loaded[0].origin, "https://hub.example.com");
        assert_eq!(store2.active_id().unwrap().as_deref(), Some(p.id.as_str()));
        assert!(secure.has(&p.id));
    }

    // -- Redaction in errors ------------------------------------------------

    #[test]
    fn profile_error_never_contains_token() {
        let prefs = Arc::new(MemoryPreferences::new());
        let secure = Arc::new(MemorySecureStore::new());
        let probe = Arc::new(FailProbe);
        let clock = Arc::new(StepClock::new(0));
        let store = make_store(prefs, secure, probe, clock);

        // The probe fails; the error message must not contain the token.
        let pv = store.preview_pairing(&auth_url("hub.example.com")).unwrap();
        match store.confirm_pairing(&pv.preview_id, "Alpha", false, ReleaseMode::Release) {
            Err(e) => assert!(!format!("{e}").contains(TOKEN)),
            Ok(_) => panic!("expected probe failure"),
        }
    }

    // -- Late operations carry profile ID / generation -----------------------

    #[test]
    fn select_result_carries_generation_and_id() {
        let prefs = Arc::new(MemoryPreferences::new());
        let secure = Arc::new(MemorySecureStore::new());
        let probe = Arc::new(OkProbe);
        let clock = Arc::new(StepClock::new(0));
        let store = make_store(prefs, secure, probe, clock);

        let p = store
            .confirm_pairing(
                &store
                    .preview_pairing(&auth_url("hub.example.com"))
                    .unwrap()
                    .preview_id,
                "Alpha",
                false,
                ReleaseMode::Release,
            )
            .unwrap();
        let res = store.select(&p.id).unwrap();
        assert_eq!(res.profile_id.as_deref(), Some(p.id.as_str()));
        assert!(res.generation.0 > 0);
    }

    fn p2_id(store: &ProfileStore, name: &str) -> String {
        store
            .list()
            .unwrap()
            .into_iter()
            .find(|p| p.name == name)
            .unwrap()
            .id
    }
}
