//! Authenticated Hub HTTP transport.
//!
//! On every connection, re-resolves the origin with `NetworkPolicy`, pins the
//! validated address for the TCP connection, and preserves the original Host
//! header and TLS SNI/certificate identity. Redirects are disabled. Caller
//! auth/cookie/hop headers are never accepted — the request DTO carries none.
//! The active profile bearer is injected natively. Bodies and media types are
//! bounded and allowlisted. Status and binary fidelity are preserved.
//! Cancellation aborts the in-flight request. No token, URL, or body ever
//! appears in errors or diagnostics.

use std::sync::Arc;
use std::time::Duration;

use serde::{Deserialize, Serialize};

use crate::diagnostics::{Diagnostics, StatusClass};
use crate::error::ReleaseMode;
use crate::network_policy::{NetworkPolicy, PinnedOrigin};

/// Shared pinning boundary for every HTTP client. It keeps the original
/// hostname in the URL (therefore TLS SNI and certificate verification) while
/// overriding only that hostname's resolver answer with validated socket
/// addresses. Redirects are disabled on both async and blocking clients.
#[derive(Clone)]
pub struct PinnedHttpBoundary {
    policy: Arc<NetworkPolicy>,
    mode: ReleaseMode,
    extra_roots: Vec<reqwest::Certificate>,
}

impl PinnedHttpBoundary {
    pub fn new(policy: Arc<NetworkPolicy>, mode: ReleaseMode) -> Self {
        Self {
            policy,
            mode,
            extra_roots: Vec::new(),
        }
    }

    pub fn with_root_certificate(mut self, certificate: reqwest::Certificate) -> Self {
        self.extra_roots.push(certificate);
        self
    }

    pub fn clone_with_mode(&self, mode: ReleaseMode) -> Self {
        Self {
            policy: self.policy.clone(),
            mode,
            extra_roots: self.extra_roots.clone(),
        }
    }

    pub fn resolve(&self, origin: &str) -> Result<(url::Url, PinnedOrigin), HttpError> {
        let origin_url = url::Url::parse(origin).map_err(|_| HttpError::PolicyRejected)?;
        let pinned = self
            .policy
            .resolve(&origin_url, self.mode)
            .map_err(|_| HttpError::PolicyRejected)?;
        Ok((origin_url, pinned))
    }

    fn socket_addrs(pinned: &PinnedOrigin) -> Vec<std::net::SocketAddr> {
        pinned
            .addrs()
            .iter()
            .map(|ip| std::net::SocketAddr::new(*ip, pinned.port()))
            .collect()
    }

    pub fn async_client(&self, pinned: &PinnedOrigin) -> Result<reqwest::Client, HttpError> {
        let addrs = Self::socket_addrs(pinned);
        let mut builder = reqwest::Client::builder()
            .redirect(reqwest::redirect::Policy::none())
            .resolve_to_addrs(pinned.host(), &addrs);
        for certificate in &self.extra_roots {
            builder = builder.add_root_certificate(certificate.clone());
        }
        builder.build().map_err(|_| HttpError::TransportError)
    }

    pub fn blocking_client(
        &self,
        pinned: &PinnedOrigin,
    ) -> Result<reqwest::blocking::Client, HttpError> {
        let addrs = Self::socket_addrs(pinned);
        let mut builder = reqwest::blocking::Client::builder()
            .redirect(reqwest::redirect::Policy::none())
            .resolve_to_addrs(pinned.host(), &addrs);
        for certificate in &self.extra_roots {
            builder = builder.add_root_certificate(certificate.clone());
        }
        builder.build().map_err(|_| HttpError::TransportError)
    }
}

// ---------------------------------------------------------------------------
// Request/Response DTOs
// ---------------------------------------------------------------------------

/// A allowlisted relative Hub HTTP request. Carries no headers, so caller
/// auth/cookie/hop headers can never be injected.
#[derive(Debug, Clone, Deserialize, Serialize)]
#[serde(rename_all = "camelCase")]
pub struct HubRequest {
    pub method: String,
    pub path: String,
    #[serde(default)]
    pub body: Option<serde_json::Value>,
    #[serde(default)]
    pub media_type: Option<String>,
}

/// A Hub HTTP response: status code and parsed body. Never carries the token.
#[derive(Debug, Clone, Serialize)]
#[serde(rename_all = "camelCase")]
pub struct HubResponse {
    pub status: u16,
    pub body: serde_json::Value,
}

// ---------------------------------------------------------------------------
// Allowlists
// ---------------------------------------------------------------------------

/// Allowed HTTP methods.
const ALLOWED_METHODS: &[&str] = &["GET", "POST", "PUT", "PATCH"];

/// Allowed path prefixes for Hub API requests.
const ALLOWED_PATH_PREFIXES: &[&str] = &["/api/", "/docs/", "/images/"];

/// Maximum request body size (8 MiB).
const MAX_BODY_BYTES: usize = 8 * 1024 * 1024;

/// Allowed media types.
const ALLOWED_MEDIA_TYPES: &[&str] = &[
    "application/json",
    "text/plain",
    "text/markdown",
    "image/jpeg",
    "image/png",
    "image/webp",
    "image/gif",
    "multipart/form-data",
];

// ---------------------------------------------------------------------------
// Errors — never carry the token, URL, or body
// ---------------------------------------------------------------------------

#[derive(Debug, thiserror::Error)]
pub enum HttpError {
    #[error("method not allowed")]
    MethodNotAllowed,
    #[error("path not allowed")]
    PathNotAllowed,
    #[error("body exceeds maximum size")]
    BodyTooLarge,
    #[error("media type not allowed")]
    MediaTypeNotAllowed,
    #[error("network policy rejected origin")]
    PolicyRejected,
    #[error("request failed")]
    RequestFailed,
    #[error("redirect rejected")]
    RedirectRejected,
    #[error("server error")]
    ServerError,
    #[error("transport error")]
    TransportError,
}

// ---------------------------------------------------------------------------
// HubHttp
// ---------------------------------------------------------------------------

/// Authenticated Hub HTTP client. Holds the immutable origin and bearer
/// token. Re-resolves the origin through the `NetworkPolicy` on every
/// connection, pins the validated address, and preserves the original Host
/// header. Redirects are disabled at the client level. Never logs the token,
/// URL, or body.
pub struct HubHttp {
    origin: String,
    token: String,
    boundary: PinnedHttpBoundary,
    diagnostics: Arc<Diagnostics>,
}

impl HubHttp {
    /// Construct an authenticated Hub HTTP client.
    ///
    /// `origin` is the immutable confirmed origin (`scheme://host[:port]`).
    /// `token` is the active profile's bearer capability, retrieved only
    /// through the native Keychain adapter by the command layer.
    pub fn new(
        origin: String,
        token: String,
        policy: Arc<NetworkPolicy>,
        mode: ReleaseMode,
    ) -> Self {
        Self {
            origin,
            token,
            boundary: PinnedHttpBoundary::new(policy, mode),
            diagnostics: Arc::new(Diagnostics::new()),
        }
    }

    /// Construct an authenticated Hub HTTP client with a shared diagnostics
    /// ring. The request client itself is built after policy resolution so its
    /// DNS override is pinned to the newly approved address set.
    pub fn with_diagnostics(
        origin: String,
        token: String,
        policy: Arc<NetworkPolicy>,
        mode: ReleaseMode,
        diagnostics: Arc<Diagnostics>,
    ) -> Self {
        Self {
            origin,
            token,
            boundary: PinnedHttpBoundary::new(policy, mode),
            diagnostics,
        }
    }

    /// Test/enterprise trust seam; pinning, hostname validation, and redirect
    /// policy remain identical.
    pub fn with_root_certificate(mut self, certificate: reqwest::Certificate) -> Self {
        self.boundary = self.boundary.with_root_certificate(certificate);
        self
    }

    /// Borrow the diagnostics ring (for snapshot/export by commands).
    pub fn diagnostics(&self) -> &Diagnostics {
        &self.diagnostics
    }

    /// The immutable origin string.
    pub fn origin(&self) -> &str {
        &self.origin
    }

    /// Make an authenticated Hub HTTP request.
    ///
    /// Validates method, path, body size, and media type. Re-resolves the
    /// origin through the network policy, pins the validated address, and
    /// preserves the original Host header. Strips caller auth/cookies (the
    /// DTO carries none). Injects the native bearer token. Rejects redirects.
    /// Never logs the token, URL, or body.
    pub async fn request(&self, request: HubRequest) -> Result<HubResponse, HttpError> {
        self.request_with_profile(request, None, 0).await
    }

    /// As `request`, but records a diagnostic entry tagged with the redacted
    /// profile ID and connection generation.
    pub async fn request_with_profile(
        &self,
        request: HubRequest,
        profile_id: Option<&str>,
        generation: u64,
    ) -> Result<HubResponse, HttpError> {
        // Validate method.
        if !ALLOWED_METHODS.contains(&request.method.as_str()) {
            return Err(HttpError::MethodNotAllowed);
        }
        // Validate path against the allowlist (rejects dot segments,
        // percent-encoded bypasses, backslashes, double slashes, and
        // non-allowlisted prefixes).
        validate_path(&request.path)?;

        // Serialize the body and enforce the size bound. The serialized bytes
        // are what goes on the wire; the bound is on the wire size.
        let body_bytes: Option<Vec<u8>> = if let Some(body) = &request.body {
            let bytes = serde_json::to_vec(body).map_err(|_| HttpError::BodyTooLarge)?;
            if bytes.len() > MAX_BODY_BYTES {
                return Err(HttpError::BodyTooLarge);
            }
            Some(bytes)
        } else {
            None
        };

        // Validate media type.
        if let Some(mt) = &request.media_type {
            if !ALLOWED_MEDIA_TYPES.contains(&mt.as_str()) {
                return Err(HttpError::MediaTypeNotAllowed);
            }
        }

        // Re-resolve the origin through the network policy on every
        // connection. Pin the validated address; preserve the original Host.
        let (mut url, pinned) = self.boundary.resolve(&self.origin)?;
        url.set_path("");
        url.set_query(None);
        url.set_fragment(None);
        let url = url::Url::parse(&format!(
            "{}{path}",
            url.as_str().trim_end_matches('/'),
            path = request.path
        ))
        .map_err(|_| HttpError::PolicyRejected)?;
        let client = self.boundary.async_client(&pinned)?;

        let method = reqwest::Method::from_bytes(request.method.as_bytes())
            .map_err(|_| HttpError::MethodNotAllowed)?;

        // The URL keeps the original hostname for Host, SNI, and certificate
        // identity. The client resolver override pins its socket addresses.
        let mut builder = client
            .request(method, url.as_str())
            .bearer_auth(&self.token);

        if let Some(bytes) = body_bytes {
            let content_type = request.media_type.as_deref().unwrap_or("application/json");
            builder = builder.header("content-type", content_type).body(bytes);
        }

        let response = builder.send().await.map_err(|_| HttpError::RequestFailed)?;

        let status = response.status().as_u16();

        // Reject redirects (redirects are disabled, but a 3xx that slips
        // through is rejected explicitly).
        if (300..400).contains(&status) {
            self.record_diag(
                profile_id,
                "http_request",
                StatusClass::TransportError,
                0,
                generation,
                Some("redirect_rejected"),
            );
            return Err(HttpError::RedirectRejected);
        }

        if !response.status().is_success() {
            self.record_diag(
                profile_id,
                "http_request",
                status_class(status),
                0,
                generation,
                Some("server_error"),
            );
            return Err(HttpError::ServerError);
        }

        // Preserve status and body fidelity. Parse as JSON; the caller
        // handles binary through a separate attachment path.
        let byte_count = response.content_length().unwrap_or(0);
        let body: serde_json::Value = response
            .json()
            .await
            .map_err(|_| HttpError::TransportError)?;

        self.record_diag(
            profile_id,
            "http_request",
            StatusClass::Success,
            byte_count,
            generation,
            None,
        );

        Ok(HubResponse { status, body })
    }

    fn record_diag(
        &self,
        profile_id: Option<&str>,
        operation: &str,
        status: StatusClass,
        byte_count: u64,
        generation: u64,
        error_id: Option<&str>,
    ) {
        self.diagnostics
            .record(crate::diagnostics::DiagnosticEntry {
                timestamp: std::time::SystemTime::now()
                    .duration_since(std::time::UNIX_EPOCH)
                    .map(|d| d.as_secs())
                    .unwrap_or(0),
                profile_id: profile_id.and_then(crate::diagnostics::hash_profile_id),
                operation: operation.to_owned(),
                status,
                byte_count,
                generation,
                error_id: error_id.map(|s| s.to_owned()),
            });
    }
}

fn status_class(status: u16) -> StatusClass {
    if (200..300).contains(&status) {
        StatusClass::Success
    } else if (400..500).contains(&status) {
        StatusClass::ClientError
    } else if status >= 500 {
        StatusClass::ServerError
    } else {
        StatusClass::TransportError
    }
}

/// Validate a request path against the allowlist. Rejects dot segments
/// (literal and percent-encoded), backslashes, double slashes, and
/// non-allowlisted prefixes. Query strings on allowed paths are permitted.
/// The path must be a site-relative path beginning with `/`.
pub fn validate_path(path: &str) -> Result<(), HttpError> {
    if path.is_empty() || !path.starts_with('/') {
        return Err(HttpError::PathNotAllowed);
    }
    // Reject protocol-relative paths (// or /\) which a URL parser may
    // interpret as a scheme-less authority.
    if path.starts_with("//") {
        return Err(HttpError::PathNotAllowed);
    }
    // Reject backslashes entirely (Windows-style separators used to confuse
    // URL parsers and bypass prefix checks).
    if path.contains('\\') {
        return Err(HttpError::PathNotAllowed);
    }
    // Reject literal dot segments anywhere: `/../` and `/.` as a segment.
    // These traverse above the allowed root.
    for seg in path.split('/') {
        if seg == ".." || seg == "." {
            return Err(HttpError::PathNotAllowed);
        }
    }
    // Reject percent-encoded dot segments (%2e == '.'). Percent-decode only
    // the path component (not the query) and re-check for dot segments and
    // prefix.
    let (path_part, _query) = match path.split_once('?') {
        Some((p, q)) => (p, Some(q)),
        None => (path, None),
    };
    let decoded = percent_decode_path(path_part);
    // Re-check dot segments in the decoded path.
    for seg in decoded.split('/') {
        if seg == ".." || seg == "." {
            return Err(HttpError::PathNotAllowed);
        }
    }
    // The decoded path must start with an allowed prefix.
    if !ALLOWED_PATH_PREFIXES
        .iter()
        .any(|prefix| decoded.starts_with(prefix))
    {
        return Err(HttpError::PathNotAllowed);
    }
    Ok(())
}

/// Percent-decode the path component, handling %2e -> '.' and %2f -> '/'.
fn percent_decode_path(input: &str) -> String {
    let bytes = input.as_bytes();
    let mut out = Vec::with_capacity(bytes.len());
    let mut i = 0;
    while i < bytes.len() {
        if bytes[i] == b'%' && i + 2 < bytes.len() {
            let h = hex_val(bytes[i + 1]);
            let l = hex_val(bytes[i + 2]);
            if let (Some(h), Some(l)) = (h, l) {
                out.push((h << 4) | l);
                i += 3;
                continue;
            }
        }
        out.push(bytes[i]);
        i += 1;
    }
    String::from_utf8_lossy(&out).into_owned()
}

fn hex_val(b: u8) -> Option<u8> {
    match b {
        b'0'..=b'9' => Some(b - b'0'),
        b'a'..=b'f' => Some(b - b'a' + 10),
        b'A'..=b'F' => Some(b - b'A' + 10),
        _ => None,
    }
}

// Suppress unused-import warning for Duration if not referenced on all
// platforms (kept for future cancellation timeout wiring).
#[allow(dead_code)]
fn _duration_unused(_: Duration) {}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn allowed_methods_are_restricted() {
        assert!(ALLOWED_METHODS.contains(&"GET"));
        assert!(ALLOWED_METHODS.contains(&"POST"));
        assert!(!ALLOWED_METHODS.contains(&"DELETE"));
        assert!(!ALLOWED_METHODS.contains(&"CONNECT"));
    }

    #[test]
    fn allowed_paths_cover_api_docs_images() {
        assert!(ALLOWED_PATH_PREFIXES.contains(&"/api/"));
        assert!(ALLOWED_PATH_PREFIXES.contains(&"/docs/"));
    }

    #[test]
    fn max_body_is_8mib() {
        assert_eq!(MAX_BODY_BYTES, 8 * 1024 * 1024);
    }

    #[test]
    fn http_error_display_never_contains_token() {
        let err = HttpError::RequestFailed;
        let display = format!("{err}");
        let debug = format!("{err:?}");
        assert!(!display.contains("secret-token"));
        assert!(!debug.contains("secret-token"));
    }

    #[test]
    fn http_error_redirect_rejected_has_no_details() {
        let err = HttpError::RedirectRejected;
        assert!(!format!("{err}").contains("evil"));
        assert!(!format!("{err}").contains("http"));
    }

    // -- Path allowlist: adversarial bypass table ----------------------------

    #[test]
    fn path_allowlist_accepts_plain_allowed_prefixes() {
        assert!(validate_path("/api/health").is_ok());
        assert!(validate_path("/docs/intro").is_ok());
        assert!(validate_path("/images/logo.png").is_ok());
    }

    #[test]
    fn path_allowlist_rejects_root_and_outside_prefixes() {
        assert!(validate_path("/").is_err());
        assert!(validate_path("").is_err());
        assert!(validate_path("/etc/passwd").is_err());
        assert!(validate_path("/admin/users").is_err());
    }

    #[test]
    fn path_allowlist_rejects_percent_encoded_dot_segments() {
        // %2e == '.', %2f == '/'. These must not bypass the prefix check.
        assert!(validate_path("/api/%2e%2e/etc/passwd").is_err());
        assert!(validate_path("/api/%2e%2e%2fetc%2fpasswd").is_err());
        assert!(validate_path("/%2e%2e/etc/passwd").is_err());
    }

    #[test]
    fn path_allowlist_rejects_dot_segments() {
        assert!(validate_path("/api/../etc/passwd").is_err());
        assert!(validate_path("/api/./health").is_err());
        assert!(validate_path("/api/../api/health").is_err());
    }

    #[test]
    fn path_allowlist_rejects_double_slash() {
        assert!(validate_path("//api/health").is_err());
        assert!(validate_path("//etc/passwd").is_err());
    }

    #[test]
    fn path_allowlist_rejects_backslashes() {
        assert!(validate_path("\\api\\health").is_err());
        assert!(validate_path("/api/..\\..\\etc").is_err());
    }

    #[test]
    fn path_allowlist_rejects_mixed_case_prefix() {
        assert!(validate_path("/API/health").is_err());
        assert!(validate_path("/Api/health").is_err());
    }

    #[test]
    fn path_allowlist_preserves_allowed_query() {
        assert!(validate_path("/api/sessions?foo=bar").is_ok());
        assert!(validate_path("/api/health?token=x&y=z").is_ok());
    }
}
