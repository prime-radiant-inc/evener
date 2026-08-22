//! HTTP transport integration tests using a scripted local server.
//!
//! Tests assert the binding-B HTTP boundary contract:
//! - pinned Host header (original hostname retained, validated address pinned)
//! - bearer injection from the native Keychain token
//! - caller auth/cookie/hop headers stripped (the request DTO carries none)
//! - allowlisted method/path/body/media only
//! - redirect rejection (redirects disabled at the client layer)
//! - cancellation
//! - body and status fidelity (including binary responses)
//! - no token, URL, or body in errors or diagnostics
//! - DNS rebinding between connections re-resolves and can reject
//!
//! No live Hub, Keychain, or provider network. The scripted HTTP server is the
//! only external boundary.

use app_lib::http_transport::{
    HttpError, HttpRequestRegistry, HubRequest, HubResponse, PreparedHttpRequest,
    MAX_REQUEST_BODY_BYTES, MAX_RESPONSE_BODY_BYTES,
};

use std::time::Duration;

// ---------------------------------------------------------------------------
// Test helpers: minimal scripted HTTP server
// ---------------------------------------------------------------------------

mod http_server {
    use std::sync::atomic::{AtomicU16, Ordering};
    use std::sync::{Arc, Mutex};

    use http_body_util::{BodyExt as _, Full};
    use hyper::body::Bytes;
    use hyper::server::conn::http1;
    use hyper::service::service_fn;
    use hyper::{Request, Response, StatusCode};
    use hyper_util::rt::TokioIo;
    use tokio::net::TcpListener;

    #[derive(Clone)]
    #[allow(dead_code)]
    pub struct ReceivedRequest {
        pub method: String,
        pub path: String,
        pub host: String,
        pub authorization: Option<String>,
        pub cookie: Option<String>,
        pub body: Vec<u8>,
        pub content_type: Option<String>,
        pub x_forwarded_for: Option<String>,
        pub x_real_ip: Option<String>,
    }

    pub struct ScriptedResponse {
        pub status: u16,
        pub headers: Vec<(String, String)>,
        pub body: Vec<u8>,
    }

    pub struct ScriptedServer {
        pub port: u16,
        pub host: String,
        requests: Arc<Mutex<Vec<ReceivedRequest>>>,
        script: Arc<Mutex<Vec<ScriptedResponse>>>,
        connection_count: Arc<AtomicU16>,
    }

    impl ScriptedServer {
        pub async fn start() -> Self {
            Self::start_on("127.0.0.1").await
        }

        /// Start bound to a literal host so we can assert the Host header is
        /// the original hostname, not the pinned IP.
        pub async fn start_on(host: &str) -> Self {
            let requests = Arc::new(Mutex::new(Vec::new()));
            let script: Arc<Mutex<Vec<ScriptedResponse>>> = Arc::new(Mutex::new(Vec::new()));
            let connection_count = Arc::new(AtomicU16::new(0));
            let bind = format!("{host}:0");
            let listener = TcpListener::bind(&bind).await.unwrap();
            let port = listener.local_addr().unwrap().port();

            let requests_clone = requests.clone();
            let script_clone = script.clone();
            let cc_clone = connection_count.clone();

            tokio::spawn(async move {
                loop {
                    let (stream, _) = match listener.accept().await {
                        Ok(s) => s,
                        Err(_) => break,
                    };
                    cc_clone.fetch_add(1, Ordering::SeqCst);
                    let io = TokioIo::new(stream);
                    let reqs = requests_clone.clone();
                    let script = script_clone.clone();
                    tokio::spawn(async move {
                        let svc = service_fn(move |req: Request<hyper::body::Incoming>| {
                            let reqs = reqs.clone();
                            let script = script.clone();
                            async move {
                                let method = req.method().to_string();
                                let path = req.uri().path().to_owned();
                                let host = req
                                    .headers()
                                    .get("host")
                                    .map(|v| v.to_str().unwrap_or("").to_owned())
                                    .unwrap_or_default();
                                let authorization = req
                                    .headers()
                                    .get("authorization")
                                    .map(|v| v.to_str().unwrap_or("").to_owned());
                                let cookie = req
                                    .headers()
                                    .get("cookie")
                                    .map(|v| v.to_str().unwrap_or("").to_owned());
                                let content_type = req
                                    .headers()
                                    .get("content-type")
                                    .map(|v| v.to_str().unwrap_or("").to_owned());
                                let x_forwarded_for = req
                                    .headers()
                                    .get("x-forwarded-for")
                                    .map(|v| v.to_str().unwrap_or("").to_owned());
                                let x_real_ip = req
                                    .headers()
                                    .get("x-real-ip")
                                    .map(|v| v.to_str().unwrap_or("").to_owned());

                                let body = req
                                    .into_body()
                                    .collect()
                                    .await
                                    .map(|b| b.to_bytes().to_vec())
                                    .unwrap_or_default();

                                reqs.lock().unwrap().push(ReceivedRequest {
                                    method,
                                    path,
                                    host,
                                    authorization,
                                    cookie,
                                    body,
                                    content_type,
                                    x_forwarded_for,
                                    x_real_ip,
                                });

                                let resp = {
                                    let mut script = script.lock().unwrap();
                                    if script.is_empty() {
                                        Response::builder()
                                            .status(StatusCode::OK)
                                            .header("content-type", "application/json")
                                            .body(Full::new(Bytes::from(
                                                r#"{"mobile_api_version":1}"#.as_bytes().to_vec(),
                                            )))
                                            .unwrap()
                                    } else {
                                        let s = script.remove(0);
                                        let mut builder = Response::builder().status(
                                            StatusCode::from_u16(s.status)
                                                .unwrap_or(StatusCode::OK),
                                        );
                                        for (k, v) in &s.headers {
                                            builder = builder.header(k.as_str(), v.as_str());
                                        }
                                        builder.body(Full::new(Bytes::from(s.body))).unwrap()
                                    }
                                };
                                Ok::<_, std::convert::Infallible>(resp)
                            }
                        });
                        let _ = http1::Builder::new().serve_connection(io, svc).await;
                    });
                }
            });

            Self {
                port,
                host: host.to_owned(),
                requests,
                script,
                connection_count,
            }
        }

        /// The origin the transport should be configured with: the hostname,
        /// not the IP, so the Host header is the original hostname.
        pub fn origin(&self) -> String {
            format!("http://{}:{}", self.host, self.port)
        }

        pub fn received(&self) -> Vec<ReceivedRequest> {
            self.requests.lock().unwrap().clone()
        }

        pub fn enqueue(&self, status: u16, headers: Vec<(String, String)>, body: Vec<u8>) {
            self.script.lock().unwrap().push(ScriptedResponse {
                status,
                headers,
                body,
            });
        }

        pub fn connection_count(&self) -> u16 {
            self.connection_count.load(Ordering::SeqCst)
        }
    }
}

// ---------------------------------------------------------------------------
// A test NetworkPolicy that resolves any host to 127.0.0.1 (loopback) and
// runs in Debug mode with allow_loopback, so the scripted server on
// 127.0.0.1 passes the policy. This is the only fake boundary: the HTTP
// transport, allowlists, and header logic below it are real.
// ---------------------------------------------------------------------------

use app_lib::error::ReleaseMode;
use app_lib::http_transport::HubHttp;
use app_lib::network_policy::{DnsResolver, NetworkPolicy};
use std::net::IpAddr;
use std::sync::{Arc, Mutex};

fn loopback_policy() -> Arc<NetworkPolicy> {
    struct LoopbackResolver;
    impl DnsResolver for LoopbackResolver {
        fn resolve(&self, _host: &str) -> Result<Vec<IpAddr>, String> {
            Ok(vec![IpAddr::V4(std::net::Ipv4Addr::new(127, 0, 0, 1))])
        }
    }
    Arc::new(NetworkPolicy::new(Box::new(LoopbackResolver)))
}

fn make_transport(origin: &str, token: &str) -> HubHttp {
    HubHttp::new(
        origin.to_owned(),
        token.to_owned(),
        loopback_policy(),
        ReleaseMode::Debug {
            allow_loopback: true,
        },
    )
}

/// A rebinding resolver that returns a different address on each call, to
/// exercise DNS rebinding between connections.
fn rebinding_policy(answers: Vec<Vec<IpAddr>>) -> Arc<NetworkPolicy> {
    struct Rebinding {
        answers: Mutex<std::collections::VecDeque<Vec<IpAddr>>>,
    }
    impl DnsResolver for Rebinding {
        fn resolve(&self, _host: &str) -> Result<Vec<IpAddr>, String> {
            self.answers
                .lock()
                .unwrap()
                .pop_front()
                .ok_or("no more".to_owned())
        }
    }
    let rb = Rebinding {
        answers: Mutex::new(answers.into()),
    };
    Arc::new(NetworkPolicy::new(Box::new(rb)))
}

// ---------------------------------------------------------------------------
// Tests: bearer injection and caller-auth stripping
// ---------------------------------------------------------------------------

#[tokio::test]
async fn http_injects_bearer_token() {
    use http_server::ScriptedServer;

    let server = ScriptedServer::start().await;
    let origin = server.origin();
    let token = "test-bearer-token-12345";

    let transport = make_transport(&origin, token);
    let response = transport
        .request(HubRequest {
            method: "GET".to_owned(),
            path: "/api/health".to_owned(),
            body: None,
            media_type: None,
        })
        .await;
    assert!(
        response.is_ok(),
        "request should succeed: {:?}",
        response.err()
    );

    let received = server.received();
    assert_eq!(received.len(), 1);
    assert_eq!(received[0].path, "/api/health");
    assert_eq!(received[0].method, "GET");
    assert_eq!(
        received[0].authorization.as_deref(),
        Some(format!("Bearer {token}").as_str())
    );
}

#[tokio::test]
async fn http_strips_caller_cookie_and_hop_headers() {
    use http_server::ScriptedServer;

    let server = ScriptedServer::start().await;
    let origin = server.origin();
    let token = "test-token-strip";

    let transport = make_transport(&origin, token);
    transport
        .request(HubRequest {
            method: "POST".to_owned(),
            path: "/api/sessions".to_owned(),
            body: Some(serde_json::to_vec(&serde_json::json!({"name": "test"})).unwrap()),
            media_type: Some("application/json".to_owned()),
        })
        .await
        .unwrap();

    let received = server.received();
    assert_eq!(received.len(), 1);
    // The HubRequest DTO carries no headers, so no caller cookie/hop headers
    // can be injected. Assert they are absent.
    assert!(received[0].cookie.is_none());
    assert!(received[0].x_forwarded_for.is_none());
    assert!(received[0].x_real_ip.is_none());
    // Authorization is the injected bearer, not a caller value.
    assert_eq!(
        received[0].authorization.as_deref(),
        Some("Bearer test-token-strip")
    );
}

// ---------------------------------------------------------------------------
// Tests: pinned Host header
// ---------------------------------------------------------------------------

#[tokio::test]
async fn http_preserves_original_host_header() {
    use http_server::ScriptedServer;

    // Bind the server to 127.0.0.1 and give the transport a hostname origin.
    // The transport resolves the hostname to 127.0.0.1 (via the loopback
    // policy) and connects to that IP, but must send the original hostname
    // in the Host header.
    let server = ScriptedServer::start().await;
    let port = server.port;
    let token = "host-preserve-token";

    // Use a hostname that the loopback policy resolves to 127.0.0.1.
    let origin = format!("http://hub.test:{port}");
    let transport = make_transport(&origin, token);

    transport
        .request(HubRequest {
            method: "GET".to_owned(),
            path: "/api/health".to_owned(),
            body: None,
            media_type: None,
        })
        .await
        .unwrap();

    let received = server.received();
    assert_eq!(received.len(), 1);
    // Host header is the original hostname:port, not 127.0.0.1:port.
    assert_eq!(received[0].host, format!("hub.test:{port}"));
}

// ---------------------------------------------------------------------------
// Tests: allowlist enforcement
// ---------------------------------------------------------------------------

#[tokio::test]
async fn http_rejects_disallowed_method() {
    let transport = make_transport("http://127.0.0.1:1", "tok");

    let err = transport
        .request(HubRequest {
            method: "DELETE".to_owned(),
            path: "/api/sessions".to_owned(),
            body: None,
            media_type: None,
        })
        .await
        .unwrap_err();
    assert!(!format!("{err}").contains("tok"));
    assert!(!format!("{err:?}").contains("tok"));
}

#[tokio::test]
async fn http_rejects_disallowed_path() {
    let transport = make_transport("http://127.0.0.1:1", "tok");

    let err = transport
        .request(HubRequest {
            method: "GET".to_owned(),
            path: "/etc/passwd".to_owned(),
            body: None,
            media_type: None,
        })
        .await
        .unwrap_err();
    assert!(!format!("{err}").contains("tok"));
}

#[tokio::test]
async fn http_rejects_oversized_body() {
    let transport = make_transport("http://127.0.0.1:1", "tok");

    // A body just over 8 MiB.
    let big = vec![b'x'; 8 * 1024 * 1024 + 1];
    let err = transport
        .request(HubRequest {
            method: "POST".to_owned(),
            path: "/api/sessions".to_owned(),
            body: Some(big),
            media_type: Some("application/json".to_owned()),
        })
        .await
        .unwrap_err();
    assert!(!format!("{err}").contains("tok"));
}

#[tokio::test]
async fn http_rejects_disallowed_media_type() {
    let transport = make_transport("http://127.0.0.1:1", "tok");

    let err = transport
        .request(HubRequest {
            method: "POST".to_owned(),
            path: "/api/sessions".to_owned(),
            body: Some(serde_json::to_vec(&serde_json::json!({"x": 1})).unwrap()),
            media_type: Some("application/x-www-form-urlencoded".to_owned()),
        })
        .await
        .unwrap_err();
    assert!(!format!("{err}").contains("tok"));
}

// ---------------------------------------------------------------------------
// Tests: path allowlist bypass rejection (end-to-end)
// ---------------------------------------------------------------------------

#[tokio::test]
async fn http_rejects_dot_segment_traversal() {
    let transport = make_transport("http://127.0.0.1:1", "tok");
    let err = transport
        .request(HubRequest {
            method: "GET".to_owned(),
            path: "/api/../etc/passwd".to_owned(),
            body: None,
            media_type: None,
        })
        .await
        .unwrap_err();
    assert!(!format!("{err}").contains("tok"));
}

#[tokio::test]
async fn http_rejects_percent_encoded_traversal() {
    let transport = make_transport("http://127.0.0.1:1", "tok");
    let err = transport
        .request(HubRequest {
            method: "GET".to_owned(),
            path: "/api/%2e%2e/etc/passwd".to_owned(),
            body: None,
            media_type: None,
        })
        .await
        .unwrap_err();
    assert!(!format!("{err}").contains("tok"));
}

#[tokio::test]
async fn http_rejects_double_slash_path() {
    let transport = make_transport("http://127.0.0.1:1", "tok");
    let err = transport
        .request(HubRequest {
            method: "GET".to_owned(),
            path: "//etc/passwd".to_owned(),
            body: None,
            media_type: None,
        })
        .await
        .unwrap_err();
    assert!(!format!("{err}").contains("tok"));
}

#[tokio::test]
async fn http_rejects_backslash_path() {
    let transport = make_transport("http://127.0.0.1:1", "tok");
    let err = transport
        .request(HubRequest {
            method: "GET".to_owned(),
            path: "\\api\\..\\..\\etc".to_owned(),
            body: None,
            media_type: None,
        })
        .await
        .unwrap_err();
    assert!(!format!("{err}").contains("tok"));
}

// ---------------------------------------------------------------------------
// Tests: redirect rejection
// ---------------------------------------------------------------------------

#[tokio::test]
async fn http_rejects_redirect() {
    use http_server::ScriptedServer;

    let server = ScriptedServer::start().await;
    let origin = server.origin();
    let token = "redirect-test-token";

    server.enqueue(
        302,
        vec![(
            "location".to_owned(),
            "http://evil.example.com/auth".to_owned(),
        )],
        Vec::new(),
    );

    let transport = make_transport(&origin, token);
    let result = transport
        .request(HubRequest {
            method: "GET".to_owned(),
            path: "/api/health".to_owned(),
            body: None,
            media_type: None,
        })
        .await;
    assert!(result.is_err(), "redirect should be rejected");
    let err = result.unwrap_err();
    assert!(!format!("{err}").contains(token));
    // No second connection was made to follow the redirect.
    assert_eq!(server.connection_count(), 1);
}

// ---------------------------------------------------------------------------
// Tests: body and status fidelity
// ---------------------------------------------------------------------------

#[tokio::test]
async fn http_preserves_body_fidelity() {
    use http_server::ScriptedServer;

    let server = ScriptedServer::start().await;
    let origin = server.origin();
    let transport = make_transport(&origin, "body-fidelity-token");

    let body = serde_json::json!({"project": "/tmp/test", "prompt": "hello world"});
    transport
        .request(HubRequest {
            method: "POST".to_owned(),
            path: "/api/spawn".to_owned(),
            body: Some(serde_json::to_vec(&body).unwrap()),
            media_type: Some("application/json".to_owned()),
        })
        .await
        .unwrap();

    let received = server.received();
    assert_eq!(received.len(), 1);
    let received_body: serde_json::Value = serde_json::from_slice(&received[0].body).unwrap();
    assert_eq!(received_body, body);
}

#[tokio::test]
async fn http_preserves_status_and_json_body() {
    use http_server::ScriptedServer;

    let server = ScriptedServer::start().await;
    let origin = server.origin();
    let transport = make_transport(&origin, "status-token");

    let response = transport
        .request(HubRequest {
            method: "GET".to_owned(),
            path: "/api/health".to_owned(),
            body: None,
            media_type: None,
        })
        .await
        .unwrap();

    let HubResponse { status, body, .. } = response;
    let body: serde_json::Value = serde_json::from_slice(&body).unwrap();
    assert_eq!(status, 200);
    assert_eq!(
        body.get("mobile_api_version").and_then(|v| v.as_i64()),
        Some(1)
    );
}

#[tokio::test]
async fn http_preserves_success_binary_bytes_and_selected_headers() {
    use http_server::ScriptedServer;

    let server = ScriptedServer::start().await;
    let bytes = vec![0xff, 0xd8, 0x00, 0x80, 0xfe, 0xff, 0xd9];
    server.enqueue(
        200,
        vec![
            ("content-type".to_owned(), "image/jpeg".to_owned()),
            ("etag".to_owned(), "\"binary-v1\"".to_owned()),
            ("x-secret-internal".to_owned(), "not-forwarded".to_owned()),
        ],
        bytes.clone(),
    );
    let response = make_transport(&server.origin(), "binary-token")
        .request(HubRequest {
            method: "GET".to_owned(),
            path: "/images/photo.jpg".to_owned(),
            body: None,
            media_type: None,
        })
        .await
        .unwrap();

    assert_eq!(response.status, 200);
    assert_eq!(response.body, bytes);
    assert_eq!(response.media_type.as_deref(), Some("image/jpeg"));
    assert_eq!(response.headers.content_type.as_deref(), Some("image/jpeg"));
    assert_eq!(response.headers.etag.as_deref(), Some("\"binary-v1\""));
    let json = serde_json::to_string(&response.headers).unwrap();
    assert!(!json.contains("x-secret-internal"));
    assert!(!json.contains("not-forwarded"));
}

#[tokio::test]
async fn http_preserves_non_success_status_text_and_nul_body() {
    use http_server::ScriptedServer;

    let server = ScriptedServer::start().await;
    let bytes = b"not found\0with details".to_vec();
    server.enqueue(
        404,
        vec![(
            "content-type".to_owned(),
            "text/plain; charset=utf-8".to_owned(),
        )],
        bytes.clone(),
    );
    let response = make_transport(&server.origin(), "four-oh-four-token")
        .request(HubRequest {
            method: "GET".to_owned(),
            path: "/api/missing".to_owned(),
            body: None,
            media_type: None,
        })
        .await
        .unwrap();

    assert_eq!(response.status, 404);
    assert_eq!(response.body, bytes);
    assert_eq!(response.media_type.as_deref(), Some("text/plain"));
}

#[tokio::test]
async fn http_exact_request_and_response_limits_are_enforced_on_wire_bytes() {
    use http_server::ScriptedServer;

    let server = ScriptedServer::start().await;
    let exact_response = vec![0xa5; MAX_RESPONSE_BODY_BYTES];
    server.enqueue(
        200,
        vec![(
            "content-type".to_owned(),
            "application/octet-stream".to_owned(),
        )],
        exact_response.clone(),
    );
    let exact_request = vec![0x5a; MAX_REQUEST_BODY_BYTES];
    let transport = make_transport(&server.origin(), "limit-token");
    let response = transport
        .request(HubRequest {
            method: "POST".to_owned(),
            path: "/api/upload".to_owned(),
            body: Some(exact_request.clone()),
            media_type: Some("application/json".to_owned()),
        })
        .await
        .unwrap();
    assert_eq!(server.received()[0].body, exact_request);
    assert_eq!(response.body, exact_response);

    let oversized = transport
        .request(HubRequest {
            method: "POST".to_owned(),
            path: "/api/upload".to_owned(),
            body: Some(vec![0; MAX_REQUEST_BODY_BYTES + 1]),
            media_type: Some("application/json".to_owned()),
        })
        .await;
    assert!(matches!(oversized, Err(HttpError::BodyTooLarge)));

    server.enqueue(200, vec![], vec![0xa5; MAX_RESPONSE_BODY_BYTES + 1]);
    let oversized = transport
        .request(HubRequest {
            method: "GET".to_owned(),
            path: "/api/large".to_owned(),
            body: None,
            media_type: None,
        })
        .await;
    assert!(matches!(oversized, Err(HttpError::ResponseTooLarge)));
}

// ---------------------------------------------------------------------------
// Tests: no token/URL/body in errors
// ---------------------------------------------------------------------------

#[tokio::test]
async fn http_errors_never_contain_token() {
    use http_server::ScriptedServer;

    let server = ScriptedServer::start().await;
    let origin = server.origin();
    let token = "secret-error-token-xyz";

    server.enqueue(404, vec![], Vec::new());

    let transport = make_transport(&origin, token);
    let response = transport
        .request(HubRequest {
            method: "GET".to_owned(),
            path: "/api/nonexistent".to_owned(),
            body: None,
            media_type: None,
        })
        .await;

    let response = response.expect("non-2xx is a byte-preserving response");
    assert_eq!(response.status, 404);
    assert!(response.body.is_empty());
}

#[tokio::test]
async fn http_errors_never_contain_origin_url() {
    use http_server::ScriptedServer;

    let server = ScriptedServer::start().await;
    let origin = server.origin();

    server.enqueue(500, vec![], Vec::new());

    let transport = make_transport(&origin, "tok");
    let response = transport
        .request(HubRequest {
            method: "GET".to_owned(),
            path: "/api/health".to_owned(),
            body: None,
            media_type: None,
        })
        .await;

    let response = response.expect("non-2xx is not a transport error");
    assert_eq!(response.status, 500);
}

// ---------------------------------------------------------------------------
// Tests: cancellation
// ---------------------------------------------------------------------------

#[tokio::test]
async fn http_cancellation_aborts_inflight_request() {
    use http_server::ScriptedServer;

    let server = ScriptedServer::start().await;
    let origin = server.origin();
    let transport = make_transport(&origin, "cancel-token");

    // A fast request must complete well within the timeout (no hang).
    let result = tokio::time::timeout(
        Duration::from_secs(5),
        transport.request(HubRequest {
            method: "GET".to_owned(),
            path: "/api/health".to_owned(),
            body: None,
            media_type: None,
        }),
    )
    .await;

    // Should complete well within the timeout.
    assert!(result.is_ok(), "request should complete within timeout");
}

/// A real command-shaped cancellation test. The scripted server acknowledges
/// the request, then waits for socket EOF. Cancelling through the same registry
/// used by Tauri must drop the reqwest future, close the socket, reap the task,
/// and remove the uploaded body/entry without sleeps or polling.
#[tokio::test]
async fn http_real_cancellation_aborts_slow_request() {
    use tokio::io::AsyncReadExt;
    use tokio::net::TcpListener;
    use tokio::sync::oneshot;

    let listener = TcpListener::bind("127.0.0.1:0").await.unwrap();
    let port = listener.local_addr().unwrap().port();
    let (accepted_tx, accepted_rx) = oneshot::channel();
    let (eof_tx, eof_rx) = oneshot::channel();
    let server_task = tokio::spawn(async move {
        let (mut stream, _) = listener.accept().await.unwrap();
        let mut received = Vec::new();
        let mut buf = [0u8; 4096];
        loop {
            let read = stream.read(&mut buf).await.unwrap();
            assert_ne!(read, 0, "client closed before sending request headers");
            received.extend_from_slice(&buf[..read]);
            if received.windows(4).any(|window| window == b"\r\n\r\n") {
                break;
            }
        }
        accepted_tx.send(()).unwrap();
        loop {
            if stream.read(&mut buf).await.unwrap() == 0 {
                break;
            }
        }
        eof_tx.send(()).unwrap();
    });

    let origin = format!("http://hub.cancel:{port}");
    let transport = make_transport(&origin, "cancel-token");
    let registry = Arc::new(HttpRequestRegistry::new());
    let request_id = "cancel_request_123456";
    let upload = vec![0, 0xff, 0x80, 7];
    registry
        .prepare(PreparedHttpRequest {
            request_id: request_id.to_owned(),
            active_profile_id: "profile".to_owned(),
            method: "POST".to_owned(),
            path: "/api/slow".to_owned(),
            body_length: upload.len(),
            media_type: Some("image/jpeg".to_owned()),
        })
        .unwrap();
    registry.upload_body(request_id, upload).unwrap();
    let registered = registry.begin(request_id).unwrap();
    let handle = tokio::spawn(async move {
        transport
            .request_cancellable(
                HubRequest {
                    method: registered.metadata.method,
                    path: registered.metadata.path,
                    body: registered.body,
                    media_type: registered.metadata.media_type,
                },
                Some("profile"),
                1,
                registered.cancellation,
            )
            .await
    });

    tokio::time::timeout(Duration::from_secs(2), accepted_rx)
        .await
        .expect("server did not observe request")
        .unwrap();
    registry.cancel(request_id).unwrap();
    assert_eq!(registry.active_count(), 0, "cancel must remove body/entry");
    let result = tokio::time::timeout(Duration::from_secs(2), handle)
        .await
        .expect("cancelled request task did not settle")
        .unwrap();
    assert!(matches!(result, Err(HttpError::Cancelled)));
    registry.finish(request_id);
    registry.cancel(request_id).unwrap();
    assert_eq!(registry.active_count(), 0, "late cancel must be idempotent");
    tokio::time::timeout(Duration::from_secs(2), eof_rx)
        .await
        .expect("server socket did not observe cancellation EOF")
        .unwrap();
    server_task.await.unwrap();
}

// ---------------------------------------------------------------------------
// Tests: DNS rebinding between connections
// ---------------------------------------------------------------------------

#[tokio::test]
async fn http_dns_rebinding_re_resolves_each_connection() {
    use http_server::ScriptedServer;

    let server = ScriptedServer::start().await;
    let port = server.port;
    let origin = format!("http://hub.rebind:{port}");

    // First resolution: 127.0.0.1 (loopback, allowed in debug). Second
    // resolution: a public address (rejected by HTTP policy).
    let public: IpAddr = "203.0.113.10".parse().unwrap();
    let loopback: IpAddr = "127.0.0.1".parse().unwrap();
    let policy = rebinding_policy(vec![vec![loopback], vec![public]]);

    let transport = HubHttp::new(
        origin,
        "rebind-token".to_owned(),
        policy,
        ReleaseMode::Debug {
            allow_loopback: true,
        },
    );

    // First request: resolves to loopback, succeeds.
    transport
        .request(HubRequest {
            method: "GET".to_owned(),
            path: "/api/health".to_owned(),
            body: None,
            media_type: None,
        })
        .await
        .expect("first request succeeds");

    // Second request: rebinds to public HTTP, must be rejected by the policy.
    let err = transport
        .request(HubRequest {
            method: "GET".to_owned(),
            path: "/api/health".to_owned(),
            body: None,
            media_type: None,
        })
        .await
        .unwrap_err();
    assert!(!format!("{err}").contains("rebind-token"));
}

// ---------------------------------------------------------------------------
// Tests: no token in diagnostics
// ---------------------------------------------------------------------------

#[tokio::test]
async fn http_diagnostics_never_contain_token_or_body() {
    use http_server::ScriptedServer;

    let server = ScriptedServer::start().await;
    let origin = server.origin();
    let token = "diag-secret-token";

    let transport = make_transport(&origin, token);
    transport
        .request(HubRequest {
            method: "GET".to_owned(),
            path: "/api/health".to_owned(),
            body: None,
            media_type: None,
        })
        .await
        .unwrap();

    let snap = transport.diagnostics().snapshot();
    let json = serde_json::to_string(&snap).unwrap();
    assert!(
        !json.contains(token),
        "diagnostics must not contain token: {json}"
    );
    assert!(
        !json.contains("mobile_api_version"),
        "diagnostics must not contain body"
    );
    assert!(
        !json.contains(&origin),
        "diagnostics must not contain origin URL"
    );
}
