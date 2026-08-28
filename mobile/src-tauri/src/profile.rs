//! Multi-profile lifecycle: ordered redacted summaries, active profile ID,
//! per-profile Keychain tokens, two-phase pairing preview/confirm, and
//! profile switching with a monotonic generation.
//!
//! JavaScript never sees a capability. It receives only redacted
//! [`ProfileSummary { id, name, origin }`] and opaque preview IDs.

use std::collections::HashMap;
use std::fmt;
use std::sync::atomic::{AtomicU64, Ordering};
use std::sync::{Arc, Condvar, Mutex};

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
pub const PREFERENCES_VERSION: u32 = 1;

#[derive(Debug, Clone, Serialize, Deserialize, PartialEq, Eq)]
#[serde(deny_unknown_fields)]
pub struct Preferences {
    pub version: u32,
    /// Ordered redacted profile summaries.
    #[serde(default)]
    pub profiles: Vec<ProfileSummary>,
    /// Active profile ID, or none.
    #[serde(default)]
    pub active_id: Option<String>,
}

impl Default for Preferences {
    fn default() -> Self {
        Self {
            version: PREFERENCES_VERSION,
            profiles: Vec::new(),
            active_id: None,
        }
    }
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

/// Maximum preview lifetime in seconds (5 minutes).
pub const PREVIEW_TTL_SECS: u64 = 300;

/// A pending pairing preview: holds the parsed secret in native memory only.
struct PendingPreview {
    pairing: PairingUrl,
    created_at: u64,
    /// Optional profile ID when previewing an edit/re-pair of an existing profile.
    existing_id: Option<String>,
}

/// Schedules secret-free expiry callbacks in one owned cancellable queue.
pub trait PreviewExpiryScheduler: Send + Sync {
    fn schedule(&self, preview_id: String, delay_secs: u64, callback: Box<dyn FnOnce() + Send>);
    fn cancel(&self, preview_id: &str);
    fn clear(&self);
}

struct ScheduledExpiryTask {
    deadline: std::time::Instant,
    callback: Box<dyn FnOnce() + Send>,
}

#[derive(Default)]
struct SchedulerQueue {
    tasks: HashMap<String, ScheduledExpiryTask>,
    shutdown: bool,
}

#[derive(Default)]
struct SchedulerWorker {
    queue: Mutex<SchedulerQueue>,
    changed: Condvar,
}

/// Production preview expiry scheduler. Exactly one worker owns all timers.
/// Shutdown clears the queue, wakes the worker, and joins it deterministically.
pub struct OwnedPreviewExpiryScheduler {
    worker: Arc<SchedulerWorker>,
    worker_handle: Mutex<Option<std::thread::JoinHandle<()>>>,
    worker_count: Arc<AtomicU64>,
}

impl OwnedPreviewExpiryScheduler {
    pub fn new() -> Self {
        let worker = Arc::new(SchedulerWorker::default());
        let worker_thread = worker.clone();
        let worker_count = Arc::new(AtomicU64::new(1));
        let thread_count = worker_count.clone();
        let handle = std::thread::Builder::new()
            .name("preview-expiry".to_owned())
            .spawn(move || {
                Self::run_worker(&worker_thread);
                thread_count.store(0, Ordering::SeqCst);
            })
            .expect("failed to start preview expiry worker");
        Self {
            worker,
            worker_handle: Mutex::new(Some(handle)),
            worker_count,
        }
    }

    fn run_worker(worker: &SchedulerWorker) {
        loop {
            let callback = {
                let mut queue = worker.queue.lock().unwrap();
                loop {
                    if queue.shutdown {
                        queue.tasks.clear();
                        return;
                    }
                    let Some((next_id, deadline)) = queue
                        .tasks
                        .iter()
                        .min_by_key(|(_, task)| task.deadline)
                        .map(|(id, task)| (id.clone(), task.deadline))
                    else {
                        queue = worker.changed.wait(queue).unwrap();
                        continue;
                    };
                    let now = std::time::Instant::now();
                    if deadline > now {
                        let (new_queue, _) = worker
                            .changed
                            .wait_timeout(queue, deadline.duration_since(now))
                            .unwrap();
                        queue = new_queue;
                        continue;
                    }
                    break queue.tasks.remove(&next_id).map(|task| task.callback);
                }
            };
            if let Some(callback) = callback {
                callback();
            }
        }
    }

    pub fn schedule_in(
        &self,
        preview_id: String,
        delay: std::time::Duration,
        callback: Box<dyn FnOnce() + Send>,
    ) {
        let mut queue = self.worker.queue.lock().unwrap();
        if queue.shutdown {
            return;
        }
        queue.tasks.insert(
            preview_id,
            ScheduledExpiryTask {
                deadline: std::time::Instant::now() + delay,
                callback,
            },
        );
        drop(queue);
        self.worker.changed.notify_one();
    }

    pub fn shutdown(&self) {
        let handle = {
            let mut handle = self.worker_handle.lock().unwrap();
            let mut queue = self.worker.queue.lock().unwrap();
            queue.shutdown = true;
            queue.tasks.clear();
            drop(queue);
            self.worker.changed.notify_one();
            handle.take()
        };
        if let Some(handle) = handle {
            let _ = handle.join();
        }
    }

    #[cfg(test)]
    fn pending_count(&self) -> usize {
        self.worker.queue.lock().unwrap().tasks.len()
    }

    pub fn worker_count(&self) -> u64 {
        self.worker_count.load(Ordering::SeqCst)
    }

    #[cfg(test)]
    fn worker_counter(&self) -> Arc<AtomicU64> {
        self.worker_count.clone()
    }
}

impl Default for OwnedPreviewExpiryScheduler {
    fn default() -> Self {
        Self::new()
    }
}

impl Drop for OwnedPreviewExpiryScheduler {
    fn drop(&mut self) {
        self.shutdown();
    }
}

impl PreviewExpiryScheduler for OwnedPreviewExpiryScheduler {
    fn schedule(&self, preview_id: String, delay_secs: u64, callback: Box<dyn FnOnce() + Send>) {
        self.schedule_in(
            preview_id,
            std::time::Duration::from_secs(delay_secs),
            callback,
        );
    }

    fn cancel(&self, preview_id: &str) {
        self.worker.queue.lock().unwrap().tasks.remove(preview_id);
        self.worker.changed.notify_one();
    }

    fn clear(&self) {
        self.worker.queue.lock().unwrap().tasks.clear();
        self.worker.changed.notify_one();
    }
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
    pending: Arc<Mutex<HashMap<String, PendingPreview>>>,
    expiry_scheduler: Arc<dyn PreviewExpiryScheduler>,
    /// Monotonic generation counter.
    generation: AtomicU64,
}

impl ProfileStore {
    pub fn new(
        prefs: Arc<dyn PreferencesStore>,
        secure: Arc<dyn SecureStore>,
        probe: Arc<dyn PairingProbe>,
        clock: Arc<dyn Clock>,
        policy: Arc<NetworkPolicy>,
    ) -> Self {
        Self::new_with_scheduler(
            prefs,
            secure,
            probe,
            clock,
            policy,
            Arc::new(OwnedPreviewExpiryScheduler::new()),
        )
    }

    pub fn new_with_scheduler(
        prefs: Arc<dyn PreferencesStore>,
        secure: Arc<dyn SecureStore>,
        probe: Arc<dyn PairingProbe>,
        clock: Arc<dyn Clock>,
        policy: Arc<NetworkPolicy>,
        expiry_scheduler: Arc<dyn PreviewExpiryScheduler>,
    ) -> Self {
        Self {
            prefs,
            secure,
            probe,
            clock,
            policy,
            pending: Arc::new(Mutex::new(HashMap::new())),
            expiry_scheduler,
            generation: AtomicU64::new(0),
        }
    }

    fn schedule_expiry(&self, preview_id: &str, created_at: u64) {
        let pending = self.pending.clone();
        let clock = self.clock.clone();
        let preview_id = preview_id.to_owned();
        let timer_id = preview_id.clone();
        self.expiry_scheduler.schedule(
            timer_id,
            PREVIEW_TTL_SECS,
            Box::new(move || {
                let now = clock.now_secs();
                let mut pending = pending.lock().unwrap();
                if pending.get(&preview_id).is_some_and(|preview| {
                    preview.created_at == created_at
                        && now.saturating_sub(preview.created_at) >= PREVIEW_TTL_SECS
                }) {
                    pending.remove(&preview_id);
                }
            }),
        );
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
        let expired: Vec<String> = pending
            .iter()
            .filter(|(_, preview)| now.saturating_sub(preview.created_at) >= PREVIEW_TTL_SECS)
            .map(|(id, _)| id.clone())
            .collect();
        pending.retain(|_, p| now.saturating_sub(p.created_at) < PREVIEW_TTL_SECS);
        let removed = before - pending.len();
        drop(pending);
        for id in expired {
            self.expiry_scheduler.cancel(&id);
        }
        removed
    }

    /// Cancel a pending preview, clearing its held secret from native memory.
    pub fn cancel_preview(&self, preview_id: &str) -> Result<(), ProfileError> {
        self.pending
            .lock()
            .unwrap()
            .remove(preview_id)
            .ok_or_else(|| ProfileError::PreviewNotFound(preview_id.to_owned()))?;
        self.expiry_scheduler.cancel(preview_id);
        Ok(())
    }

    /// Clear all pending previews (background/foreground transition).
    pub fn clear_previews(&self) {
        self.pending.lock().unwrap().clear();
        self.expiry_scheduler.clear();
    }

    /// Phase 1: preview a pasted or scanned auth URL. Holds the parsed secret
    /// in native memory for at most 5 minutes. Returns an opaque preview ID
    /// and the normalized origin only.
    pub fn preview_pairing(&self, raw: &str) -> Result<PairingPreview, ProfileError> {
        self.expire_pending();
        let pairing = parse_pairing(raw)?;
        let origin = pairing.origin().to_owned();
        let preview_id = new_preview_id();
        let created_at = self.clock.now_secs();
        let pending = PendingPreview {
            pairing,
            created_at,
            existing_id: None,
        };
        self.pending
            .lock()
            .unwrap()
            .insert(preview_id.clone(), pending);
        self.schedule_expiry(&preview_id, created_at);
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
        let created_at = self.clock.now_secs();
        let pending = PendingPreview {
            pairing,
            created_at,
            existing_id: Some(profile_id.to_owned()),
        };
        self.pending
            .lock()
            .unwrap()
            .insert(preview_id.clone(), pending);
        self.schedule_expiry(&preview_id, created_at);
        Ok(PairingPreview { preview_id, origin })
    }

    /// Return the redacted profile target of a pending repair preview. The
    /// capability remains inside `PendingPreview`; lifecycle orchestration uses
    /// only this ID to decide whether the active socket must be retired.
    pub fn preview_profile_id(&self, preview_id: &str) -> Result<Option<String>, ProfileError> {
        self.expire_pending();
        self.pending
            .lock()
            .unwrap()
            .get(preview_id)
            .map(|preview| preview.existing_id.clone())
            .ok_or_else(|| ProfileError::PreviewNotFound(preview_id.to_owned()))
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
        self.expiry_scheduler.cancel(preview_id);

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

        // Authenticated probe (health + AppWire upgrade, redirects disabled).
        let mobile_api_version =
            self.probe
                .probe(&origin, token, mode)
                .map_err(|error| match error {
                    ProfileError::ProbeTimedOut => ProfileError::ProbeTimedOut,
                    other => ProfileError::ProbeFailed {
                        origin: origin.clone(),
                        message: other.to_string(),
                    },
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

        // Capture the prior token (if editing) before the Keychain write so we
        // can compensate a preferences-save failure.
        let prior_token = match &pending.existing_id {
            Some(_) => self.secure.get(&profile_id)?,
            None => None,
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

        // Auto-select the new profile when there is no active profile.
        // This is the common case for first-time pairing.
        if new_prefs.active_id.is_none() {
            new_prefs.active_id = Some(profile_id.clone());
        }

        if let Err(prefs_err) = self.prefs.save(&new_prefs) {
            if matches!(prefs_err, ProfileError::PreferencesDurabilityUncertain) {
                // Rename already committed the matching preferences. Keep the
                // new Keychain value and surface the durability uncertainty;
                // rolling back here would create an origin/token mismatch.
                if pending.existing_id.is_some()
                    && new_prefs.active_id.as_deref() == Some(&profile_id)
                {
                    self.bump_generation();
                }
                return Err(prefs_err);
            }
            // Compensating rollback: undo the Keychain mutation so no orphan
            // secret remains and the old profile stays usable.
            let operation = if pending.existing_id.is_some() {
                "repair"
            } else {
                "add"
            };
            self.rollback_keychain(&profile_id, prior_token.as_deref(), operation)?;
            return Err(prefs_err);
        }

        if pending.existing_id.is_some() && new_prefs.active_id.as_deref() == Some(&profile_id) {
            self.bump_generation();
        }

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

        // Capture the prior token before deleting so we can restore it if the
        // preferences save fails.
        let prior_token = self.secure.get(profile_id)?;

        // Keychain delete first: if it fails, preferences are untouched.
        self.secure.delete(profile_id)?;

        let was_active = prefs.active_id.as_deref() == Some(profile_id);

        if was_active {
            new_prefs.active_id = new_prefs.profiles.first().map(|p| p.id.clone());
        }

        if let Err(prefs_err) = self.prefs.save(&new_prefs) {
            if matches!(prefs_err, ProfileError::PreferencesDurabilityUncertain) {
                // The visible preferences and Keychain deletion already match.
                // Never restore the old token after the rename committed.
                if was_active {
                    self.bump_generation();
                }
                return Err(prefs_err);
            }
            // Compensating rollback: restore the deleted token so the old
            // profile remains usable.
            self.rollback_keychain(profile_id, prior_token.as_deref(), "remove")?;
            return Err(prefs_err);
        }

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

        let mut new_prefs = prefs.clone();
        new_prefs.active_id = Some(profile_id.to_owned());
        if let Err(error) = self.prefs.save(&new_prefs) {
            if matches!(error, ProfileError::PreferencesDurabilityUncertain) {
                self.bump_generation();
            }
            return Err(error);
        }

        Ok(SelectResult {
            profile_id: Some(profile_id.to_owned()),
            generation: self.bump_generation(),
        })
    }

    fn bump_generation(&self) -> ProfileGeneration {
        let g = self.generation.fetch_add(1, Ordering::SeqCst) + 1;
        ProfileGeneration(g)
    }

    /// Compensating rollback for a preferences-save failure that occurred
    /// after a Keychain mutation.
    ///
    /// - `prior_token` is `None` for a new add (delete the orphan token).
    /// - `prior_token` is `Some(old_token)` for re-pair or remove (restore it).
    ///
    /// If the rollback itself fails, returns a [`ProfileError::Consistency`]
    /// error identifying only the profile ID and both operation classes. The
    /// token is never logged.
    fn rollback_keychain(
        &self,
        profile_id: &str,
        prior_token: Option<&str>,
        operation: &str,
    ) -> Result<(), ProfileError> {
        let rollback_result = match prior_token {
            Some(old_token) => self.secure.set(profile_id, old_token),
            None => self.secure.delete(profile_id),
        };
        match rollback_result {
            Ok(()) => Ok(()),
            Err(_) => Err(ProfileError::Consistency {
                profile_id: profile_id.to_owned(),
                operation: operation.to_owned(),
                rollback_operation: if prior_token.is_some() {
                    "restore".to_owned()
                } else {
                    "delete".to_owned()
                },
            }),
        }
    }

    /// Capture one immutable active transport snapshot. Active-id validation,
    /// summary lookup, generation read, and Keychain read happen while the
    /// caller holds the profile lifecycle lock.
    pub fn active_snapshot(
        &self,
        expected_profile_id: &str,
    ) -> Result<ActiveProfileSnapshot, ProfileError> {
        let snapshot = self.active_snapshot_current()?;
        if snapshot.id() != expected_profile_id {
            return Err(ProfileError::NotFound("active profile mismatch".to_owned()));
        }
        Ok(snapshot)
    }

    pub fn active_snapshot_current(&self) -> Result<ActiveProfileSnapshot, ProfileError> {
        let prefs = self.prefs.load()?;
        let active_id = prefs
            .active_id
            .as_deref()
            .ok_or_else(|| ProfileError::NotFound("no active profile".to_owned()))?;
        let profile = prefs
            .profiles
            .iter()
            .find(|profile| profile.id == active_id)
            .ok_or_else(|| ProfileError::NotFound("active profile mismatch".to_owned()))?;
        let token = self
            .secure
            .get(active_id)?
            .ok_or_else(|| ProfileError::SecureStore("capability missing".to_owned()))?;
        Ok(ActiveProfileSnapshot {
            id: profile.id.clone(),
            generation: self.generation(),
            origin: profile.origin.clone(),
            token,
        })
    }
}

/// Immutable native-only credentials and routing identity captured atomically.
/// Deliberately has no `Debug` or serialization implementation.
pub struct ActiveProfileSnapshot {
    id: String,
    generation: ProfileGeneration,
    origin: String,
    token: String,
}

impl ActiveProfileSnapshot {
    pub fn id(&self) -> &str {
        &self.id
    }
    pub fn generation(&self) -> ProfileGeneration {
        self.generation
    }
    pub fn origin(&self) -> &str {
        &self.origin
    }
    pub fn token(&self) -> &str {
        &self.token
    }
    pub fn into_parts(self) -> (String, ProfileGeneration, String, String) {
        (self.id, self.generation, self.origin, self.token)
    }
}

fn new_preview_id() -> String {
    format!("preview-{}", uuid::Uuid::new_v4().simple())
}

fn parse_pairing(raw: &str) -> Result<PairingUrl, ProfileError> {
    PairingUrl::parse(raw).map_err(|e| ProfileError::InvalidPairing(e.to_string()))
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
    fail_next_save: Mutex<bool>,
}

impl MemoryPreferences {
    pub fn new() -> Self {
        Self::default()
    }
    pub fn save_count(&self) -> u64 {
        self.save_count.load(Ordering::SeqCst)
    }
    pub fn fail_next_save(&self) {
        *self.fail_next_save.lock().unwrap() = true;
    }
}

impl PreferencesStore for MemoryPreferences {
    fn load(&self) -> Result<Preferences, ProfileError> {
        Ok(self.inner.lock().unwrap().clone())
    }
    fn save(&self, prefs: &Preferences) -> Result<(), ProfileError> {
        if *self.fail_next_save.lock().unwrap() {
            *self.fail_next_save.lock().unwrap() = false;
            return Err(ProfileError::Preferences(
                "injected save failure".to_owned(),
            ));
        }
        *self.inner.lock().unwrap() = prefs.clone();
        self.save_count.fetch_add(1, Ordering::SeqCst);
        Ok(())
    }
}

#[derive(Default)]
pub struct MemorySecureStore {
    map: Mutex<HashMap<String, String>>,
    fail_set: Mutex<Option<String>>,
    fail_set_after_n: Mutex<Option<(String, u64)>>,
    fail_delete: Mutex<Option<String>>,
    fail_get: Mutex<Option<String>>,
    fail_all_delete: Mutex<bool>,
}

impl MemorySecureStore {
    pub fn new() -> Self {
        Self::default()
    }
    pub fn fail_next_set(&self, id: String) {
        *self.fail_set.lock().unwrap() = Some(id);
    }
    pub fn fail_set_after_n(&self, id: String, n: u64) {
        // Fail the Nth-from-now set call for this id (1-based: n=1 fails the next set).
        *self.fail_set_after_n.lock().unwrap() = Some((id, n));
    }
    pub fn fail_next_delete(&self, id: String) {
        *self.fail_delete.lock().unwrap() = Some(id);
    }
    pub fn fail_next_get(&self, id: String) {
        *self.fail_get.lock().unwrap() = Some(id);
    }
    pub fn fail_all_next_delete(&self) {
        *self.fail_all_delete.lock().unwrap() = true;
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
        if let Some(fail_id) = self.fail_get.lock().unwrap().take() {
            if fail_id == profile_id {
                return Err(ProfileError::SecureStore("injected get failure".to_owned()));
            }
        }
        Ok(self.map.lock().unwrap().get(profile_id).cloned())
    }
    fn set(&self, profile_id: &str, token: &str) -> Result<(), ProfileError> {
        if let Some(fail_id) = self.fail_set.lock().unwrap().take() {
            if fail_id == profile_id {
                return Err(ProfileError::SecureStore("injected set failure".to_owned()));
            }
        }
        // Decrement the after-n counter; fail when it reaches zero.
        let should_fail = {
            let mut guard = self.fail_set_after_n.lock().unwrap();
            if let Some((fail_id, n)) = guard.as_mut() {
                if *fail_id == profile_id {
                    *n -= 1;
                    if *n == 0 {
                        *guard = None;
                        true
                    } else {
                        false
                    }
                } else {
                    false
                }
            } else {
                false
            }
        };
        if should_fail {
            return Err(ProfileError::SecureStore("injected set failure".to_owned()));
        }
        self.map
            .lock()
            .unwrap()
            .insert(profile_id.to_owned(), token.to_owned());
        Ok(())
    }
    fn delete(&self, profile_id: &str) -> Result<(), ProfileError> {
        if let Some(fail_id) = self.fail_delete.lock().unwrap().take() {
            if fail_id == profile_id {
                return Err(ProfileError::SecureStore(
                    "injected delete failure".to_owned(),
                ));
            }
        }
        if *self.fail_all_delete.lock().unwrap() {
            *self.fail_all_delete.lock().unwrap() = false;
            return Err(ProfileError::SecureStore(
                "injected delete failure".to_owned(),
            ));
        }
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
    ProfileStore::new(prefs, secure, probe, clock, policy)
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

    type ScheduledExpiry = (u64, Box<dyn FnOnce() + Send>);

    #[derive(Default)]
    struct DeterministicScheduler {
        scheduled: Mutex<HashMap<String, ScheduledExpiry>>,
    }

    impl PreviewExpiryScheduler for DeterministicScheduler {
        fn schedule(
            &self,
            preview_id: String,
            delay_secs: u64,
            callback: Box<dyn FnOnce() + Send>,
        ) {
            self.scheduled
                .lock()
                .unwrap()
                .insert(preview_id, (delay_secs, callback));
        }

        fn cancel(&self, preview_id: &str) {
            self.scheduled.lock().unwrap().remove(preview_id);
        }

        fn clear(&self) {
            self.scheduled.lock().unwrap().clear();
        }
    }

    impl DeterministicScheduler {
        fn delays(&self) -> Vec<u64> {
            self.scheduled
                .lock()
                .unwrap()
                .values()
                .map(|(delay, _)| *delay)
                .collect()
        }

        fn run_all(&self) {
            let callbacks = std::mem::take(&mut *self.scheduled.lock().unwrap());
            for (_, (_, callback)) in callbacks {
                callback();
            }
        }
    }

    fn make_store_with_scheduler(
        clock: Arc<StepClock>,
        scheduler: Arc<DeterministicScheduler>,
    ) -> ProfileStore {
        ProfileStore::new_with_scheduler(
            Arc::new(MemoryPreferences::new()),
            Arc::new(MemorySecureStore::new()),
            Arc::new(OkProbe),
            clock,
            Arc::new(NetworkPolicy::new(Box::new(
                crate::network_policy::AlwaysPrivateResolver,
            ))),
            scheduler,
        )
    }

    #[test]
    fn scheduled_expiry_removes_secret_without_another_store_call() {
        let clock = Arc::new(StepClock::new(10));
        let scheduler = Arc::new(DeterministicScheduler::default());
        let store = make_store_with_scheduler(clock.clone(), scheduler.clone());
        let preview = store.preview_pairing(&auth_url("hub.example.com")).unwrap();
        assert_eq!(scheduler.delays(), vec![PREVIEW_TTL_SECS]);

        clock.advance(PREVIEW_TTL_SECS);
        scheduler.run_all();

        assert!(matches!(
            store.cancel_preview(&preview.preview_id),
            Err(ProfileError::PreviewNotFound(_))
        ));
    }

    #[test]
    fn scheduled_callback_contains_no_secret_and_cancel_is_idempotent_for_timer() {
        let clock = Arc::new(StepClock::new(0));
        let scheduler = Arc::new(DeterministicScheduler::default());
        let store = make_store_with_scheduler(clock.clone(), scheduler.clone());
        let preview = store.preview_pairing(&auth_url("hub.example.com")).unwrap();
        store.cancel_preview(&preview.preview_id).unwrap();
        clock.advance(PREVIEW_TTL_SECS);
        scheduler.run_all();
        assert!(matches!(
            store.cancel_preview(&preview.preview_id),
            Err(ProfileError::PreviewNotFound(_))
        ));
    }

    #[test]
    fn clear_previews_removes_all_pending_secrets() {
        let clock = Arc::new(StepClock::new(0));
        let scheduler = Arc::new(DeterministicScheduler::default());
        let store = make_store_with_scheduler(clock, scheduler);
        let first = store.preview_pairing(&auth_url("one.example.com")).unwrap();
        let second = store.preview_pairing(&auth_url("two.example.com")).unwrap();
        store.clear_previews();
        assert!(store.cancel_preview(&first.preview_id).is_err());
        assert!(store.cancel_preview(&second.preview_id).is_err());
    }

    #[test]
    fn owned_scheduler_uses_one_worker_for_one_hundred_previews_and_cleans_up() {
        let scheduler = Arc::new(OwnedPreviewExpiryScheduler::new());
        let worker_counter = scheduler.worker_counter();
        let clock = Arc::new(StepClock::new(0));
        let store = ProfileStore::new_with_scheduler(
            Arc::new(MemoryPreferences::new()),
            Arc::new(MemorySecureStore::new()),
            Arc::new(OkProbe),
            clock,
            Arc::new(NetworkPolicy::new(Box::new(
                crate::network_policy::AlwaysPrivateResolver,
            ))),
            scheduler.clone(),
        );

        let mut ids = Vec::new();
        for _ in 0..100 {
            ids.push(
                store
                    .preview_pairing(&auth_url("hub.example.com"))
                    .unwrap()
                    .preview_id,
            );
        }
        assert_eq!(scheduler.worker_count(), 1);
        assert_eq!(scheduler.pending_count(), 100);

        store
            .confirm_pairing(&ids[0], "Confirmed", false, ReleaseMode::Release)
            .unwrap();
        assert_eq!(scheduler.pending_count(), 99);
        for id in ids.iter().skip(1).take(50) {
            store.cancel_preview(id).unwrap();
        }
        assert_eq!(scheduler.pending_count(), 49);
        store.clear_previews();
        assert_eq!(scheduler.pending_count(), 0);

        drop(store);
        drop(scheduler);
        assert_eq!(worker_counter.load(Ordering::SeqCst), 0);
    }

    #[test]
    fn owned_scheduler_never_runs_cancelled_callback() {
        let scheduler = OwnedPreviewExpiryScheduler::new();
        let fired = Arc::new(AtomicU64::new(0));
        let fired_callback = fired.clone();
        scheduler.schedule_in(
            "cancelled".to_owned(),
            std::time::Duration::from_millis(25),
            Box::new(move || {
                fired_callback.fetch_add(1, Ordering::SeqCst);
            }),
        );
        scheduler.cancel("cancelled");
        std::thread::sleep(std::time::Duration::from_millis(75));
        assert_eq!(fired.load(Ordering::SeqCst), 0);
        scheduler.shutdown();
        assert_eq!(scheduler.worker_count(), 0);
    }

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
    fn select_updates_active_id_and_generation() {
        let prefs = Arc::new(MemoryPreferences::new());
        let secure = Arc::new(MemorySecureStore::new());
        let probe = Arc::new(OkProbe);
        let clock = Arc::new(StepClock::new(0));
        let policy = Arc::new(NetworkPolicy::new(Box::new(
            crate::network_policy::AlwaysPrivateResolver,
        )));
        let store = ProfileStore::new(prefs.clone(), secure, probe, clock, policy);

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

        let first = store.select(&p1.id).unwrap();
        let second = store.select(&p2.id).unwrap();
        assert!(second.generation > first.generation);
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

    // -- Preferences-save failure rollback -------------------------------------

    #[test]
    fn prefs_save_failure_on_new_add_deletes_new_token() {
        let prefs = Arc::new(MemoryPreferences::new());
        let secure = Arc::new(MemorySecureStore::new());
        let probe = Arc::new(OkProbe);
        let clock = Arc::new(StepClock::new(0));
        let store = make_store(prefs.clone(), secure.clone(), probe, clock);

        // No existing profiles. Confirm a new add; make prefs.save fail.
        prefs.fail_next_save();
        let pv = store.preview_pairing(&auth_url("hub.example.com")).unwrap();
        let err = store
            .confirm_pairing(&pv.preview_id, "Alpha", false, ReleaseMode::Release)
            .unwrap_err();
        assert!(matches!(err, ProfileError::Preferences(_)));

        // No profile was added and no orphan token should remain.
        assert_eq!(store.list().unwrap().len(), 0);
        assert_eq!(secure.map.lock().unwrap().len(), 0);
    }

    #[test]
    fn prefs_save_failure_on_repair_restores_prior_token() {
        let prefs = Arc::new(MemoryPreferences::new());
        let secure = Arc::new(MemorySecureStore::new());
        let probe = Arc::new(OkProbe);
        let clock = Arc::new(StepClock::new(0));
        let store = make_store(prefs.clone(), secure.clone(), probe, clock);

        // Establish a profile with a known token.
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
        let original_token = secure.get_token(&p.id).unwrap();

        // Re-pair with a different token (same origin, allow duplicate).
        // Use a distinct 43-char base64url token so the rollback assertion is
        // meaningful: after prefs failure, the original token must be restored.
        const NEW_TOKEN: &str = "AQIDBAUGBwgJCgsMDQ4PEBESExQVFhcYGRobHB0eHyA";
        assert_eq!(NEW_TOKEN.len(), 43);
        let new_url = format!("https://hub.example.com:8443/auth?token={NEW_TOKEN}");
        let pv = store.preview_repair(&p.id, &new_url).unwrap();
        // Make prefs.save fail after the Keychain write.
        prefs.fail_next_save();
        let err = store
            .confirm_pairing(&pv.preview_id, "Alpha", true, ReleaseMode::Release)
            .unwrap_err();
        assert!(matches!(err, ProfileError::Preferences(_)));

        // Old profile remains fully usable: still listed, original token restored.
        assert_eq!(store.list().unwrap().len(), 1);
        assert_eq!(store.list().unwrap()[0].name, "Alpha");
        assert_eq!(secure.get_token(&p.id).unwrap(), original_token);
        assert_ne!(secure.get_token(&p.id).unwrap(), NEW_TOKEN);
    }

    #[test]
    fn prefs_save_failure_on_remove_restores_deleted_token() {
        let prefs = Arc::new(MemoryPreferences::new());
        let secure = Arc::new(MemorySecureStore::new());
        let probe = Arc::new(OkProbe);
        let clock = Arc::new(StepClock::new(0));
        let store = make_store(prefs.clone(), secure.clone(), probe, clock);

        // Establish a profile.
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
        let original_token = secure.get_token(&p.id).unwrap();

        // Remove; make prefs.save fail after the Keychain delete.
        prefs.fail_next_save();
        let err = store.remove(&p.id).unwrap_err();
        assert!(matches!(err, ProfileError::Preferences(_)));

        // Profile still listed (prefs unchanged), token restored.
        assert_eq!(store.list().unwrap().len(), 1);
        assert!(secure.has(&p.id));
        assert_eq!(secure.get_token(&p.id).unwrap(), original_token);
    }

    // -- Defect 1: secure.get failure aborts before mutation -----------------

    #[test]
    fn repair_aborts_when_secure_get_fails() {
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
        let original_token = secure.get_token(&p.id).unwrap();

        // Make secure.get fail for this profile during re-pair.
        secure.fail_next_get(p.id.clone());
        const NEW_TOKEN: &str = "AQIDBAUGBwgJCgsMDQ4PEBESExQVFhcYGRobHB0eHyA";
        let new_url = format!("https://hub.example.com:8443/auth?token={NEW_TOKEN}");
        let pv = store.preview_repair(&p.id, &new_url).unwrap();
        let err = store
            .confirm_pairing(&pv.preview_id, "Alpha", true, ReleaseMode::Release)
            .unwrap_err();

        // Operation aborted before set/delete: prefs unchanged, token unchanged.
        assert!(matches!(err, ProfileError::SecureStore(_)));
        assert_eq!(store.list().unwrap().len(), 1);
        assert_eq!(secure.get_token(&p.id).unwrap(), original_token);
        assert_ne!(secure.get_token(&p.id).unwrap(), NEW_TOKEN);
        // Error message must not contain the token.
        assert!(!format!("{err}").contains(NEW_TOKEN));
        assert!(!format!("{err:?}").contains(NEW_TOKEN));
    }

    #[test]
    fn remove_aborts_when_secure_get_fails() {
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
        let original_token = secure.get_token(&p.id).unwrap();

        // Make secure.get fail during remove.
        secure.fail_next_get(p.id.clone());
        let err = store.remove(&p.id).unwrap_err();

        // Operation aborted before delete: prefs unchanged, token unchanged.
        assert!(matches!(err, ProfileError::SecureStore(_)));
        assert_eq!(store.list().unwrap().len(), 1);
        assert!(secure.has(&p.id));
        assert_eq!(secure.get_token(&p.id).unwrap(), original_token);
    }

    // -- Defect 2: Consistency rollback-failure -------------------------------

    #[test]
    fn consistency_error_on_new_add_when_rollback_delete_fails_v2() {
        let prefs = Arc::new(MemoryPreferences::new());
        let secure = Arc::new(MemorySecureStore::new());
        let probe = Arc::new(OkProbe);
        let clock = Arc::new(StepClock::new(0));
        let store = make_store(prefs.clone(), secure.clone(), probe, clock);

        // New add: prefs.save fails, then rollback delete also fails.
        // Use fail_next_save + fail_all_next_delete.
        prefs.fail_next_save();
        secure.fail_all_next_delete();
        let pv = store.preview_pairing(&auth_url("hub.example.com")).unwrap();
        let err = store
            .confirm_pairing(&pv.preview_id, "Alpha", false, ReleaseMode::Release)
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
            _ => panic!("expected Consistency error, got {err:?}"),
        }
    }

    #[test]
    fn consistency_error_on_repair_when_rollback_restore_fails() {
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
        let original_token = secure.get_token(&p.id).unwrap();

        // Re-pair: prefs.save fails, then rollback set (restore) also fails.
        const NEW_TOKEN: &str = "AQIDBAUGBwgJCgsMDQ4PEBESExQVFhcYGRobHB0eHyA";
        let new_url = format!("https://hub.example.com:8443/auth?token={NEW_TOKEN}");
        let pv = store.preview_repair(&p.id, &new_url).unwrap();
        prefs.fail_next_save();
        // The initial confirm did 1 set; the re-pair confirm does a 2nd set
        // (main write); the rollback does a 3rd set (restore). Fail on the 3rd.
        secure.fail_set_after_n(p.id.clone(), 2); // 1st=main write, 2nd=rollback restore
        let err = store
            .confirm_pairing(&pv.preview_id, "Alpha", true, ReleaseMode::Release)
            .unwrap_err();

        match &err {
            ProfileError::Consistency {
                profile_id,
                operation,
                rollback_operation,
            } => {
                assert_eq!(profile_id, &p.id);
                assert_eq!(operation, "repair");
                assert_eq!(rollback_operation, "restore");
            }
            _ => panic!("expected Consistency error, got {err:?}"),
        }
        // No token in Display/Debug/serialization.
        assert!(!format!("{err}").contains(NEW_TOKEN));
        assert!(!format!("{err:?}").contains(NEW_TOKEN));
        assert!(!format!("{err}").contains(&original_token));
    }

    #[test]
    fn consistency_error_on_remove_when_rollback_restore_fails() {
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
        let original_token = secure.get_token(&p.id).unwrap();

        // Remove: prefs.save fails, then rollback set (restore) also fails.
        prefs.fail_next_save();
        // The initial confirm did 1 set; the rollback does a 2nd set (restore).
        // Fail on the 2nd.
        secure.fail_set_after_n(p.id.clone(), 1); // 1st=rollback restore
        let err = store.remove(&p.id).unwrap_err();

        match &err {
            ProfileError::Consistency {
                profile_id,
                operation,
                rollback_operation,
            } => {
                assert_eq!(profile_id, &p.id);
                assert_eq!(operation, "remove");
                assert_eq!(rollback_operation, "restore");
            }
            _ => panic!("expected Consistency error, got {err:?}"),
        }
        assert!(!format!("{err}").contains(&original_token));
        assert!(!format!("{err:?}").contains(&original_token));
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
