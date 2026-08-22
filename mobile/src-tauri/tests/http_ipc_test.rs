use std::collections::HashMap;
use std::net::{IpAddr, Ipv4Addr};
use std::sync::{Arc, Mutex};

use app_lib::appwire_transport::AppwireManager;
use app_lib::commands::HubHttpResponseMetadata;
use app_lib::diagnostics::Diagnostics;
use app_lib::error::{ProfileError, ReleaseMode};
use app_lib::http_transport::{MAX_REQUEST_BODY_BYTES, MAX_RESPONSE_BODY_BYTES, REQUEST_ID_HEADER};
use app_lib::network_policy::{DnsResolver, NetworkPolicy};
use app_lib::profile::{
    Clock, PairingProbe, Preferences, PreferencesStore, ProfileStore, ProfileSummary, SecureStore,
    PREFERENCES_VERSION,
};
use app_lib::profile_runtime::ProfileRuntime;
use app_lib::transport_state::TransportState;
use http_body_util::{BodyExt as _, Full};
use hyper::body::Bytes;
use hyper::server::conn::http1;
use hyper::service::service_fn;
use hyper::{Request, Response, StatusCode};
use hyper_util::rt::TokioIo;
use tauri::ipc::{CallbackFn, InvokeBody, InvokeResponseBody};
use tauri::test::{get_ipc_response, mock_builder, mock_context, noop_assets, MockRuntime};
use tauri::webview::InvokeRequest;
use tauri::{Manager as _, WebviewWindow, WebviewWindowBuilder};
use tokio::net::TcpListener;

const PROFILE_ID: &str = "11111111-1111-4111-8111-111111111111";
const TOKEN: &str = "native-only-test-capability";

#[derive(Clone)]
struct MemoryPreferences(Arc<Mutex<Preferences>>);

impl PreferencesStore for MemoryPreferences {
    fn load(&self) -> Result<Preferences, ProfileError> {
        Ok(self.0.lock().unwrap().clone())
    }

    fn save(&self, preferences: &Preferences) -> Result<(), ProfileError> {
        *self.0.lock().unwrap() = preferences.clone();
        Ok(())
    }
}

#[derive(Clone)]
struct MemorySecure(Arc<Mutex<HashMap<String, String>>>);

impl SecureStore for MemorySecure {
    fn get(&self, profile_id: &str) -> Result<Option<String>, ProfileError> {
        Ok(self.0.lock().unwrap().get(profile_id).cloned())
    }

    fn set(&self, profile_id: &str, token: &str) -> Result<(), ProfileError> {
        self.0
            .lock()
            .unwrap()
            .insert(profile_id.to_owned(), token.to_owned());
        Ok(())
    }

    fn delete(&self, profile_id: &str) -> Result<(), ProfileError> {
        self.0.lock().unwrap().remove(profile_id);
        Ok(())
    }
}

struct NoopProbe;
impl PairingProbe for NoopProbe {
    fn probe(&self, _origin: &str, _token: &str, _mode: ReleaseMode) -> Result<i64, ProfileError> {
        Ok(1)
    }
}

struct FixedClock;
impl Clock for FixedClock {
    fn now_secs(&self) -> u64 {
        1
    }
}

struct LoopbackResolver;
impl DnsResolver for LoopbackResolver {
    fn resolve(&self, _host: &str) -> Result<Vec<IpAddr>, String> {
        Ok(vec![IpAddr::V4(Ipv4Addr::LOCALHOST)])
    }
}

struct ScriptedHttpServer {
    origin: String,
    received: tokio::sync::mpsc::UnboundedReceiver<(String, Vec<u8>)>,
    task: tokio::task::JoinHandle<()>,
}

impl ScriptedHttpServer {
    async fn start() -> Self {
        let listener = TcpListener::bind("127.0.0.1:0").await.unwrap();
        let port = listener.local_addr().unwrap().port();
        let (received_tx, received) = tokio::sync::mpsc::unbounded_channel();
        let task = tokio::spawn(async move {
            loop {
                let Ok((stream, _)) = listener.accept().await else {
                    return;
                };
                let received_tx = received_tx.clone();
                tokio::spawn(async move {
                    let service = service_fn(move |request: Request<hyper::body::Incoming>| {
                        let received_tx = received_tx.clone();
                        async move {
                            let path = request.uri().path().to_owned();
                            let body = request
                                .into_body()
                                .collect()
                                .await
                                .unwrap()
                                .to_bytes()
                                .to_vec();
                            received_tx.send((path.clone(), body)).unwrap();
                            let (status, content_type, response_body) = match path.as_str() {
                                "/images/binary" => (
                                    StatusCode::OK,
                                    "image/jpeg",
                                    vec![0xff, 0xd8, 0, 0x80, 0xfe, 0xd9],
                                ),
                                "/api/missing" => (
                                    StatusCode::NOT_FOUND,
                                    "text/plain; charset=utf-8",
                                    b"missing\0body".to_vec(),
                                ),
                                "/api/exact" => (
                                    StatusCode::OK,
                                    "application/octet-stream",
                                    vec![0xa5; MAX_RESPONSE_BODY_BYTES],
                                ),
                                "/api/too-large" => (
                                    StatusCode::OK,
                                    "application/octet-stream",
                                    vec![0xa5; MAX_RESPONSE_BODY_BYTES + 1],
                                ),
                                _ => (StatusCode::OK, "application/json", b"{}".to_vec()),
                            };
                            Ok::<_, std::convert::Infallible>(
                                Response::builder()
                                    .status(status)
                                    .header("content-type", content_type)
                                    .header("etag", "opaque-ipc-etag")
                                    .body(Full::new(Bytes::from(response_body)))
                                    .unwrap(),
                            )
                        }
                    });
                    let _ = http1::Builder::new()
                        .serve_connection(TokioIo::new(stream), service)
                        .await;
                });
            }
        });
        Self {
            origin: format!("http://hub.test:{port}"),
            received,
            task,
        }
    }
}

impl Drop for ScriptedHttpServer {
    fn drop(&mut self) {
        self.task.abort();
    }
}

fn build_app(
    origin: String,
) -> (
    tauri::App<MockRuntime>,
    std::sync::mpsc::Receiver<(u32, InvokeResponseBody)>,
) {
    let preferences: Arc<dyn PreferencesStore> =
        Arc::new(MemoryPreferences(Arc::new(Mutex::new(Preferences {
            version: PREFERENCES_VERSION,
            profiles: vec![ProfileSummary {
                id: PROFILE_ID.to_owned(),
                name: "Test Hub".to_owned(),
                origin,
            }],
            active_id: Some(PROFILE_ID.to_owned()),
        }))));
    let secure_impl = Arc::new(MemorySecure(Arc::new(Mutex::new(HashMap::from([(
        PROFILE_ID.to_owned(),
        TOKEN.to_owned(),
    )])))));
    let secure: Arc<dyn SecureStore> = secure_impl;
    let policy = Arc::new(NetworkPolicy::new(Box::new(LoopbackResolver)));
    let mode = ReleaseMode::Debug {
        allow_loopback: true,
    };
    let appwire = Arc::new(AppwireManager::new(policy.clone(), mode));
    let store = Arc::new(ProfileStore::new(
        preferences,
        secure.clone(),
        Arc::new(NoopProbe),
        Arc::new(FixedClock),
        policy.clone(),
    ));
    let runtime = ProfileRuntime::new(store);
    let transport =
        TransportState::new(policy, mode, Arc::new(Diagnostics::new()), appwire, secure);
    let (channel_tx, channel_rx) = std::sync::mpsc::channel();
    let app = mock_builder()
        .channel_interceptor(move |_webview, callback, _index, body| {
            channel_tx.send((callback.0, body.clone())).unwrap();
            true
        })
        .manage(runtime)
        .manage(transport)
        .invoke_handler(app_lib::command_handler())
        .build(mock_context(noop_assets()))
        .unwrap();
    (app, channel_rx)
}

fn webview(app: &tauri::App<MockRuntime>) -> WebviewWindow<MockRuntime> {
    WebviewWindowBuilder::new(app, "main", Default::default())
        .build()
        .unwrap()
}

fn invoke(
    webview: &WebviewWindow<MockRuntime>,
    command: &str,
    body: InvokeBody,
    headers: tauri::http::HeaderMap,
) -> Result<InvokeResponseBody, serde_json::Value> {
    get_ipc_response(
        webview,
        InvokeRequest {
            cmd: command.to_owned(),
            callback: CallbackFn(100),
            error: CallbackFn(101),
            url: "tauri://localhost".parse().unwrap(),
            body,
            headers,
            invoke_key: tauri::test::INVOKE_KEY.to_owned(),
        },
    )
}

fn prepare_body(request_id: &str, path: &str, body_length: usize) -> InvokeBody {
    let media_type = if body_length == 0 {
        serde_json::Value::Null
    } else {
        serde_json::Value::String("image/jpeg".to_owned())
    };
    InvokeBody::Json(serde_json::json!({
        "request": {
            "requestId": request_id,
            "activeProfileId": PROFILE_ID,
            "method": if body_length == 0 { "GET" } else { "POST" },
            "path": path,
            "bodyLength": body_length,
            "mediaType": media_type,
        }
    }))
}

#[tokio::test(flavor = "multi_thread", worker_threads = 4)]
async fn actual_tauri_ipc_dispatch_preserves_raw_bytes_metadata_limits_and_cleanup() {
    let mut server = ScriptedHttpServer::start().await;
    let (app, channel_rx) = build_app(server.origin.clone());
    let webview = webview(&app);

    let request_id = "ipc_binary_request_123";
    let request_bytes = vec![0xff, 0xd8, 0, 0x80, 0xfe, 0xd9];
    invoke(
        &webview,
        "hub_http_prepare",
        prepare_body(request_id, "/images/binary", request_bytes.len()),
        Default::default(),
    )
    .unwrap();
    let mut upload_headers = tauri::http::HeaderMap::new();
    upload_headers.insert(REQUEST_ID_HEADER, request_id.parse().unwrap());
    invoke(
        &webview,
        "hub_http_upload_body",
        InvokeBody::Raw(request_bytes.clone()),
        upload_headers,
    )
    .unwrap();
    let raw = invoke(
        &webview,
        "hub_http_request",
        InvokeBody::Json(serde_json::json!({
            "request": { "requestId": request_id },
            "onResponse": "__CHANNEL__:42",
        })),
        Default::default(),
    )
    .unwrap();
    assert!(
        matches!(raw, InvokeResponseBody::Raw(bytes) if bytes == vec![0xff, 0xd8, 0, 0x80, 0xfe, 0xd9])
    );
    let (channel_id, metadata) = channel_rx.recv().unwrap();
    assert_eq!(channel_id, 42);
    let metadata = metadata.deserialize::<HubHttpResponseMetadata>().unwrap();
    assert_eq!(metadata.status, 200);
    assert_eq!(metadata.headers.content_type.as_deref(), Some("image/jpeg"));
    assert_eq!(metadata.headers.etag.as_deref(), Some("opaque-ipc-etag"));
    assert_eq!(metadata.body_length, 6);
    let (path, uploaded) = server.received.recv().await.unwrap();
    assert_eq!(path, "/images/binary");
    assert_eq!(uploaded, request_bytes);
    assert_eq!(
        app.state::<TransportState>().http_requests().active_count(),
        0
    );

    let missing_id = "ipc_missing_request_123";
    invoke(
        &webview,
        "hub_http_prepare",
        prepare_body(missing_id, "/api/missing", 0),
        Default::default(),
    )
    .unwrap();
    let missing = invoke(
        &webview,
        "hub_http_request",
        InvokeBody::Json(serde_json::json!({
            "request": { "requestId": missing_id },
            "onResponse": "__CHANNEL__:43",
        })),
        Default::default(),
    )
    .unwrap();
    assert!(matches!(missing, InvokeResponseBody::Raw(bytes) if bytes == b"missing\0body"));
    let (_, metadata) = channel_rx.recv().unwrap();
    assert_eq!(
        metadata
            .deserialize::<HubHttpResponseMetadata>()
            .unwrap()
            .status,
        404
    );
    assert_eq!(
        app.state::<TransportState>().http_requests().active_count(),
        0
    );

    let exact_request_id = "ipc_exact_request_1234";
    let exact_request = vec![0x5a; MAX_REQUEST_BODY_BYTES];
    invoke(
        &webview,
        "hub_http_prepare",
        prepare_body(exact_request_id, "/api/exact", exact_request.len()),
        Default::default(),
    )
    .unwrap();
    let mut exact_headers = tauri::http::HeaderMap::new();
    exact_headers.insert(REQUEST_ID_HEADER, exact_request_id.parse().unwrap());
    invoke(
        &webview,
        "hub_http_upload_body",
        InvokeBody::Raw(exact_request),
        exact_headers,
    )
    .unwrap();
    let exact = invoke(
        &webview,
        "hub_http_request",
        InvokeBody::Json(serde_json::json!({
            "request": { "requestId": exact_request_id },
            "onResponse": "__CHANNEL__:44",
        })),
        Default::default(),
    )
    .unwrap();
    assert!(
        matches!(exact, InvokeResponseBody::Raw(bytes) if bytes.len() == MAX_RESPONSE_BODY_BYTES)
    );
    let (_, exact_metadata) = channel_rx.recv().unwrap();
    assert_eq!(
        exact_metadata
            .deserialize::<HubHttpResponseMetadata>()
            .unwrap()
            .body_length,
        MAX_RESPONSE_BODY_BYTES
    );
    assert_eq!(
        app.state::<TransportState>().http_requests().active_count(),
        0
    );

    let oversized_request_id = "ipc_oversized_req_123";
    assert!(invoke(
        &webview,
        "hub_http_prepare",
        prepare_body(
            oversized_request_id,
            "/api/exact",
            MAX_REQUEST_BODY_BYTES + 1,
        ),
        Default::default(),
    )
    .is_err());
    assert_eq!(
        app.state::<TransportState>().http_requests().active_count(),
        0
    );

    let failed_upload_id = "ipc_failed_upload_123";
    invoke(
        &webview,
        "hub_http_prepare",
        prepare_body(failed_upload_id, "/images/binary", 4),
        Default::default(),
    )
    .unwrap();
    let mut failed_upload_headers = tauri::http::HeaderMap::new();
    failed_upload_headers.insert(REQUEST_ID_HEADER, failed_upload_id.parse().unwrap());
    assert!(invoke(
        &webview,
        "hub_http_upload_body",
        InvokeBody::Raw(vec![0, 1, 2]),
        failed_upload_headers,
    )
    .is_err());
    invoke(
        &webview,
        "hub_http_cancel",
        InvokeBody::Json(serde_json::json!({
            "request": { "requestId": failed_upload_id },
        })),
        Default::default(),
    )
    .unwrap();
    assert_eq!(
        app.state::<TransportState>().http_requests().active_count(),
        0
    );

    let oversized_response_id = "ipc_oversized_resp_12";
    invoke(
        &webview,
        "hub_http_prepare",
        prepare_body(oversized_response_id, "/api/too-large", 0),
        Default::default(),
    )
    .unwrap();
    assert!(invoke(
        &webview,
        "hub_http_request",
        InvokeBody::Json(serde_json::json!({
            "request": { "requestId": oversized_response_id },
            "onResponse": "__CHANNEL__:45",
        })),
        Default::default(),
    )
    .is_err());
    assert_eq!(
        app.state::<TransportState>().http_requests().active_count(),
        0
    );
}
