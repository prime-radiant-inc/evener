//! Authenticated, bounded, binary-safe Hub HTTP transport.
//!
//! Every connection is re-resolved through `NetworkPolicy`; the approved socket
//! addresses are pinned while the original host remains in the URL for Host,
//! TLS SNI, and certificate validation. Redirects are disabled. Request and
//! response bodies remain bytes; JSON is a caller codec.

use std::collections::{HashMap, HashSet, VecDeque};
use std::sync::Arc;

use futures_util::StreamExt as _;
use parking_lot::Mutex;
use serde::{Deserialize, Serialize};
use tokio::sync::watch;

use crate::diagnostics::{Diagnostics, StatusClass};
use crate::error::ReleaseMode;
use crate::network_policy::{NetworkPolicy, PinnedOrigin};

pub const MAX_REQUEST_BODY_BYTES: usize = 8 * 1024 * 1024;
pub const MAX_RESPONSE_BODY_BYTES: usize = 20 * 1024 * 1024;
const COMPLETED_ID_CAPACITY: usize = 1024;
const REQUEST_ID_MIN_LEN: usize = 16;
const REQUEST_ID_MAX_LEN: usize = 128;

/// Header used only by the raw IPC upload command. It carries an opaque ID,
/// never request metadata or a capability.
pub const REQUEST_ID_HEADER: &str = "x-evener-request-id";

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

/// Metadata registered before an optional raw-body upload. The body length is
/// the exact raw-byte count expected by the execute command.
#[derive(Debug, Clone, Deserialize, Serialize)]
#[serde(rename_all = "camelCase", deny_unknown_fields)]
pub struct PreparedHttpRequest {
    pub request_id: String,
    pub active_profile_id: String,
    pub method: String,
    pub path: String,
    pub body_length: usize,
    #[serde(default)]
    pub media_type: Option<String>,
}

/// An allowlisted relative request with a byte body. It intentionally has no
/// caller-provided headers.
#[derive(Debug, Clone)]
pub struct HubRequest {
    pub method: String,
    pub path: String,
    pub body: Option<Vec<u8>>,
    pub media_type: Option<String>,
}

#[derive(Debug, Clone, Default, Deserialize, Serialize, PartialEq, Eq)]
#[serde(rename_all = "camelCase", deny_unknown_fields)]
pub struct HubResponseHeaders {
    #[serde(skip_serializing_if = "Option::is_none")]
    pub content_type: Option<String>,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub content_length: Option<String>,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub etag: Option<String>,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub last_modified: Option<String>,
}

#[derive(Debug, Clone)]
pub struct HubResponse {
    pub status: u16,
    pub headers: HubResponseHeaders,
    pub media_type: Option<String>,
    pub body: Vec<u8>,
}

const ALLOWED_METHODS: &[&str] = &["GET", "POST", "PUT", "PATCH"];
const ALLOWED_PATH_PREFIXES: &[&str] = &["/api/", "/docs/", "/images/"];
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

#[derive(Debug, thiserror::Error, Clone, Copy, PartialEq, Eq)]
pub enum HttpError {
    #[error("method not allowed")]
    MethodNotAllowed,
    #[error("path not allowed")]
    PathNotAllowed,
    #[error("body exceeds maximum size")]
    BodyTooLarge,
    #[error("response exceeds maximum size")]
    ResponseTooLarge,
    #[error("media type not allowed")]
    MediaTypeNotAllowed,
    #[error("network policy rejected origin")]
    PolicyRejected,
    #[error("request failed")]
    RequestFailed,
    #[error("redirect rejected")]
    RedirectRejected,
    #[error("transport error")]
    TransportError,
    #[error("request cancelled")]
    Cancelled,
    #[error("invalid request id")]
    InvalidRequestId,
    #[error("request already exists")]
    DuplicateRequest,
    #[error("request body missing")]
    BodyMissing,
}

/// Race-safe lifecycle for prepared bodies and live cancellation. A cancel
/// arriving before prepare records only the opaque ID in the bounded completed
/// set, so prepare rejects it without retaining a pending entry, body, or
/// metadata.
#[derive(Default)]
pub struct HttpRequestRegistry {
    inner: Mutex<RegistryInner>,
}

#[derive(Default)]
struct RegistryInner {
    entries: HashMap<String, RegistryEntry>,
    completed_order: VecDeque<String>,
    completed: HashSet<String>,
}

enum RegistryEntry {
    Pending {
        metadata: PreparedHttpRequest,
        body: Option<Vec<u8>>,
    },
    Running {
        cancel: watch::Sender<bool>,
    },
}

pub struct RegisteredHttpRequest {
    pub metadata: PreparedHttpRequest,
    pub body: Option<Vec<u8>>,
    pub cancellation: watch::Receiver<bool>,
}

impl HttpRequestRegistry {
    pub fn new() -> Self {
        Self::default()
    }

    pub fn prepare(&self, metadata: PreparedHttpRequest) -> Result<(), HttpError> {
        validate_request_id(&metadata.request_id)?;
        validate_request_metadata(&metadata)?;
        let mut inner = self.inner.lock();
        if inner.completed.contains(&metadata.request_id) {
            return Err(HttpError::Cancelled);
        }
        if inner.entries.contains_key(&metadata.request_id) {
            return Err(HttpError::DuplicateRequest);
        }
        inner.entries.insert(
            metadata.request_id.clone(),
            RegistryEntry::Pending {
                metadata,
                body: None,
            },
        );
        Ok(())
    }

    pub fn upload_body(&self, request_id: &str, body: Vec<u8>) -> Result<(), HttpError> {
        validate_request_id(request_id)?;
        if body.len() > MAX_REQUEST_BODY_BYTES {
            return Err(HttpError::BodyTooLarge);
        }
        let mut inner = self.inner.lock();
        let completed = inner.completed.contains(request_id);
        match inner.entries.get_mut(request_id) {
            Some(RegistryEntry::Pending {
                metadata,
                body: slot,
            }) if metadata.body_length == body.len() && slot.is_none() => {
                *slot = Some(body);
                Ok(())
            }
            Some(RegistryEntry::Pending { .. }) => Err(HttpError::BodyMissing),
            None if completed => Err(HttpError::Cancelled),
            _ => Err(HttpError::DuplicateRequest),
        }
    }

    pub fn begin(&self, request_id: &str) -> Result<RegisteredHttpRequest, HttpError> {
        validate_request_id(request_id)?;
        let mut inner = self.inner.lock();
        let entry = inner.entries.remove(request_id);
        match entry {
            Some(RegistryEntry::Pending { metadata, body }) => {
                let body = if metadata.body_length == 0 {
                    body.or_else(|| Some(Vec::new()))
                } else {
                    body
                }
                .ok_or(HttpError::BodyMissing)?;
                if body.len() != metadata.body_length {
                    return Err(HttpError::BodyMissing);
                }
                let (cancel, cancellation) = watch::channel(false);
                inner
                    .entries
                    .insert(request_id.to_owned(), RegistryEntry::Running { cancel });
                Ok(RegisteredHttpRequest {
                    metadata,
                    body: if body.is_empty() { None } else { Some(body) },
                    cancellation,
                })
            }
            Some(existing) => {
                inner.entries.insert(request_id.to_owned(), existing);
                Err(HttpError::DuplicateRequest)
            }
            None if inner.completed.contains(request_id) => Err(HttpError::Cancelled),
            None => Err(HttpError::BodyMissing),
        }
    }

    pub fn cancel(&self, request_id: &str) -> Result<(), HttpError> {
        validate_request_id(request_id)?;
        let mut inner = self.inner.lock();
        if inner.completed.contains(request_id) {
            return Ok(());
        }
        match inner.entries.remove(request_id) {
            Some(RegistryEntry::Running { cancel }) => {
                let _ = cancel.send(true);
                inner.mark_completed(request_id.to_owned());
            }
            Some(RegistryEntry::Pending { .. }) => {
                inner.mark_completed(request_id.to_owned());
            }
            None => {
                inner.mark_completed(request_id.to_owned());
            }
        }
        Ok(())
    }

    pub fn finish(&self, request_id: &str) {
        let mut inner = self.inner.lock();
        inner.entries.remove(request_id);
        inner.mark_completed(request_id.to_owned());
    }

    pub fn active_count(&self) -> usize {
        self.inner.lock().entries.len()
    }
}

impl RegistryInner {
    fn mark_completed(&mut self, request_id: String) {
        if self.completed.insert(request_id.clone()) {
            self.completed_order.push_back(request_id);
        }
        while self.completed_order.len() > COMPLETED_ID_CAPACITY {
            if let Some(old) = self.completed_order.pop_front() {
                self.completed.remove(&old);
            }
        }
    }
}

pub struct HubHttp {
    origin: String,
    token: String,
    boundary: PinnedHttpBoundary,
    diagnostics: Arc<Diagnostics>,
}

impl HubHttp {
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

    pub fn with_root_certificate(mut self, certificate: reqwest::Certificate) -> Self {
        self.boundary = self.boundary.with_root_certificate(certificate);
        self
    }

    pub fn diagnostics(&self) -> &Diagnostics {
        &self.diagnostics
    }

    pub fn origin(&self) -> &str {
        &self.origin
    }

    pub async fn request(&self, request: HubRequest) -> Result<HubResponse, HttpError> {
        self.request_with_profile(request, None, 0).await
    }

    pub async fn request_with_profile(
        &self,
        request: HubRequest,
        profile_id: Option<&str>,
        generation: u64,
    ) -> Result<HubResponse, HttpError> {
        self.perform_request(request, profile_id, generation).await
    }

    pub async fn request_cancellable(
        &self,
        request: HubRequest,
        profile_id: Option<&str>,
        generation: u64,
        mut cancellation: watch::Receiver<bool>,
    ) -> Result<HubResponse, HttpError> {
        if *cancellation.borrow() {
            return Err(HttpError::Cancelled);
        }
        tokio::select! {
            biased;
            _ = cancellation.changed() => Err(HttpError::Cancelled),
            result = self.perform_request(request, profile_id, generation) => result,
        }
    }

    async fn perform_request(
        &self,
        request: HubRequest,
        profile_id: Option<&str>,
        generation: u64,
    ) -> Result<HubResponse, HttpError> {
        validate_hub_request(&request)?;
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
        let mut builder = client
            .request(method, url.as_str())
            .bearer_auth(&self.token);
        if let Some(body) = request.body {
            let content_type = request
                .media_type
                .as_deref()
                .unwrap_or("application/octet-stream");
            builder = builder.header("content-type", content_type).body(body);
        }

        let response = builder.send().await.map_err(|_| HttpError::RequestFailed)?;
        let status = response.status().as_u16();
        if (300..400).contains(&status) {
            self.record_diag(
                profile_id,
                StatusClass::TransportError,
                0,
                generation,
                Some("redirect_rejected"),
            );
            return Err(HttpError::RedirectRejected);
        }

        let headers = selected_headers(response.headers());
        let media_type = headers
            .content_type
            .as_deref()
            .and_then(|value| value.split(';').next())
            .map(str::trim)
            .filter(|value| !value.is_empty())
            .map(str::to_owned);
        if response
            .content_length()
            .is_some_and(|len| len > MAX_RESPONSE_BODY_BYTES as u64)
        {
            return Err(HttpError::ResponseTooLarge);
        }
        let mut body = Vec::with_capacity(
            response
                .content_length()
                .unwrap_or(0)
                .min(MAX_RESPONSE_BODY_BYTES as u64) as usize,
        );
        let mut stream = response.bytes_stream();
        while let Some(chunk) = stream.next().await {
            let chunk = chunk.map_err(|_| HttpError::TransportError)?;
            if body.len().saturating_add(chunk.len()) > MAX_RESPONSE_BODY_BYTES {
                return Err(HttpError::ResponseTooLarge);
            }
            body.extend_from_slice(&chunk);
        }

        self.record_diag(
            profile_id,
            status_class(status),
            body.len() as u64,
            generation,
            None,
        );
        Ok(HubResponse {
            status,
            headers,
            media_type,
            body,
        })
    }

    fn record_diag(
        &self,
        profile_id: Option<&str>,
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
                operation: "http_request".to_owned(),
                status,
                byte_count,
                generation,
                error_id: error_id.map(str::to_owned),
            });
    }
}

fn validate_request_id(request_id: &str) -> Result<(), HttpError> {
    if !(REQUEST_ID_MIN_LEN..=REQUEST_ID_MAX_LEN).contains(&request_id.len())
        || !request_id
            .bytes()
            .all(|byte| byte.is_ascii_alphanumeric() || byte == b'-' || byte == b'_')
    {
        return Err(HttpError::InvalidRequestId);
    }
    Ok(())
}

fn validate_request_metadata(request: &PreparedHttpRequest) -> Result<(), HttpError> {
    if request.body_length > MAX_REQUEST_BODY_BYTES {
        return Err(HttpError::BodyTooLarge);
    }
    validate_method_path_media(
        &request.method,
        &request.path,
        request.media_type.as_deref(),
    )
}

fn validate_hub_request(request: &HubRequest) -> Result<(), HttpError> {
    if request
        .body
        .as_ref()
        .is_some_and(|body| body.len() > MAX_REQUEST_BODY_BYTES)
    {
        return Err(HttpError::BodyTooLarge);
    }
    validate_method_path_media(
        &request.method,
        &request.path,
        request.media_type.as_deref(),
    )
}

fn validate_method_path_media(
    method: &str,
    path: &str,
    media_type: Option<&str>,
) -> Result<(), HttpError> {
    if !ALLOWED_METHODS.contains(&method) {
        return Err(HttpError::MethodNotAllowed);
    }
    validate_path(path)?;
    if let Some(media_type) = media_type {
        if media_type.contains(['\r', '\n']) {
            return Err(HttpError::MediaTypeNotAllowed);
        }
        let base = media_type.split(';').next().map(str::trim).unwrap_or("");
        if !ALLOWED_MEDIA_TYPES.contains(&base) {
            return Err(HttpError::MediaTypeNotAllowed);
        }
    }
    Ok(())
}

fn selected_headers(headers: &reqwest::header::HeaderMap) -> HubResponseHeaders {
    fn value(headers: &reqwest::header::HeaderMap, name: &'static str) -> Option<String> {
        headers
            .get(name)
            .and_then(|value| value.to_str().ok())
            .filter(|value| value.len() <= 4096)
            .map(str::to_owned)
    }
    HubResponseHeaders {
        content_type: value(headers, "content-type"),
        content_length: value(headers, "content-length"),
        etag: value(headers, "etag"),
        last_modified: value(headers, "last-modified"),
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

/// Validate only the path component. Query bytes cannot become route segments.
/// Encoded separators/dots are decoded once; a remaining `%` is rejected to
/// prevent mixed/double-encoding ambiguity.
pub fn validate_path(input: &str) -> Result<(), HttpError> {
    let path = input.split_once('?').map_or(input, |(path, _)| path);
    if path.is_empty() || !path.starts_with('/') || path.starts_with("//") || path.contains('\\') {
        return Err(HttpError::PathNotAllowed);
    }
    let decoded = percent_decode_path(path)?;
    if decoded.contains('%')
        || decoded.starts_with("//")
        || decoded.contains("//")
        || decoded.contains('\\')
    {
        return Err(HttpError::PathNotAllowed);
    }
    if decoded
        .split('/')
        .any(|segment| segment == "." || segment == "..")
    {
        return Err(HttpError::PathNotAllowed);
    }
    if !ALLOWED_PATH_PREFIXES
        .iter()
        .any(|prefix| decoded.starts_with(prefix))
    {
        return Err(HttpError::PathNotAllowed);
    }
    Ok(())
}

fn percent_decode_path(input: &str) -> Result<String, HttpError> {
    let bytes = input.as_bytes();
    let mut out = Vec::with_capacity(bytes.len());
    let mut index = 0;
    while index < bytes.len() {
        if bytes[index] == b'%' {
            if index + 2 >= bytes.len() {
                return Err(HttpError::PathNotAllowed);
            }
            let high = hex_val(bytes[index + 1]).ok_or(HttpError::PathNotAllowed)?;
            let low = hex_val(bytes[index + 2]).ok_or(HttpError::PathNotAllowed)?;
            out.push((high << 4) | low);
            index += 3;
        } else {
            out.push(bytes[index]);
            index += 1;
        }
    }
    String::from_utf8(out).map_err(|_| HttpError::PathNotAllowed)
}

fn hex_val(byte: u8) -> Option<u8> {
    match byte {
        b'0'..=b'9' => Some(byte - b'0'),
        b'a'..=b'f' => Some(byte - b'a' + 10),
        b'A'..=b'F' => Some(byte - b'A' + 10),
        _ => None,
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn exact_binary_limits_are_contractual() {
        assert_eq!(MAX_REQUEST_BODY_BYTES, 8 * 1024 * 1024);
        assert_eq!(MAX_RESPONSE_BODY_BYTES, 20 * 1024 * 1024);
    }

    #[test]
    fn canonical_path_policy_splits_query_before_validation() {
        assert!(validate_path("/api/health?next=/../secret\\value").is_ok());
        for path in [
            "/api/%2e%2e/x",
            "/api/%2E%2e/x",
            "/api/%252e%252e/x",
            "/api/%2f/x",
            "/api//x",
            "/api/..\\x",
            "//api/x",
            "/API/x",
            "/api/%GG",
        ] {
            assert!(validate_path(path).is_err(), "accepted {path}");
        }
    }

    #[test]
    fn cancellation_registry_handles_cancel_before_registration_and_completion() {
        let registry = HttpRequestRegistry::new();
        let id = "request_123456789";
        registry.cancel(id).unwrap();
        assert_eq!(registry.active_count(), 0);
        let result = registry.prepare(PreparedHttpRequest {
            request_id: id.to_owned(),
            active_profile_id: "profile".to_owned(),
            method: "GET".to_owned(),
            path: "/api/x".to_owned(),
            body_length: 0,
            media_type: None,
        });
        assert_eq!(result, Err(HttpError::Cancelled));
        assert_eq!(registry.active_count(), 0);
        registry.cancel(id).unwrap();
        assert_eq!(registry.active_count(), 0);
    }

    #[test]
    fn media_type_allows_multipart_boundary_without_header_injection() {
        assert!(validate_method_path_media(
            "POST",
            "/api/upload",
            Some("multipart/form-data; boundary=abc123")
        )
        .is_ok());
        assert!(validate_method_path_media(
            "POST",
            "/api/upload",
            Some("multipart/form-data\r\nx-secret: value")
        )
        .is_err());
    }
}
