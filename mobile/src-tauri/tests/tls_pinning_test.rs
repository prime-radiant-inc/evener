//! TLS pinning integration tests. The fixture certificate is valid only for
//! `hub.identity.test`, while the listener binds only to 127.0.0.1. Success
//! therefore proves both resolver pinning to the socket and hostname-based
//! SNI/certificate validation; an IP-authority URL or plaintext client fails.

use std::net::{IpAddr, Ipv4Addr};
use std::sync::{Arc, Mutex};
use std::time::Duration;

use app_lib::error::ReleaseMode;
use app_lib::http_transport::{HubHttp, HubRequest};
use app_lib::network_policy::{DnsResolver, NetworkPolicy};
use app_lib::profile::PairingProbe;
use app_lib::profile_runtime::RealPairingProbe;
use futures_util::StreamExt;
use rcgen::{generate_simple_self_signed, CertifiedKey};
use rustls::pki_types::{CertificateDer, PrivateKeyDer, PrivatePkcs8KeyDer};
use tokio::io::{AsyncRead, AsyncReadExt, AsyncWrite, AsyncWriteExt};
use tokio::net::TcpListener;
use tokio_rustls::TlsAcceptor;
use tokio_tungstenite::tungstenite::{protocol::Role, Message};

const HOST: &str = "hub.identity.test";
const OVERSIZED_HEALTH_BYTES: usize = 70 * 1024;

struct LoopbackResolver;

impl DnsResolver for LoopbackResolver {
    fn resolve(&self, _host: &str) -> Result<Vec<IpAddr>, String> {
        Ok(vec![IpAddr::V4(Ipv4Addr::LOCALHOST)])
    }
}

#[derive(Clone, Copy)]
enum PairingHandshake {
    None,
    Accept { token: &'static str },
    Malformed { token: &'static str },
}

#[derive(Clone, Copy)]
enum HealthBehavior {
    Healthy,
    Stall,
    Oversized,
}

#[derive(Clone)]
struct ScriptState {
    pairing: PairingHandshake,
    health: HealthBehavior,
    requests: Arc<Mutex<Vec<String>>>,
    close_codes: Arc<Mutex<Vec<u16>>>,
    health_seen: Arc<tokio::sync::Notify>,
    health_release: Arc<tokio::sync::Notify>,
}

impl ScriptState {
    fn new(pairing: PairingHandshake, health: HealthBehavior) -> Self {
        Self {
            pairing,
            health,
            requests: Arc::new(Mutex::new(Vec::new())),
            close_codes: Arc::new(Mutex::new(Vec::new())),
            health_seen: Arc::new(tokio::sync::Notify::new()),
            health_release: Arc::new(tokio::sync::Notify::new()),
        }
    }

    async fn wait_for_health_request(&self) {
        loop {
            let seen = self.health_seen.notified();
            if self
                .requests
                .lock()
                .unwrap()
                .iter()
                .any(|request| request.starts_with("GET /api/health "))
            {
                return;
            }
            seen.await;
        }
    }
}

async fn serve_scripted_connection<S>(mut stream: S, script: ScriptState)
where
    S: AsyncRead + AsyncWrite + Unpin,
{
    let mut bytes = Vec::new();
    let mut buffer = [0_u8; 1024];
    loop {
        let count = stream.read(&mut buffer).await.unwrap_or(0);
        if count == 0 {
            break;
        }
        bytes.extend_from_slice(&buffer[..count]);
        if bytes.windows(4).any(|window| window == b"\r\n\r\n") {
            break;
        }
    }
    let request = String::from_utf8_lossy(&bytes).into_owned();
    script.requests.lock().unwrap().push(request.clone());

    if request.starts_with("GET /rpc ") {
        let expected_token = match script.pairing {
            PairingHandshake::None => None,
            PairingHandshake::Accept { token } | PairingHandshake::Malformed { token } => {
                Some(token)
            }
        };
        let has_expected_auth = expected_token.is_some_and(|token| {
            request
                .to_ascii_lowercase()
                .contains(&format!("authorization: bearer {token}").to_ascii_lowercase())
        });
        if !has_expected_auth {
            let _ = stream
                .write_all(
                    b"HTTP/1.1 401 Unauthorized\r\nContent-Length: 0\r\nConnection: close\r\n\r\n",
                )
                .await;
            let _ = stream.shutdown().await;
            return;
        }
        if matches!(script.pairing, PairingHandshake::Malformed { .. }) {
            let _ = stream
                .write_all(b"HTTP/1.1 200 OK\r\nContent-Length: 0\r\nConnection: close\r\n\r\n")
                .await;
            let _ = stream.shutdown().await;
            return;
        }
        let key = request
            .lines()
            .find_map(|line| {
                line.split_once(':').and_then(|(name, value)| {
                    name.eq_ignore_ascii_case("sec-websocket-key")
                        .then(|| value.trim())
                })
            })
            .unwrap();
        let accept = tokio_tungstenite::tungstenite::handshake::derive_accept_key(key.as_bytes());
        let response = format!(
            "HTTP/1.1 101 Switching Protocols\r\nConnection: Upgrade\r\nUpgrade: websocket\r\nSec-WebSocket-Accept: {accept}\r\n\r\n"
        );
        let _ = stream.write_all(response.as_bytes()).await;
        let mut websocket =
            tokio_tungstenite::WebSocketStream::from_raw_socket(stream, Role::Server, None).await;
        while let Some(message) = websocket.next().await {
            match message {
                Ok(Message::Close(frame)) => {
                    script
                        .close_codes
                        .lock()
                        .unwrap()
                        .push(frame.map_or(1005, |frame| u16::from(frame.code)));
                    break;
                }
                Ok(_) => {}
                Err(_) => break,
            }
        }
        return;
    }

    if request.starts_with("GET /api/health ") {
        script.health_seen.notify_waiters();
        match script.health {
            HealthBehavior::Healthy => {
                let body = r#"{"mobile_api_version":1}"#;
                let response = format!(
                    "HTTP/1.1 200 OK\r\nContent-Type: application/json\r\nContent-Length: {}\r\nConnection: close\r\n\r\n{}",
                    body.len(), body
                );
                let _ = stream.write_all(response.as_bytes()).await;
            }
            HealthBehavior::Stall => {
                script.health_release.notified().await;
            }
            HealthBehavior::Oversized => {
                let mut body = br#"{"mobile_api_version":1}"#.to_vec();
                body.resize(OVERSIZED_HEALTH_BYTES, b' ');
                let _ = stream
                    .write_all(
                        b"HTTP/1.1 200 OK\r\nContent-Type: application/json\r\nConnection: close\r\n\r\n",
                    )
                    .await;
                let _ = stream.write_all(&body).await;
            }
        }
        let _ = stream.shutdown().await;
        return;
    }

    let (status, body) = if request.starts_with("GET /api/mobile/pairing ") {
        (
            "503 Service Unavailable",
            r#"{"error":"route unavailable"}"#,
        )
    } else {
        ("404 Not Found", r#"{"error":"not found"}"#)
    };
    let response = format!(
        "HTTP/1.1 {status}\r\nContent-Type: application/json\r\nContent-Length: {}\r\nConnection: close\r\n\r\n{}",
        body.len(), body
    );
    let _ = stream.write_all(response.as_bytes()).await;
    let _ = stream.shutdown().await;
}

struct TlsFixture {
    port: u16,
    root: reqwest::Certificate,
    root_der: Vec<u8>,
    server_names: Arc<Mutex<Vec<String>>>,
    script: ScriptState,
}

impl TlsFixture {
    async fn start(expected_connections: usize) -> Self {
        Self::start_scripted(
            expected_connections,
            PairingHandshake::None,
            HealthBehavior::Healthy,
        )
        .await
    }

    async fn start_with_pairing(expected_connections: usize, pairing: PairingHandshake) -> Self {
        Self::start_scripted(expected_connections, pairing, HealthBehavior::Healthy).await
    }

    async fn start_scripted(
        expected_connections: usize,
        pairing: PairingHandshake,
        health: HealthBehavior,
    ) -> Self {
        let CertifiedKey { cert, signing_key } =
            generate_simple_self_signed(vec![HOST.to_owned()]).unwrap();
        let cert_der: CertificateDer<'static> = cert.der().clone();
        let key_der = PrivateKeyDer::Pkcs8(PrivatePkcs8KeyDer::from(signing_key.serialize_der()));
        let config = rustls::ServerConfig::builder()
            .with_no_client_auth()
            .with_single_cert(vec![cert_der.clone()], key_der)
            .unwrap();
        let acceptor = TlsAcceptor::from(Arc::new(config));
        let listener = TcpListener::bind((Ipv4Addr::LOCALHOST, 0)).await.unwrap();
        let port = listener.local_addr().unwrap().port();
        let server_names = Arc::new(Mutex::new(Vec::new()));
        let names = server_names.clone();
        let script = ScriptState::new(pairing, health);
        let server_script = script.clone();

        tokio::spawn(async move {
            for _ in 0..expected_connections {
                let (socket, _) = listener.accept().await.unwrap();
                let tls = match acceptor.accept(socket).await {
                    Ok(tls) => tls,
                    Err(_) => continue,
                };
                if let Some(name) = tls.get_ref().1.server_name() {
                    names.lock().unwrap().push(name.to_owned());
                }
                serve_scripted_connection(tls, server_script.clone()).await;
            }
        });

        Self {
            port,
            root: reqwest::Certificate::from_der(cert_der.as_ref()).unwrap(),
            root_der: cert_der.as_ref().to_vec(),
            server_names,
            script,
        }
    }

    fn policy() -> Arc<NetworkPolicy> {
        Arc::new(NetworkPolicy::new(Box::new(LoopbackResolver)))
    }

    fn origin(&self, host: &str) -> String {
        format!("https://{host}:{}", self.port)
    }
}

struct PlainPairingFixture {
    port: u16,
    script: ScriptState,
}

impl PlainPairingFixture {
    async fn start() -> Self {
        let listener = TcpListener::bind((Ipv4Addr::LOCALHOST, 0)).await.unwrap();
        let port = listener.local_addr().unwrap().port();
        let script = ScriptState::new(
            PairingHandshake::Accept {
                token: "plain-pairing-bearer",
            },
            HealthBehavior::Healthy,
        );
        let server_script = script.clone();
        tokio::spawn(async move {
            for _ in 0..2 {
                let (stream, _) = listener.accept().await.unwrap();
                serve_scripted_connection(stream, server_script.clone()).await;
            }
        });
        Self { port, script }
    }

    fn origin(&self) -> String {
        format!("http://{HOST}:{}", self.port)
    }
}

fn request_header<'a>(request: &'a str, expected_name: &str) -> Option<&'a str> {
    request.lines().find_map(|line| {
        line.split_once(':').and_then(|(name, value)| {
            name.eq_ignore_ascii_case(expected_name)
                .then(|| value.trim())
        })
    })
}

#[tokio::test(flavor = "multi_thread", worker_threads = 2)]
async fn hub_https_connects_to_pinned_socket_with_original_tls_identity() {
    let fixture = TlsFixture::start(1).await;
    let client = HubHttp::new(
        fixture.origin(HOST),
        "native-bearer".to_owned(),
        TlsFixture::policy(),
        ReleaseMode::Release,
    )
    .with_root_certificate(fixture.root.clone());

    client
        .request(HubRequest {
            method: "GET".to_owned(),
            path: "/api/health".to_owned(),
            body: None,
            media_type: None,
        })
        .await
        .unwrap();

    assert_eq!(fixture.server_names.lock().unwrap().as_slice(), [HOST]);
    let requests = fixture.script.requests.lock().unwrap();
    assert_eq!(requests.len(), 1);
    let request = requests[0].to_ascii_lowercase();
    assert!(request.contains(&format!("host: {HOST}:{}", fixture.port)));
    assert!(request.contains("authorization: bearer native-bearer"));
}

#[tokio::test(flavor = "multi_thread", worker_threads = 2)]
async fn hub_https_rejects_certificate_for_different_original_hostname() {
    let fixture = TlsFixture::start(1).await;
    let client = HubHttp::new(
        fixture.origin("wrong.identity.test"),
        "native-bearer".to_owned(),
        TlsFixture::policy(),
        ReleaseMode::Release,
    )
    .with_root_certificate(fixture.root.clone());

    assert!(client
        .request(HubRequest {
            method: "GET".to_owned(),
            path: "/api/health".to_owned(),
            body: None,
            media_type: None,
        })
        .await
        .is_err());
}

#[tokio::test(flavor = "multi_thread", worker_threads = 4)]
async fn pairing_probe_uses_tls_for_health_and_authenticated_appwire_with_original_hostname() {
    let fixture = TlsFixture::start_with_pairing(
        2,
        PairingHandshake::Accept {
            token: "pairing-bearer",
        },
    )
    .await;
    let probe = RealPairingProbe::new(TlsFixture::policy())
        .with_root_certificate_der(fixture.root_der.clone())
        .unwrap();
    let version = probe
        .probe(
            &fixture.origin(HOST),
            "pairing-bearer",
            ReleaseMode::Release,
        )
        .unwrap();

    assert_eq!(version, 1);
    assert_eq!(
        fixture.server_names.lock().unwrap().as_slice(),
        [HOST, HOST]
    );
    let requests = fixture.script.requests.lock().unwrap();
    assert_eq!(requests.len(), 2);
    assert!(requests[0].starts_with("GET /api/health "));
    assert!(!requests[0].to_ascii_lowercase().contains("authorization:"));
    assert!(requests[1].starts_with("GET /rpc "));
    assert_eq!(
        request_header(&requests[1], "Authorization"),
        Some("Bearer pairing-bearer")
    );
    let expected_host = format!("{HOST}:{}", fixture.port);
    let expected_origin = format!("wss://{HOST}:{}", fixture.port);
    assert_eq!(
        request_header(&requests[1], "Host"),
        Some(expected_host.as_str())
    );
    assert_eq!(
        request_header(&requests[1], "Origin"),
        Some(expected_origin.as_str())
    );
    assert_eq!(
        fixture.script.close_codes.lock().unwrap().as_slice(),
        [1000]
    );
}

#[tokio::test(flavor = "multi_thread", worker_threads = 4)]
async fn pairing_probe_converts_http_origin_to_ws_rpc_with_exact_headers() {
    let fixture = PlainPairingFixture::start().await;
    let probe = RealPairingProbe::new(TlsFixture::policy());
    let version = probe
        .probe(
            &fixture.origin(),
            "plain-pairing-bearer",
            ReleaseMode::Debug {
                allow_loopback: true,
            },
        )
        .unwrap();

    assert_eq!(version, 1);
    let requests = fixture.script.requests.lock().unwrap();
    assert_eq!(requests.len(), 2);
    assert!(requests[1].starts_with("GET /rpc "));
    let expected_host = format!("{HOST}:{}", fixture.port);
    let expected_origin = format!("ws://{HOST}:{}", fixture.port);
    assert_eq!(
        request_header(&requests[1], "Host"),
        Some(expected_host.as_str())
    );
    assert_eq!(
        request_header(&requests[1], "Origin"),
        Some(expected_origin.as_str())
    );
    assert_eq!(
        request_header(&requests[1], "Authorization"),
        Some("Bearer plain-pairing-bearer")
    );
}

#[tokio::test(flavor = "multi_thread", worker_threads = 4)]
async fn pairing_probe_bounds_stalled_health_request() {
    let fixture =
        TlsFixture::start_scripted(1, PairingHandshake::None, HealthBehavior::Stall).await;
    let origin = fixture.origin(HOST);
    let probe = RealPairingProbe::new(TlsFixture::policy())
        .with_root_certificate_der(fixture.root_der.clone())
        .unwrap()
        .with_pairing_timeout(Duration::from_millis(250));

    let task = tokio::task::spawn_blocking(move || {
        probe.probe(&origin, "stalled-health-token", ReleaseMode::Release)
    });
    fixture.script.wait_for_health_request().await;
    let error = task.await.unwrap().unwrap_err();
    fixture.script.health_release.notify_one();

    let message = error.to_string();
    assert_eq!(message, "pairing probe timed out");
    assert!(!message.contains("stalled-health-token"));
}

#[tokio::test(flavor = "multi_thread", worker_threads = 4)]
async fn pairing_probe_rejects_oversized_health_before_json_parse() {
    let fixture =
        TlsFixture::start_scripted(1, PairingHandshake::None, HealthBehavior::Oversized).await;
    let probe = RealPairingProbe::new(TlsFixture::policy())
        .with_root_certificate_der(fixture.root_der.clone())
        .unwrap();

    let error = probe
        .probe(
            &fixture.origin(HOST),
            "oversized-health-token",
            ReleaseMode::Release,
        )
        .unwrap_err();
    let message = error.to_string();
    assert!(message.contains("health response too large"), "{message}");
    assert!(!message.contains("oversized-health-token"));
    let requests = fixture.script.requests.lock().unwrap();
    assert_eq!(
        requests.len(),
        1,
        "oversized health must stop before AppWire"
    );
}

#[tokio::test(flavor = "multi_thread", worker_threads = 4)]
async fn pairing_probe_rejects_invalid_appwire_token() {
    let fixture = TlsFixture::start_with_pairing(
        2,
        PairingHandshake::Accept {
            token: "expected-bearer",
        },
    )
    .await;
    let probe = RealPairingProbe::new(TlsFixture::policy())
        .with_root_certificate_der(fixture.root_der.clone())
        .unwrap();

    assert!(probe
        .probe(
            &fixture.origin(HOST),
            "invalid-bearer",
            ReleaseMode::Release,
        )
        .is_err());
    let requests = fixture.script.requests.lock().unwrap();
    assert_eq!(requests.len(), 2);
    assert!(requests[1].starts_with("GET /rpc "));
    assert_eq!(
        request_header(&requests[1], "Authorization"),
        Some("Bearer invalid-bearer")
    );
}

#[tokio::test(flavor = "multi_thread", worker_threads = 4)]
async fn pairing_probe_rejects_invalid_appwire_handshake() {
    let fixture = TlsFixture::start_with_pairing(
        2,
        PairingHandshake::Malformed {
            token: "pairing-bearer",
        },
    )
    .await;
    let probe = RealPairingProbe::new(TlsFixture::policy())
        .with_root_certificate_der(fixture.root_der.clone())
        .unwrap();

    assert!(probe
        .probe(
            &fixture.origin(HOST),
            "pairing-bearer",
            ReleaseMode::Release,
        )
        .is_err());
    let requests = fixture.script.requests.lock().unwrap();
    assert_eq!(requests.len(), 2);
    assert!(requests[1].starts_with("GET /rpc "));
}
