//! Private-network policy for Hub HTTP/HTTPS connections.
//!
//! Release HTTP permits only addresses in IPv4 loopback/RFC1918/link-local/
//! CGNAT (`100.64.0.0/10`) or IPv6 loopback/link-local/unique-local ranges;
//! release builds reject loopback because a physical phone cannot reach it.
//! HTTPS may be public or private. Hostnames must resolve entirely into the
//! allowed set. The original hostname is retained for Host/TLS identity while
//! a validated address set is used for connection. Resolution happens anew on
//! every new connection. Display and errors expose only the normalized
//! origin, never query or token.

use std::fmt;
use std::net::{IpAddr, Ipv4Addr, Ipv6Addr};

use url::Url;

use crate::error::{NetworkError, ReleaseMode};

/// A resolved, validated origin pinned to a set of IP addresses.
///
/// The original hostname is retained for Host/TLS identity; the address set
/// is used for connection. Display exposes only the normalized origin.
#[derive(Clone)]
pub struct PinnedOrigin {
    /// Normalized origin string: `scheme://host[:port]`.
    origin: String,
    /// Original hostname (for Host header / TLS SNI).
    host: String,
    /// Explicit port, or the scheme default if none was given.
    port: u16,
    /// Scheme.
    scheme: String,
    /// Validated IP addresses the host resolved to.
    addrs: Vec<IpAddr>,
}

impl PinnedOrigin {
    pub fn origin(&self) -> &str {
        &self.origin
    }
    pub fn host(&self) -> &str {
        &self.host
    }
    pub fn port(&self) -> u16 {
        self.port
    }
    pub fn scheme(&self) -> &str {
        &self.scheme
    }
    pub fn addrs(&self) -> &[IpAddr] {
        &self.addrs
    }
}

impl fmt::Display for PinnedOrigin {
    fn fmt(&self, f: &mut fmt::Formatter<'_>) -> fmt::Result {
        f.write_str(&self.origin)
    }
}

impl fmt::Debug for PinnedOrigin {
    fn fmt(&self, f: &mut fmt::Formatter<'_>) -> fmt::Result {
        f.debug_struct("PinnedOrigin")
            .field("origin", &self.origin)
            .field("host", &self.host)
            .field("port", &self.port)
            .field("scheme", &self.scheme)
            .field("addrs", &self.addrs)
            .finish()
    }
}

/// DNS resolver abstraction. Injected so tests can supply deterministic answers.
pub trait DnsResolver: Send + Sync {
    /// Resolve a hostname to IP addresses. Order is preserved.
    fn resolve(&self, host: &str) -> Result<Vec<IpAddr>, String>;
}

/// Network policy that validates origins against private-network rules.
pub struct NetworkPolicy {
    resolver: Box<dyn DnsResolver>,
}

impl NetworkPolicy {
    pub fn new(resolver: Box<dyn DnsResolver>) -> Self {
        Self { resolver }
    }

    /// Resolve and validate a Hub URL for the given release mode.
    ///
    /// Re-resolves on every call (every new connection boundary). Returns a
    /// [`PinnedOrigin`] with the original hostname and validated addresses.
    pub fn resolve(&self, url: &Url, mode: ReleaseMode) -> Result<PinnedOrigin, NetworkError> {
        let scheme = url.scheme().to_owned();
        let host =
            url.host_str()
                .filter(|h| !h.is_empty())
                .ok_or_else(|| NetworkError::NoAddresses {
                    origin: origin_str(url),
                })?;
        let port = url
            .port_or_known_default()
            .ok_or_else(|| NetworkError::NoAddresses {
                origin: origin_str(url),
            })?;
        let origin = origin_str(url);

        // If the host is already a literal IP, resolve is the IP itself.
        let addrs = if let Ok(ip) = host.parse::<IpAddr>() {
            vec![ip]
        } else {
            self.resolver
                .resolve(host)
                .map_err(|e| NetworkError::Resolve {
                    origin: origin.clone(),
                    message: e,
                })?
        };

        if addrs.is_empty() {
            return Err(NetworkError::NoAddresses { origin });
        }

        validate_ranges(&scheme, &addrs, mode, &origin)?;

        Ok(PinnedOrigin {
            origin,
            host: host.to_owned(),
            port,
            scheme,
            addrs,
        })
    }
}

fn origin_str(url: &Url) -> String {
    let scheme = url.scheme();
    let host = url.host_str().unwrap_or("");
    match url.port() {
        Some(port) => format!("{scheme}://{host}:{port}"),
        None => format!("{scheme}://{host}"),
    }
}

fn validate_ranges(
    scheme: &str,
    addrs: &[IpAddr],
    mode: ReleaseMode,
    origin: &str,
) -> Result<(), NetworkError> {
    let is_https = scheme == "https";
    let is_http = scheme == "http";

    // For HTTP: every resolved address must be private (or loopback per mode).
    // For HTTPS: public or private both allowed; no range restriction.
    if is_https {
        return Ok(());
    }
    if !is_http {
        // Non-http(s) schemes are rejected at parse time upstream; defensive.
        return Ok(());
    }

    // HTTP: classify each address.
    let mut has_loopback = false;
    let mut has_private = false;
    let mut has_public = false;
    for ip in addrs {
        match classify_http_addr(ip) {
            AddrClass::Loopback => has_loopback = true,
            AddrClass::Private => has_private = true,
            AddrClass::Public => has_public = true,
        }
    }

    // Release rejects loopback outright.
    if let ReleaseMode::Release = mode {
        if has_loopback {
            return Err(NetworkError::ReleaseLoopback {
                origin: origin.to_owned(),
            });
        }
    }
    // Debug: loopback allowed only if explicitly opted in.
    if let ReleaseMode::Debug { allow_loopback } = mode {
        if has_loopback && !allow_loopback {
            return Err(NetworkError::ReleaseLoopback {
                origin: origin.to_owned(),
            });
        }
    }

    // Mixed public/private is forbidden.
    if has_public && (has_private || has_loopback) {
        return Err(NetworkError::MixedPublicPrivate {
            origin: origin.to_owned(),
        });
    }

    // HTTP to a public host is forbidden.
    if has_public {
        return Err(NetworkError::HttpPublicHost);
    }

    Ok(())
}

enum AddrClass {
    Loopback,
    Private,
    Public,
}

fn classify_http_addr(ip: &IpAddr) -> AddrClass {
    match ip {
        IpAddr::V4(v4) => classify_v4(v4),
        IpAddr::V6(v6) => classify_v6(v6),
    }
}

fn classify_v4(ip: &Ipv4Addr) -> AddrClass {
    // Loopback 127.0.0.0/8
    if ip.is_loopback() {
        return AddrClass::Loopback;
    }
    // RFC1918 + link-local + CGNAT + loopback-as-private handled separately.
    if ip.is_private() {
        // is_private covers 10/8, 172.16/12, 192.168/16.
        return AddrClass::Private;
    }
    // Link-local 169.254.0.0/16
    if ip.is_link_local() {
        return AddrClass::Private;
    }
    // CGNAT 100.64.0.0/10
    if is_cgnat_v4(ip) {
        return AddrClass::Private;
    }
    AddrClass::Public
}

fn classify_v6(ip: &Ipv6Addr) -> AddrClass {
    // Loopback ::1
    if ip.is_loopback() {
        return AddrClass::Loopback;
    }
    // Unique-local fc00::/7
    if is_unique_local_v6(ip) {
        return AddrClass::Private;
    }
    // Link-local fe80::/10
    if ip.is_unicast_link_local() {
        return AddrClass::Private;
    }
    AddrClass::Public
}

fn is_cgnat_v4(ip: &Ipv4Addr) -> bool {
    let octets = ip.octets();
    octets[0] == 100 && (octets[1] & 0xc0) == 0x40 // 100.64.0.0/10
}

fn is_unique_local_v6(ip: &Ipv6Addr) -> bool {
    // fc00::/7 — first byte high 7 bits zero: 0xfc..0xfd
    let seg = ip.segments()[0];
    (seg & 0xfe00) == 0xfc00
}

/// Static resolver for tests: maps hostnames to fixed address sets.
pub struct StaticResolver {
    map: std::collections::HashMap<String, Vec<IpAddr>>,
}

impl StaticResolver {
    pub fn new() -> Self {
        Self {
            map: std::collections::HashMap::new(),
        }
    }
    pub fn with(mut self, host: &str, addrs: Vec<IpAddr>) -> Self {
        self.map.insert(host.to_owned(), addrs);
        self
    }
}

/// Test resolver that resolves any hostname to a fixed private IPv4 address.
/// Useful for profile tests that use HTTPS origins (range-agnostic).
pub struct AlwaysPrivateResolver;

impl DnsResolver for AlwaysPrivateResolver {
    fn resolve(&self, _host: &str) -> Result<Vec<IpAddr>, String> {
        Ok(vec![IpAddr::V4(Ipv4Addr::new(192, 168, 1, 1))])
    }
}
impl Default for StaticResolver {
    fn default() -> Self {
        Self::new()
    }
}

impl DnsResolver for StaticResolver {
    fn resolve(&self, host: &str) -> Result<Vec<IpAddr>, String> {
        self.map
            .get(host)
            .cloned()
            .ok_or_else(|| format!("no fixture for host {host}"))
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    const PRIVATE4: &str = "192.168.1.20";
    const PUBLIC4: &str = "203.0.113.10";

    fn url_for(scheme: &str, host: &str, port: Option<u16>) -> Url {
        match port {
            Some(p) => Url::parse(&format!("{scheme}://{host}:{p}/api/health")).unwrap(),
            None => Url::parse(&format!("{scheme}://{host}/api/health")).unwrap(),
        }
    }

    // -- HTTPS public host ----------------------------------------------------

    #[test]
    fn https_public_host_allowed_in_release() {
        let r = StaticResolver::new().with("hub.example.com", vec![PUBLIC4.parse().unwrap()]);
        let policy = NetworkPolicy::new(Box::new(r));
        let url = url_for("https", "hub.example.com", None);
        let pinned = policy.resolve(&url, ReleaseMode::Release).unwrap();
        assert_eq!(pinned.scheme(), "https");
        assert_eq!(pinned.host(), "hub.example.com");
        assert_eq!(pinned.port(), 443);
        assert_eq!(pinned.addrs().len(), 1);
    }

    #[test]
    fn https_public_host_allowed_in_debug() {
        let r = StaticResolver::new().with("hub.example.com", vec![PUBLIC4.parse().unwrap()]);
        let policy = NetworkPolicy::new(Box::new(r));
        let url = url_for("https", "hub.example.com", None);
        policy
            .resolve(
                &url,
                ReleaseMode::Debug {
                    allow_loopback: false,
                },
            )
            .unwrap();
    }

    #[test]
    fn https_private_host_allowed_in_release() {
        let r = StaticResolver::new().with("hub.local", vec![PRIVATE4.parse().unwrap()]);
        let policy = NetworkPolicy::new(Box::new(r));
        let url = url_for("https", "hub.local", Some(8443));
        let pinned = policy.resolve(&url, ReleaseMode::Release).unwrap();
        assert_eq!(pinned.origin(), "https://hub.local:8443");
    }

    // -- HTTP private ranges --------------------------------------------------

    #[test]
    fn http_rfc1918_allowed_in_release() {
        for addr in ["192.168.1.20", "10.0.0.5", "172.16.0.1"] {
            let r = StaticResolver::new().with("hub", vec![addr.parse().unwrap()]);
            let policy = NetworkPolicy::new(Box::new(r));
            let url = url_for("http", "hub", Some(9180));
            policy.resolve(&url, ReleaseMode::Release).unwrap();
        }
    }

    #[test]
    fn http_cgnat_allowed_in_release() {
        let r = StaticResolver::new().with("hub", vec!["100.64.0.1".parse().unwrap()]);
        let policy = NetworkPolicy::new(Box::new(r));
        let url = url_for("http", "hub", Some(9180));
        policy.resolve(&url, ReleaseMode::Release).unwrap();
    }

    #[test]
    fn http_ula_v6_allowed_in_release() {
        let r = StaticResolver::new().with("hub", vec!["fd00::1".parse().unwrap()]);
        let policy = NetworkPolicy::new(Box::new(r));
        let url = url_for("http", "hub", Some(9180));
        policy.resolve(&url, ReleaseMode::Release).unwrap();
    }

    #[test]
    fn http_link_local_v6_allowed_in_release() {
        let r = StaticResolver::new().with("hub", vec!["fe80::1".parse().unwrap()]);
        let policy = NetworkPolicy::new(Box::new(r));
        let url = url_for("http", "hub", Some(9180));
        policy.resolve(&url, ReleaseMode::Release).unwrap();
    }

    #[test]
    fn http_link_local_v4_allowed_in_release() {
        let r = StaticResolver::new().with("hub", vec!["169.254.1.1".parse().unwrap()]);
        let policy = NetworkPolicy::new(Box::new(r));
        let url = url_for("http", "hub", Some(9180));
        policy.resolve(&url, ReleaseMode::Release).unwrap();
    }

    // -- HTTP loopback --------------------------------------------------------

    #[test]
    fn http_loopback_rejected_in_release() {
        let r = StaticResolver::new().with("hub", vec!["127.0.0.1".parse().unwrap()]);
        let policy = NetworkPolicy::new(Box::new(r));
        let url = url_for("http", "hub", Some(9180));
        let err = policy.resolve(&url, ReleaseMode::Release).unwrap_err();
        assert!(matches!(err, NetworkError::ReleaseLoopback { .. }));
    }

    #[test]
    fn http_loopback_rejected_in_debug_without_opt_in() {
        let r = StaticResolver::new().with("hub", vec!["127.0.0.1".parse().unwrap()]);
        let policy = NetworkPolicy::new(Box::new(r));
        let url = url_for("http", "hub", Some(9180));
        let err = policy
            .resolve(
                &url,
                ReleaseMode::Debug {
                    allow_loopback: false,
                },
            )
            .unwrap_err();
        assert!(matches!(err, NetworkError::ReleaseLoopback { .. }));
    }

    #[test]
    fn http_loopback_allowed_in_debug_with_opt_in() {
        let r = StaticResolver::new().with("hub", vec!["127.0.0.1".parse().unwrap()]);
        let policy = NetworkPolicy::new(Box::new(r));
        let url = url_for("http", "hub", Some(9180));
        policy
            .resolve(
                &url,
                ReleaseMode::Debug {
                    allow_loopback: true,
                },
            )
            .unwrap();
    }

    // -- Mixed DNS answers ----------------------------------------------------

    #[test]
    fn mixed_public_private_answers_rejected() {
        let r = StaticResolver::new().with(
            "hub",
            vec![PRIVATE4.parse().unwrap(), PUBLIC4.parse().unwrap()],
        );
        let policy = NetworkPolicy::new(Box::new(r));
        let url = url_for("http", "hub", Some(9180));
        let err = policy.resolve(&url, ReleaseMode::Release).unwrap_err();
        assert!(matches!(err, NetworkError::MixedPublicPrivate { .. }));
    }

    #[test]
    fn http_public_host_rejected() {
        let r = StaticResolver::new().with("hub", vec![PUBLIC4.parse().unwrap()]);
        let policy = NetworkPolicy::new(Box::new(r));
        let url = url_for("http", "hub", Some(9180));
        let err = policy.resolve(&url, ReleaseMode::Release).unwrap_err();
        assert!(matches!(err, NetworkError::HttpPublicHost));
    }

    // -- DNS rebinding between connections ------------------------------------

    #[test]
    fn rebinding_between_connections_re_resolves() {
        use std::sync::atomic::{AtomicUsize, Ordering};
        use std::sync::Mutex;

        struct Rebinding {
            answers: Mutex<Vec<Vec<IpAddr>>>,
            call: AtomicUsize,
        }
        impl DnsResolver for Rebinding {
            fn resolve(&self, _host: &str) -> Result<Vec<IpAddr>, String> {
                let i = self.call.fetch_add(1, Ordering::SeqCst);
                let answers = self.answers.lock().unwrap();
                Ok(answers
                    .get(i)
                    .cloned()
                    .unwrap_or_else(|| vec![PRIVATE4.parse::<IpAddr>().unwrap()]))
            }
        }
        let r = Rebinding {
            answers: Mutex::new(vec![
                vec![PRIVATE4.parse().unwrap()],
                vec![PUBLIC4.parse().unwrap()], // rebind to public
            ]),
            call: AtomicUsize::new(0),
        };
        let policy = NetworkPolicy::new(Box::new(r));
        let url = url_for("http", "hub", Some(9180));
        // First connection: private, ok.
        policy.resolve(&url, ReleaseMode::Release).unwrap();
        // Second connection: rebinds to public HTTP → rejected.
        let err = policy.resolve(&url, ReleaseMode::Release).unwrap_err();
        assert!(matches!(err, NetworkError::HttpPublicHost));
    }

    // -- Literal IP host ------------------------------------------------------

    #[test]
    fn literal_private_ip_http_allowed_in_release() {
        let r = StaticResolver::new();
        let policy = NetworkPolicy::new(Box::new(r));
        let url = url_for("http", "192.168.1.20", Some(9180));
        policy.resolve(&url, ReleaseMode::Release).unwrap();
    }

    // -- Redirect refusal ----------------------------------------------------
    // Redirects are disabled at the HTTP client layer (Task 6). Network policy
    // validates origins; a redirect to another origin is rejected by refusing
    // to follow. We assert the policy pins the original host.

    #[test]
    fn pinned_origin_retains_original_host_for_tls_identity() {
        let r = StaticResolver::new().with("hub.example.com", vec![PRIVATE4.parse().unwrap()]);
        let policy = NetworkPolicy::new(Box::new(r));
        let url = url_for("https", "hub.example.com", None);
        let pinned = policy.resolve(&url, ReleaseMode::Release).unwrap();
        assert_eq!(pinned.host(), "hub.example.com");
        assert_eq!(pinned.addrs()[0], PRIVATE4.parse::<IpAddr>().unwrap());
    }

    #[test]
    fn display_exposes_only_origin() {
        let r = StaticResolver::new().with("hub", vec![PRIVATE4.parse().unwrap()]);
        let policy = NetworkPolicy::new(Box::new(r));
        let url = url_for("http", "hub", Some(9180));
        let pinned = policy.resolve(&url, ReleaseMode::Release).unwrap();
        let s = pinned.to_string();
        assert_eq!(s, "http://hub:9180");
    }

    #[test]
    fn no_addresses_resolved_rejected() {
        let r = StaticResolver::new();
        let policy = NetworkPolicy::new(Box::new(r));
        let url = url_for("http", "unknown", Some(9180));
        let err = policy.resolve(&url, ReleaseMode::Release).unwrap_err();
        assert!(matches!(err, NetworkError::Resolve { .. }));
    }

    #[test]
    fn loopback_v6_rejected_in_release_http() {
        let r = StaticResolver::new().with("hub", vec!["::1".parse().unwrap()]);
        let policy = NetworkPolicy::new(Box::new(r));
        let url = url_for("http", "hub", Some(9180));
        let err = policy.resolve(&url, ReleaseMode::Release).unwrap_err();
        assert!(matches!(err, NetworkError::ReleaseLoopback { .. }));
    }
}
