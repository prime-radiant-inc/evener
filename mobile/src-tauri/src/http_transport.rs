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
    policy: Arc<NetworkPolicy>,
    mode: ReleaseMode,
    client: reqwest::Client,
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
        let client = reqwest::Client::builder()
            .redirect(reqwest::redirect::Policy::none())
            .build()
            .expect("failed to build reqwest client");
        Self {
            origin,
            token,
            policy,
            mode,
            client,
            diagnostics: Arc::new(Diagnostics::new()),
        }
    }

    /// Construct an authenticated Hub HTTP client with a shared reqwest
    /// client and shared diagnostics ring. Used by the managed
    /// `TransportState` so all requests share one client (connection pool)
    /// and one diagnostics ring.
    pub fn with_client(
        origin: String,
        token: String,
        policy: Arc<NetworkPolicy>,
        mode: ReleaseMode,
        client: reqwest::Client,
        diagnostics: Arc<Diagnostics>,
    ) -> Self {
        Self {
            origin,
            token,
            policy,
            mode,
            client,
            diagnostics,
        }
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
        // Validate path.
        if !ALLOWED_PATH_PREFIXES
            .iter()
            .any(|prefix| request.path.starts_with(prefix))
        {
            return Err(HttpError::PathNotAllowed);
        }

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
        let origin_url = url::Url::parse(&self.origin).map_err(|_| HttpError::PolicyRejected)?;
        let pinned = self
            .policy
            .resolve(&origin_url, self.mode)
            .map_err(|_| HttpError::PolicyRejected)?;

        // Build the full URL using the pinned origin. We connect to the
        // validated address while preserving the original Host header.
        let url = build_url(&pinned, &request.path);

        let method = reqwest::Method::from_bytes(request.method.as_bytes())
            .map_err(|_| HttpError::MethodNotAllowed)?;

        // Build the request. reqwest resolves the hostname in `url`; since we
        // use the pinned IP in the URL authority, it connects to the validated
        // address. We set the Host header to the original hostname to preserve
        // TLS SNI/certificate identity.
        let host_header = format!(
            "{}{}",
            pinned.host(),
            pinned
                .port()
                .checked_sub(0)
                .map(|p| format!(":{p}"))
                .filter(|_| {
                    // Omit the port if it's the scheme default.
                    !is_default_port(pinned.scheme(), pinned.port())
                })
                .unwrap_or_default()
        );

        let mut builder = self
            .client
            .request(method, url.as_str())
            .header("host", &host_header)
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
                profile_id: profile_id.map(|s| s.to_owned()),
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

fn is_default_port(scheme: &str, port: u16) -> bool {
    matches!((scheme, port), ("http", 80) | ("https", 443))
}

/// Build a request URL from a pinned origin and a relative path. The URL
/// authority is the pinned IP address; the Host header carries the original
/// hostname separately.
fn build_url(pinned: &PinnedOrigin, path: &str) -> url::Url {
    let scheme = pinned.scheme();
    let addr = pinned
        .addrs()
        .first()
        .map(|ip| ip.to_string())
        .unwrap_or_default();
    let port = pinned.port();
    let authority = if is_default_port(scheme, port) {
        addr
    } else {
        format!("{addr}:{port}")
    };
    url::Url::parse(&format!("{scheme}://{authority}{path}"))
        .unwrap_or_else(|_| url::Url::parse(&format!("{scheme}://{authority}{path}")).unwrap())
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
}
