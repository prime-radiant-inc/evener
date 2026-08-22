//! Test-only stdio bridge used by the TypeScript imported-AppwireClient test.
//! It dispatches through Tauri's real mock IPC runtime, the production command
//! handler/state types, and the production AppwireManager. The only scripted
//! boundary is the local WebSocket Hub.

use std::collections::{HashMap, VecDeque};
use std::io::{BufRead as _, BufWriter, Write as _};
use std::net::{IpAddr, Ipv4Addr};
use std::sync::{Arc, Mutex};

use app_lib::appwire_transport::AppwireManager;
use app_lib::diagnostics::Diagnostics;
use app_lib::error::{ProfileError, ReleaseMode};
use app_lib::network_policy::{DnsResolver, NetworkPolicy};
use app_lib::profile::{
    Clock, PairingProbe, Preferences, PreferencesStore, ProfileStore, ProfileSummary, SecureStore,
    PREFERENCES_VERSION,
};
use app_lib::profile_runtime::ProfileRuntime;
use app_lib::transport_state::TransportState;
use futures_util::{SinkExt as _, StreamExt as _};
use serde_json::{json, Value};
use tauri::ipc::{CallbackFn, InvokeBody, InvokeResponseBody};
use tauri::test::{get_ipc_response, mock_builder, mock_context, noop_assets, MockRuntime};
use tauri::webview::InvokeRequest;
use tauri::WebviewWindowBuilder;
use tokio::net::{TcpListener, TcpStream};
use tokio::sync::mpsc;
use tokio_tungstenite::tungstenite::handshake::server::{Request, Response};
use tokio_tungstenite::tungstenite::protocol::{CloseFrame, Message};
use tokio_tungstenite::WebSocketStream;

const PROFILE_ONE: &str = "11111111-1111-4111-8111-111111111111";
const PROFILE_TWO: &str = "22222222-2222-4222-8222-222222222222";
const TOKEN_ONE: &str = "harness-native-capability-one";
const TOKEN_TWO: &str = "harness-native-capability-two";

#[derive(Clone)]
struct Output(Arc<Mutex<BufWriter<std::io::Stdout>>>);

impl Output {
    fn send(&self, value: Value) {
        let mut writer = self.0.lock().unwrap();
        serde_json::to_writer(&mut *writer, &value).unwrap();
        writer.write_all(b"\n").unwrap();
        writer.flush().unwrap();
    }
}

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

#[derive(Default, serde::Serialize)]
#[serde(rename_all = "camelCase")]
struct ServerObservations {
    connection_count: usize,
    active_sockets: usize,
    max_active_sockets: usize,
    initialized_count: usize,
    client_infos: Vec<Value>,
    request_methods: Vec<String>,
}

enum ConnectionControl {
    ServerClose,
}

enum ServerControl {
    Shutdown,
}

type ActiveControl = Arc<Mutex<Option<(usize, mpsc::UnboundedSender<ConnectionControl>)>>>;

struct ScriptedHub {
    origin: String,
    observations: Arc<Mutex<ServerObservations>>,
    active_control: ActiveControl,
    shutdown: mpsc::UnboundedSender<ServerControl>,
    thread: Option<std::thread::JoinHandle<()>>,
}

impl ScriptedHub {
    fn start() -> Self {
        let observations = Arc::new(Mutex::new(ServerObservations::default()));
        let active_control = Arc::new(Mutex::new(None));
        let (shutdown, shutdown_rx) = mpsc::unbounded_channel();
        let (ready_tx, ready_rx) = std::sync::mpsc::sync_channel(1);
        let observations_thread = observations.clone();
        let active_thread = active_control.clone();
        let thread = std::thread::Builder::new()
            .name("mobile-appwire-scripted-hub".to_owned())
            .spawn(move || {
                let runtime = tokio::runtime::Builder::new_multi_thread()
                    .worker_threads(2)
                    .enable_all()
                    .build()
                    .unwrap();
                runtime.block_on(async move {
                    run_scripted_hub(observations_thread, active_thread, shutdown_rx, ready_tx)
                        .await;
                });
            })
            .unwrap();
        let port = ready_rx.recv().unwrap();
        Self {
            origin: format!("http://hub.test:{port}"),
            observations,
            active_control,
            shutdown,
            thread: Some(thread),
        }
    }

    fn server_close(&self) -> bool {
        self.active_control
            .lock()
            .unwrap()
            .as_ref()
            .is_some_and(|(_, control)| control.send(ConnectionControl::ServerClose).is_ok())
    }

    fn snapshot(&self) -> Value {
        serde_json::to_value(&*self.observations.lock().unwrap()).unwrap()
    }

    fn shutdown(&mut self) {
        let _ = self.shutdown.send(ServerControl::Shutdown);
        if let Some(thread) = self.thread.take() {
            thread.join().unwrap();
        }
    }
}

impl Drop for ScriptedHub {
    fn drop(&mut self) {
        self.shutdown();
    }
}

async fn run_scripted_hub(
    observations: Arc<Mutex<ServerObservations>>,
    active_control: ActiveControl,
    mut shutdown: mpsc::UnboundedReceiver<ServerControl>,
    ready: std::sync::mpsc::SyncSender<u16>,
) {
    let listener = TcpListener::bind("127.0.0.1:0").await.unwrap();
    ready.send(listener.local_addr().unwrap().port()).unwrap();
    loop {
        tokio::select! {
            command = shutdown.recv() => {
                if matches!(command, Some(ServerControl::Shutdown) | None) {
                    return;
                }
            }
            accepted = listener.accept() => {
                let (stream, _) = accepted.unwrap();
                let connection_id = {
                    let mut state = observations.lock().unwrap();
                    state.connection_count += 1;
                    state.active_sockets += 1;
                    state.max_active_sockets = state.max_active_sockets.max(state.active_sockets);
                    state.connection_count
                };
                let (control_tx, control_rx) = mpsc::unbounded_channel();
                *active_control.lock().unwrap() = Some((connection_id, control_tx));
                tokio::spawn(run_connection(
                    connection_id,
                    stream,
                    observations.clone(),
                    active_control.clone(),
                    control_rx,
                ));
            }
        }
    }
}

async fn run_connection(
    connection_id: usize,
    stream: TcpStream,
    observations: Arc<Mutex<ServerObservations>>,
    active_control: ActiveControl,
    mut control: mpsc::UnboundedReceiver<ConnectionControl>,
) {
    #[allow(clippy::result_large_err)]
    let accepted = tokio_tungstenite::accept_hdr_async(
        stream,
        |_request: &Request, mut response: Response| {
            response.headers_mut().insert(
                "Sec-WebSocket-Protocol",
                "evener-appwire-v3".parse().unwrap(),
            );
            Ok(response)
        },
    )
    .await;
    let Ok(mut socket) = accepted else {
        connection_finished(connection_id, &observations, &active_control);
        return;
    };

    loop {
        tokio::select! {
            command = control.recv() => {
                match command {
                    Some(ConnectionControl::ServerClose) => {
                        let _ = socket.send(Message::Close(Some(CloseFrame {
                            code: 1012.into(),
                            reason: "scripted restart".into(),
                        }))).await;
                        break;
                    }
                    None => break,
                }
            }
            message = socket.next() => {
                match message {
                    Some(Ok(Message::Text(text))) => {
                        if handle_client_frame(&mut socket, &text, &observations).await.is_err() {
                            break;
                        }
                    }
                    Some(Ok(Message::Close(_))) | None | Some(Err(_)) => break,
                    _ => {}
                }
            }
        }
    }
    connection_finished(connection_id, &observations, &active_control);
}

fn connection_finished(
    connection_id: usize,
    observations: &Arc<Mutex<ServerObservations>>,
    active_control: &ActiveControl,
) {
    let mut state = observations.lock().unwrap();
    state.active_sockets = state.active_sockets.saturating_sub(1);
    drop(state);
    let mut current = active_control.lock().unwrap();
    if current
        .as_ref()
        .is_some_and(|(current_id, _)| *current_id == connection_id)
    {
        *current = None;
    }
}

async fn handle_client_frame(
    socket: &mut WebSocketStream<TcpStream>,
    text: &str,
    observations: &Arc<Mutex<ServerObservations>>,
) -> Result<(), tokio_tungstenite::tungstenite::Error> {
    let frame: Value = serde_json::from_str(text).unwrap();
    let method = frame.get("method").and_then(Value::as_str).unwrap_or("");
    observations
        .lock()
        .unwrap()
        .request_methods
        .push(method.to_owned());
    if method == "initialize" {
        if let Some(client_info) = frame.pointer("/params/clientInfo") {
            observations
                .lock()
                .unwrap()
                .client_infos
                .push(client_info.clone());
        }
        socket
            .send(Message::Text(
                json!({
                    "id": frame["id"],
                    "result": { "protocolVersion": "evener-appwire-v3" },
                })
                .to_string()
                .into(),
            ))
            .await?;
    } else if method == "initialized" {
        observations.lock().unwrap().initialized_count += 1;
        socket
            .send(Message::Text(
                json!({ "method": "thread/closed", "params": {} })
                    .to_string()
                    .into(),
            ))
            .await?;
        socket
            .send(Message::Text(
                json!({ "method": "evener/tree/changed", "params": {} })
                    .to_string()
                    .into(),
            ))
            .await?;
    } else if let Some(id) = frame.get("id") {
        socket
            .send(Message::Text(
                json!({ "id": id, "result": {} }).to_string().into(),
            ))
            .await?;
    }
    Ok(())
}

fn build_app(
    origin: String,
    output: Output,
    held_events: Arc<Mutex<VecDeque<Value>>>,
) -> tauri::App<MockRuntime> {
    let preferences: Arc<dyn PreferencesStore> =
        Arc::new(MemoryPreferences(Arc::new(Mutex::new(Preferences {
            version: PREFERENCES_VERSION,
            profiles: vec![
                ProfileSummary {
                    id: PROFILE_ONE.to_owned(),
                    name: "One".to_owned(),
                    origin: origin.clone(),
                },
                ProfileSummary {
                    id: PROFILE_TWO.to_owned(),
                    name: "Two".to_owned(),
                    origin,
                },
            ],
            active_id: Some(PROFILE_ONE.to_owned()),
        }))));
    let secure: Arc<dyn SecureStore> =
        Arc::new(MemorySecure(Arc::new(Mutex::new(HashMap::from([
            (PROFILE_ONE.to_owned(), TOKEN_ONE.to_owned()),
            (PROFILE_TWO.to_owned(), TOKEN_TWO.to_owned()),
        ])))));
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

    mock_builder()
        .channel_interceptor(move |_webview, callback, _index, body| {
            let payload = match body {
                InvokeResponseBody::Json(json) => serde_json::from_str(json).unwrap(),
                InvokeResponseBody::Raw(bytes) => json!({ "bytes": bytes }),
            };
            let line = json!({
                "kind": "channel",
                "channelId": callback.0,
                "payload": payload,
            });
            let held = line
                .pointer("/payload/data")
                .and_then(Value::as_str)
                .is_some_and(|data| data.contains("thread/closed"));
            if held {
                held_events.lock().unwrap().push_back(line);
            } else {
                output.send(line);
            }
            true
        })
        .manage(runtime)
        .manage(transport)
        .invoke_handler(app_lib::command_handler())
        .build(mock_context(noop_assets()))
        .unwrap()
}

fn invoke(
    webview: &tauri::WebviewWindow<MockRuntime>,
    command: &str,
    args: Value,
) -> Result<Value, Value> {
    get_ipc_response(
        webview,
        InvokeRequest {
            cmd: command.to_owned(),
            callback: CallbackFn(100),
            error: CallbackFn(101),
            url: "tauri://localhost".parse().unwrap(),
            body: InvokeBody::Json(args),
            headers: Default::default(),
            invoke_key: tauri::test::INVOKE_KEY.to_owned(),
        },
    )
    .map(|body| body.deserialize::<Value>().unwrap())
}

fn main() {
    let output = Output(Arc::new(Mutex::new(BufWriter::new(std::io::stdout()))));
    let mut hub = ScriptedHub::start();
    let held_events = Arc::new(Mutex::new(VecDeque::new()));
    let app = build_app(hub.origin.clone(), output.clone(), held_events.clone());
    let webview = WebviewWindowBuilder::new(&app, "main", Default::default())
        .build()
        .unwrap();
    output.send(json!({
        "kind": "ready",
        "profiles": [PROFILE_ONE, PROFILE_TWO],
    }));

    for line in std::io::stdin().lock().lines() {
        let line = line.unwrap();
        let request: Value = serde_json::from_str(&line).unwrap();
        let id = request["id"].as_u64().unwrap();
        match request["kind"].as_str().unwrap_or("") {
            "invoke" => {
                let command = request["cmd"].as_str().unwrap();
                let args = request.get("args").cloned().unwrap_or_else(|| json!({}));
                match invoke(&webview, command, args) {
                    Ok(value) => output.send(json!({
                        "kind": "response", "id": id, "ok": true, "value": value,
                    })),
                    Err(error) => output.send(json!({
                        "kind": "response", "id": id, "ok": false, "error": error,
                    })),
                }
            }
            "control" => match request["action"].as_str().unwrap_or("") {
                "serverClose" => output.send(json!({
                    "kind": "response", "id": id, "ok": hub.server_close(), "value": null,
                })),
                "releaseHeld" => {
                    if let Some(event) = held_events.lock().unwrap().pop_front() {
                        output.send(event);
                    }
                    output.send(json!({
                        "kind": "response", "id": id, "ok": true, "value": null,
                    }));
                }
                "snapshot" => output.send(json!({
                    "kind": "response", "id": id, "ok": true, "value": hub.snapshot(),
                })),
                "shutdown" => {
                    hub.shutdown();
                    output.send(json!({
                        "kind": "response", "id": id, "ok": true, "value": null,
                    }));
                    break;
                }
                _ => output.send(json!({
                    "kind": "response", "id": id, "ok": false, "error": "unknown control",
                })),
            },
            _ => output.send(json!({
                "kind": "response", "id": id, "ok": false, "error": "unknown request",
            })),
        }
    }
}
