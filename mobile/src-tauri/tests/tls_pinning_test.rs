//! TLS pinning integration tests. The fixture certificate is valid only for
//! `hub.identity.test`, while the listener binds only to 127.0.0.1. Success
//! therefore proves both resolver pinning to the socket and hostname-based
//! SNI/certificate validation; an IP-authority URL or plaintext client fails.

use std::net::{IpAddr, Ipv4Addr};
use std::sync::{Arc, Mutex};

use app_lib::error::ReleaseMode;
use app_lib::http_transport::{HubHttp, HubRequest};
use app_lib::network_policy::{DnsResolver, NetworkPolicy};
use app_lib::profile::PairingProbe;
use app_lib::profile_runtime::RealPairingProbe;
use futures_util::StreamExt;
use rcgen::{generate_simple_self_signed, CertifiedKey};
use rustls::pki_types::{CertificateDer, PrivateKeyDer, PrivatePkcs8KeyDer};
use tokio::io::{AsyncReadExt, AsyncWriteExt};
use tokio::net::TcpListener;
use tokio_rustls::TlsAcceptor;
use tokio_tungstenite::tungstenite::{protocol::Role, Message};

const HOST: &str = "hub.identity.test";

struct LoopbackResolver;

impl DnsResolver for LoopbackResolver {
    fn resolve(&self, _host: &str) -> Result<Vec<IpAddr>, String> {
        Ok(vec![IpAddr::V4(Ipv4Addr::LOCALHOST)])
    }
}

struct TlsFixture {
    port: u16,
    root: reqwest::Certificate,
    root_der: Vec<u8>,
    server_names: Arc<Mutex<Vec<String>>>,
    requests: Arc<Mutex<Vec<String>>>,
    close_codes: Arc<Mutex<Vec<u16>>>,
}

#[derive(Clone, Copy)]
enum PairingHandshake {
    None,
    Accept { token: &'static str },
    Malformed { token: &'static str },
}

impl TlsFixture {
    async fn start(expected_connections: usize) -> Self {
        Self::start_with_pairing(expected_connections, PairingHandshake::None).await
    }

    async fn start_with_pairing(expected_connections: usize, pairing: PairingHandshake) -> Self {
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
        let requests = Arc::new(Mutex::new(Vec::new()));
        let close_codes = Arc::new(Mutex::new(Vec::new()));
        let names = server_names.clone();
        let captured = requests.clone();
        let observed_closes = close_codes.clone();

        tokio::spawn(async move {
            for _ in 0..expected_connections {
                let (socket, _) = listener.accept().await.unwrap();
                let mut tls = match acceptor.accept(socket).await {
                    Ok(tls) => tls,
                    Err(_) => continue,
                };
                if let Some(name) = tls.get_ref().1.server_name() {
                    names.lock().unwrap().push(name.to_owned());
                }
                let mut bytes = Vec::new();
                let mut buffer = [0_u8; 1024];
                loop {
                    let count = tls.read(&mut buffer).await.unwrap_or(0);
                    if count == 0 {
                        break;
                    }
                    bytes.extend_from_slice(&buffer[..count]);
                    if bytes.windows(4).any(|window| window == b"\r\n\r\n") {
                        break;
                    }
                }
                let request = String::from_utf8_lossy(&bytes).into_owned();
                captured.lock().unwrap().push(request.clone());
                if request.starts_with("GET /rpc ") {
                    let expected_token = match pairing {
                        PairingHandshake::None => None,
                        PairingHandshake::Accept { token }
                        | PairingHandshake::Malformed { token } => Some(token),
                    };
                    let has_expected_auth = expected_token.is_some_and(|token| {
                        request.to_ascii_lowercase().contains(
                            &format!("authorization: bearer {token}").to_ascii_lowercase(),
                        )
                    });
                    if !has_expected_auth {
                        let _ = tls
                            .write_all(
                                b"HTTP/1.1 401 Unauthorized\r\nContent-Length: 0\r\nConnection: close\r\n\r\n",
                            )
                            .await;
                        let _ = tls.shutdown().await;
                        continue;
                    }
                    if matches!(pairing, PairingHandshake::Malformed { .. }) {
                        let _ = tls
                            .write_all(
                                b"HTTP/1.1 200 OK\r\nContent-Length: 0\r\nConnection: close\r\n\r\n",
                            )
                            .await;
                        let _ = tls.shutdown().await;
                        continue;
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
                    let accept = tokio_tungstenite::tungstenite::handshake::derive_accept_key(
                        key.as_bytes(),
                    );
                    let response = format!(
                        "HTTP/1.1 101 Switching Protocols\r\nConnection: Upgrade\r\nUpgrade: websocket\r\nSec-WebSocket-Accept: {accept}\r\n\r\n"
                    );
                    let _ = tls.write_all(response.as_bytes()).await;
                    let mut websocket = tokio_tungstenite::WebSocketStream::from_raw_socket(
                        tls,
                        Role::Server,
                        None,
                    )
                    .await;
                    while let Some(message) = websocket.next().await {
                        match message {
                            Ok(Message::Close(frame)) => {
                                observed_closes
                                    .lock()
                                    .unwrap()
                                    .push(frame.map_or(1005, |frame| u16::from(frame.code)));
                                break;
                            }
                            Ok(_) => {}
                            Err(_) => break,
                        }
                    }
                    continue;
                }

                let (status, body) = if request.starts_with("GET /api/health ") {
                    ("200 OK", r#"{"mobile_api_version":1}"#)
                } else if request.starts_with("GET /api/mobile/pairing ") {
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
                let _ = tls.write_all(response.as_bytes()).await;
                let _ = tls.shutdown().await;
            }
        });

        Self {
            port,
            root: reqwest::Certificate::from_der(cert_der.as_ref()).unwrap(),
            root_der: cert_der.as_ref().to_vec(),
            server_names,
            requests,
            close_codes,
        }
    }

    fn policy() -> Arc<NetworkPolicy> {
        Arc::new(NetworkPolicy::new(Box::new(LoopbackResolver)))
    }

    fn origin(&self, host: &str) -> String {
        format!("https://{host}:{}", self.port)
    }
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
    let requests = fixture.requests.lock().unwrap();
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
    let requests = fixture.requests.lock().unwrap();
    assert_eq!(requests.len(), 2);
    assert!(requests[0].starts_with("GET /api/health "));
    assert!(!requests[0].to_ascii_lowercase().contains("authorization:"));
    assert!(requests[1].starts_with("GET /rpc "));
    assert!(requests[1]
        .to_ascii_lowercase()
        .contains("authorization: bearer pairing-bearer"));
    assert_eq!(fixture.close_codes.lock().unwrap().as_slice(), [1000]);
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
    let requests = fixture.requests.lock().unwrap();
    assert_eq!(requests.len(), 2);
    assert!(requests[1].starts_with("GET /rpc "));
    assert!(requests[1]
        .to_ascii_lowercase()
        .contains("authorization: bearer invalid-bearer"));
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
    let requests = fixture.requests.lock().unwrap();
    assert_eq!(requests.len(), 2);
    assert!(requests[1].starts_with("GET /rpc "));
}
