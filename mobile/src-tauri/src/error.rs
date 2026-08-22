//! Errors for pairing URL parsing, network policy, and profile lifecycle.
//!
//! Display output never includes the capability token, auth URL query, or
//! raw scan text. Only the normalized origin is exposed in errors.

use std::fmt;

#[derive(Debug, thiserror::Error)]
pub enum PairingError {
    #[error("invalid authorization URL: {0}")]
    InvalidUrl(String),
    #[error("authorization URL must use http or https, got {0}")]
    BadScheme(String),
    #[error("authorization URL must not contain userinfo")]
    Userinfo,
    #[error("authorization URL must not contain a fragment")]
    Fragment,
    #[error("authorization URL path must be exactly /auth, got {0}")]
    BadPath(String),
    #[error("authorization URL must contain exactly one token query parameter")]
    MissingToken,
    #[error("authorization URL token must be 43-character base64url")]
    BadToken,
    #[error("authorization URL contains unknown query key: {0}")]
    UnknownQueryKey(String),
    #[error("authorization URL next must be a site-relative path")]
    BadNext,
    #[error("authorization URL must have an explicit host")]
    NoHost,
}

#[derive(Debug, thiserror::Error)]
pub enum NetworkError {
    #[error("no addresses resolved for {origin}")]
    NoAddresses { origin: String },
    #[error("release builds reject loopback origin {origin}; a physical phone cannot reach it")]
    ReleaseLoopback { origin: String },
    #[error("origin {origin} resolved to a mixed public/private address set")]
    MixedPublicPrivate { origin: String },
    #[error("resolver error for {origin}: {message}")]
    Resolve { origin: String, message: String },
    #[error("HTTP is forbidden for public hosts")]
    HttpPublicHost,
}

#[derive(Debug, thiserror::Error)]
pub enum ProfileError {
    #[error("profile name must not be blank")]
    BlankName,
    #[error("profile name already exists (case-insensitive): {0}")]
    DuplicateName(String),
    #[error("profile origin already exists: {0}")]
    DuplicateOrigin(String),
    #[error("profile not found: {0}")]
    NotFound(String),
    #[error("preview not found or expired: {0}")]
    PreviewNotFound(String),
    #[error("probe failed for {origin}: {message}")]
    ProbeFailed { origin: String, message: String },
    #[error("invalid pairing URL: {0}")]
    InvalidPairing(String),
    #[error("mobile API version mismatch: expected 1, got {0}")]
    MobileApiMismatch(i64),
    #[error("secure store failure: {0}")]
    SecureStore(String),
    #[error("preferences failure: {0}")]
    Preferences(String),
    #[error("preferences consistency failure: {reason}")]
    PreferencesConsistency { reason: &'static str },
    #[error("consistency error for profile {profile_id}: rollback failed after {operation} ({rollback_operation})")]
    Consistency {
        profile_id: String,
        operation: String,
        rollback_operation: String,
    },
}

/// Release mode controls which private-network policy applies.
#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub enum ReleaseMode {
    /// Production release: HTTP only to private ranges, loopback rejected.
    Release,
    /// Debug/simulator: loopback may be opted into explicitly.
    Debug { allow_loopback: bool },
}

impl fmt::Display for ReleaseMode {
    fn fmt(&self, f: &mut fmt::Formatter<'_>) -> fmt::Result {
        match self {
            ReleaseMode::Release => write!(f, "release"),
            ReleaseMode::Debug { allow_loopback } => {
                write!(f, "debug(allow_loopback={allow_loopback})")
            }
        }
    }
}
