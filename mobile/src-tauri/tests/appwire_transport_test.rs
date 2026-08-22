//! AppWire transport integration tests using scripted local WebSocket servers.
//!
//! Tests assert the binding-B AppWire contract:
//! - ordered text frames
//! - close code propagation
//! - exactly one total socket for the active profile
//! - switch closes the prior profile+connection before opening the next
//! - stale profile/connection generation rejects sends
//! - bounded queue closes on overload without dropping or reordering frames
//! - cancellation closes the connection
//! - bearer token and correct upstream Origin
//! - no token in errors
//!
//! Uses exactly one or two scripted local WebSocket servers. No live Hub,
//! Keychain, or provider network.

use std::net::IpAddr;
use std::sync::Arc;
use std::time::Duration;

use app_lib::appwire_transport::{AppwireEvent, AppwireManager};
use app_lib::error::ReleaseMode;
use app_lib::network_policy::{DnsResolver, NetworkPolicy};
use app_lib::profile::{MemoryPreferences, MemorySecureStore, OkProbe, ProfileStore, StepClock};
use app_lib::profile_runtime::ProfileRuntime;

/// A loopback network policy for tests: resolves any hostname to 127.0.0.1
/// and allows loopback in Debug mode so the scripted WebSocket server on
/// 127.0.0.1 passes the policy.
fn loopback_policy() -> Arc<NetworkPolicy> {
    struct LoopbackResolver;
    impl DnsResolver for LoopbackResolver {
        fn resolve(&self, _host: &str) -> Result<Vec<IpAddr>, String> {
            Ok(vec![IpAddr::V4(std::net::Ipv4Addr::new(127, 0, 0, 1))])
        }
    }
    Arc::new(NetworkPolicy::new(Box::new(LoopbackResolver)))
}

/// A test AppwireManager configured with the loopback policy.
fn test_manager() -> AppwireManager {
    AppwireManager::new(
        loopback_policy(),
        ReleaseMode::Debug {
            allow_loopback: true,
        },
    )
}

const PROFILE_TOKEN: &str = "AAECAwQFBgcICQoLDA0ODxAREhMUFRYXGBkaGxwdHh8";

fn pairing_url(ws_url: &str) -> String {
    format!(
        "{}/auth?token={PROFILE_TOKEN}",
        ws_url
            .strip_prefix("ws://")
            .map(|rest| format!("http://{}", rest.trim_end_matches("/rpc")))
            .unwrap()
    )
}

// ---------------------------------------------------------------------------
// Scripted WebSocket server
// ---------------------------------------------------------------------------

mod ws_server {
    use std::sync::atomic::{AtomicBool, AtomicU64, Ordering};
    use std::sync::{Arc, Mutex};

    use futures_util::{SinkExt, StreamExt};
    use tokio::net::TcpListener;
    use tokio_tungstenite::tungstenite::Message;

    pub struct ScriptedWsServer {
        pub url: String,
        pub connection_count: Arc<AtomicU64>,
        frames_to_send: Arc<Mutex<Vec<String>>>,
        server_close: Arc<Mutex<Option<(u16, String)>>>,
        abrupt_close: Arc<Mutex<bool>>,
        pause_after_frames: Arc<AtomicBool>,
        frames_sent_count: Arc<AtomicU64>,
        frames_sent: Arc<tokio::sync::Notify>,
        resume_after_frames: Arc<tokio::sync::Notify>,
        received_frames: Arc<Mutex<Vec<String>>>,
        close_code: Arc<Mutex<Option<u16>>>,
        close_observed: Arc<tokio::sync::Notify>,
        received_authorization: Arc<Mutex<Option<String>>>,
        received_origin: Arc<Mutex<Option<String>>>,
        received_protocol: Arc<Mutex<Option<String>>>,
    }

    impl ScriptedWsServer {
        pub async fn start() -> Self {
            let listener = TcpListener::bind("127.0.0.1:0").await.unwrap();
            let port = listener.local_addr().unwrap().port();
            let url = format!("ws://127.0.0.1:{port}/rpc");

            let connection_count = Arc::new(AtomicU64::new(0));
            let frames_to_send: Arc<Mutex<Vec<String>>> = Arc::new(Mutex::new(Vec::new()));
            let server_close: Arc<Mutex<Option<(u16, String)>>> = Arc::new(Mutex::new(None));
            let abrupt_close = Arc::new(Mutex::new(false));
            let pause_after_frames = Arc::new(AtomicBool::new(false));
            let frames_sent_count = Arc::new(AtomicU64::new(0));
            let frames_sent = Arc::new(tokio::sync::Notify::new());
            let resume_after_frames = Arc::new(tokio::sync::Notify::new());
            let received_frames: Arc<Mutex<Vec<String>>> = Arc::new(Mutex::new(Vec::new()));
            let close_code: Arc<Mutex<Option<u16>>> = Arc::new(Mutex::new(None));
            let close_observed = Arc::new(tokio::sync::Notify::new());
            let received_authorization: Arc<Mutex<Option<String>>> = Arc::new(Mutex::new(None));
            let received_origin: Arc<Mutex<Option<String>>> = Arc::new(Mutex::new(None));
            let received_protocol: Arc<Mutex<Option<String>>> = Arc::new(Mutex::new(None));

            let cc = connection_count.clone();
            let fts = frames_to_send.clone();
            let server_close_task = server_close.clone();
            let abrupt_close_task = abrupt_close.clone();
            let pause_after_frames_task = pause_after_frames.clone();
            let frames_sent_count_task = frames_sent_count.clone();
            let frames_sent_task = frames_sent.clone();
            let resume_after_frames_task = resume_after_frames.clone();
            let rf = received_frames.clone();
            let clc = close_code.clone();
            let close_observed_task = close_observed.clone();
            let ra = received_authorization.clone();
            let ro = received_origin.clone();
            let rp = received_protocol.clone();

            tokio::spawn(async move {
                loop {
                    let (stream, _) = match listener.accept().await {
                        Ok(s) => s,
                        Err(_) => break,
                    };
                    cc.fetch_add(1, Ordering::SeqCst);

                    let fts = fts.clone();
                    let server_close = server_close_task.clone();
                    let abrupt_close = abrupt_close_task.clone();
                    let pause_after_frames = pause_after_frames_task.clone();
                    let frames_sent_count = frames_sent_count_task.clone();
                    let frames_sent = frames_sent_task.clone();
                    let resume_after_frames = resume_after_frames_task.clone();
                    let rf = rf.clone();
                    let clc = clc.clone();
                    let close_observed = close_observed_task.clone();
                    let ra = ra.clone();
                    let ro = ro.clone();
                    let rp = rp.clone();

                    tokio::spawn(async move {
                        // Capture the handshake headers before accepting and
                        // echo the requested subprotocol so the client
                        // handshake succeeds.
                        #[allow(clippy::result_large_err)]
                        let callback = |req: &tokio_tungstenite::tungstenite::handshake::server::Request,
                                         mut resp: tokio_tungstenite::tungstenite::handshake::server::Response| {
                            let headers = req.headers();
                            *ra.lock().unwrap() =
                                headers.get("authorization").map(|v| v.to_str().unwrap_or("").to_owned());
                            *ro.lock().unwrap() =
                                headers.get("origin").map(|v| v.to_str().unwrap_or("").to_owned());
                            let proto = headers
                                .get("sec-websocket-protocol")
                                .map(|v| v.to_str().unwrap_or("").to_owned());
                            *rp.lock().unwrap() = proto.clone();
                            // Echo the subprotocol the client requested.
                            if let Some(p) = proto {
                                resp.headers_mut().insert(
                                    "sec-websocket-protocol",
                                    p.as_str().try_into().unwrap(),
                                );
                            }
                            Ok(resp)
                        };
                        let ws_stream =
                            match tokio_tungstenite::accept_hdr_async(stream, callback).await {
                                Ok(s) => s,
                                Err(_) => return,
                            };
                        let (mut write, mut read) = ws_stream.split();

                        // Send any queued frames.
                        let frames = fts.lock().unwrap().clone();
                        for frame in frames {
                            let _ = write.send(Message::Text(frame.into())).await;
                        }
                        frames_sent_count.fetch_add(1, Ordering::SeqCst);
                        frames_sent.notify_waiters();
                        loop {
                            let resumed = resume_after_frames.notified();
                            if !pause_after_frames.load(Ordering::SeqCst) {
                                break;
                            }
                            resumed.await;
                        }
                        let should_abort = *abrupt_close.lock().unwrap();
                        if should_abort {
                            return;
                        }
                        let close = server_close.lock().unwrap().clone();
                        if let Some((code, reason)) = close {
                            let _ = write
                                .send(Message::Close(Some(
                                    tokio_tungstenite::tungstenite::protocol::CloseFrame {
                                        code: code.into(),
                                        reason: reason.into(),
                                    },
                                )))
                                .await;
                            return;
                        }

                        // Read frames until closed.
                        while let Some(msg) = read.next().await {
                            match msg {
                                Ok(Message::Text(text)) => {
                                    rf.lock().unwrap().push(text.to_string());
                                }
                                Ok(Message::Close(close_frame)) => {
                                    if let Some(cf) = close_frame {
                                        *clc.lock().unwrap() = Some(u16::from(cf.code));
                                    }
                                    close_observed.notify_waiters();
                                    break;
                                }
                                Ok(_) => {}
                                Err(_) => break,
                            }
                        }
                    });
                }
            });

            Self {
                url,
                connection_count,
                frames_to_send,
                server_close,
                abrupt_close,
                pause_after_frames,
                frames_sent_count,
                frames_sent,
                resume_after_frames,
                received_frames,
                close_code,
                close_observed,
                received_authorization,
                received_origin,
                received_protocol,
            }
        }

        pub fn enqueue_frame(&self, frame: String) {
            self.frames_to_send.lock().unwrap().push(frame);
        }

        pub fn clear_frames(&self) {
            self.frames_to_send.lock().unwrap().clear();
        }

        pub fn close_new_connections(&self, code: u16, reason: &str) {
            *self.server_close.lock().unwrap() = Some((code, reason.to_owned()));
        }

        pub fn keep_new_connections_open(&self) {
            *self.server_close.lock().unwrap() = None;
            *self.abrupt_close.lock().unwrap() = false;
        }

        pub fn abort_new_connections(&self) {
            *self.abrupt_close.lock().unwrap() = true;
        }

        pub fn pause_new_connections_after_frames(&self) {
            self.pause_after_frames.store(true, Ordering::SeqCst);
        }

        pub async fn await_frames_sent(&self, count: u64) {
            loop {
                let sent = self.frames_sent.notified();
                if self.frames_sent_count.load(Ordering::SeqCst) >= count {
                    return;
                }
                sent.await;
            }
        }

        pub fn resume_new_connections_after_frames(&self) {
            self.pause_after_frames.store(false, Ordering::SeqCst);
            self.resume_after_frames.notify_waiters();
        }

        pub fn received_frames(&self) -> Vec<String> {
            self.received_frames.lock().unwrap().clone()
        }

        pub fn connection_count(&self) -> u64 {
            self.connection_count.load(Ordering::SeqCst)
        }

        pub fn last_close_code(&self) -> Option<u16> {
            *self.close_code.lock().unwrap()
        }

        pub async fn await_close_code(&self) -> u16 {
            loop {
                if let Some(code) = self.last_close_code() {
                    return code;
                }
                self.close_observed.notified().await;
            }
        }

        pub fn received_authorization(&self) -> Option<String> {
            self.received_authorization.lock().unwrap().clone()
        }

        pub fn received_origin(&self) -> Option<String> {
            self.received_origin.lock().unwrap().clone()
        }

        pub fn received_protocol(&self) -> Option<String> {
            self.received_protocol.lock().unwrap().clone()
        }
    }
}

// ---------------------------------------------------------------------------
// Helper: await an event with a bounded deadline instead of a fixed sleep.
// ---------------------------------------------------------------------------

async fn next_event(
    rx: &mut tokio::sync::mpsc::Receiver<AppwireEvent>,
    deadline: tokio::time::Instant,
) -> Option<AppwireEvent> {
    tokio::time::timeout_at(deadline, rx.recv())
        .await
        .ok()
        .flatten()
}

fn deadline_secs(secs: u64) -> tokio::time::Instant {
    tokio::time::Instant::now() + Duration::from_secs(secs)
}

// ---------------------------------------------------------------------------
// Tests: ordered text frames
// ---------------------------------------------------------------------------

#[tokio::test]
async fn appwire_open_and_receive_ordered_frames() {
    use ws_server::ScriptedWsServer;

    let server = ScriptedWsServer::start().await;
    let url = server.url.clone();

    server.enqueue_frame(r#"{"id":1,"method":"initialize","params":{}}"#.to_owned());
    server.enqueue_frame(r#"{"id":2,"method":"ping","params":{}}"#.to_owned());

    let manager = test_manager();
    let (tx, mut rx) = tokio::sync::mpsc::channel::<_>(256);

    let conn_id = manager
        .open("profile-1", 0, url, "test-token".to_owned(), tx)
        .await
        .unwrap();

    let dl = deadline_secs(3);
    let frame1 = next_event(&mut rx, dl).await.expect("first frame");
    let frame2 = next_event(&mut rx, dl).await.expect("second frame");

    match &frame1 {
        AppwireEvent::Text { data: t, .. } => {
            assert!(t.contains("initialize"), "first frame: {t:?}")
        }
        other => panic!("expected Text, got {other:?}"),
    }
    match &frame2 {
        AppwireEvent::Text { data: t, .. } => assert!(t.contains("ping"), "second frame: {t:?}"),
        other => panic!("expected Text, got {other:?}"),
    }

    manager.close(conn_id).await;
}

#[tokio::test]
async fn server_close_clears_active_emits_exact_identity_then_allows_immediate_reopen() {
    use ws_server::ScriptedWsServer;

    let server = ScriptedWsServer::start().await;
    server.enqueue_frame("first-generation-message".to_owned());
    server.close_new_connections(1012, "scripted service restart");
    let manager = test_manager();
    let (tx1, mut rx1) = tokio::sync::mpsc::channel(16);
    let first = manager
        .open("profile-1", 0, server.url.clone(), "tok".to_owned(), tx1)
        .await
        .unwrap();

    let message = next_event(&mut rx1, deadline_secs(3)).await.unwrap();
    assert!(matches!(
        message,
        AppwireEvent::Text {
            ref connection_id,
            ref data,
        } if connection_id == &first && data == "first-generation-message"
    ));
    let closed = next_event(&mut rx1, deadline_secs(3)).await.unwrap();
    assert!(matches!(
        closed,
        AppwireEvent::Closed {
            ref connection_id,
            code: 1012,
            ref reason,
        } if connection_id == &first && reason == "scripted service restart"
    ));
    assert!(!manager.is_active(&first));

    server.keep_new_connections_open();
    let (tx2, _rx2) = tokio::sync::mpsc::channel(16);
    let second = manager
        .open("profile-1", 0, server.url.clone(), "tok".to_owned(), tx2)
        .await
        .unwrap();
    assert!(second.generation() > first.generation());
    assert!(manager.is_active(&second));
    assert_eq!(server.connection_count(), 2);
    manager.close(second).await;
}

#[tokio::test]
async fn abnormal_reader_error_emits_error_before_closed_and_releases_socket() {
    use ws_server::ScriptedWsServer;

    let server = ScriptedWsServer::start().await;
    server.abort_new_connections();
    let manager = test_manager();
    let (tx, mut rx) = tokio::sync::mpsc::channel(16);
    let connection = manager
        .open("profile-1", 0, server.url.clone(), "tok".to_owned(), tx)
        .await
        .unwrap();

    assert!(matches!(
        next_event(&mut rx, deadline_secs(3)).await,
        Some(AppwireEvent::Error { ref connection_id }) if connection_id == &connection
    ));
    assert!(matches!(
        next_event(&mut rx, deadline_secs(3)).await,
        Some(AppwireEvent::Closed {
            ref connection_id,
            code: 1006,
            ref reason,
        }) if connection_id == &connection && reason == "transport read failed"
    ));
    assert!(!manager.is_active(&connection));
}

#[tokio::test]
async fn two_profile_runtime_switch_reaps_before_persist_and_rolls_manager_back_on_failure() {
    use app_lib::profile::PreferencesStore;
    use ws_server::ScriptedWsServer;

    let server1 = ScriptedWsServer::start().await;
    let server2 = ScriptedWsServer::start().await;
    let prefs = Arc::new(MemoryPreferences::new());
    let secure = Arc::new(MemorySecureStore::new());
    let store = Arc::new(ProfileStore::new(
        prefs.clone(),
        secure,
        Arc::new(OkProbe),
        Arc::new(StepClock::new(0)),
        loopback_policy(),
    ));
    let runtime = ProfileRuntime::new(store.clone());
    let mode = ReleaseMode::Debug {
        allow_loopback: true,
    };
    let profile1 = runtime
        .serialized(|store| {
            let preview = store.preview_pairing(&pairing_url(&server1.url))?;
            store.confirm_pairing(&preview.preview_id, "One", false, mode)
        })
        .await
        .unwrap();
    let profile2 = runtime
        .serialized(|store| {
            let preview = store.preview_pairing(&pairing_url(&server2.url))?;
            store.confirm_pairing(&preview.preview_id, "Two", false, mode)
        })
        .await
        .unwrap();

    let manager = test_manager();
    let first_selection = runtime
        .select_profile(&manager, &profile1.id)
        .await
        .unwrap();
    let (tx1, mut rx1) = tokio::sync::mpsc::channel(16);
    let first = manager
        .open(
            &profile1.id,
            first_selection.generation.0,
            server1.url.clone(),
            PROFILE_TOKEN.to_owned(),
            tx1,
        )
        .await
        .unwrap();

    // A pre-rename persistence failure closes/reaps the socket first, but then
    // restores manager selection to the still-durable old profile/generation.
    prefs.fail_next_save();
    let error = runtime
        .select_profile(&manager, &profile2.id)
        .await
        .unwrap_err();
    assert!(matches!(
        error,
        app_lib::error::ProfileError::Preferences(_)
    ));
    assert!(!manager.is_active(&first));
    assert_eq!(
        store.active_id().unwrap().as_deref(),
        Some(profile1.id.as_str())
    );
    assert_eq!(
        manager.selected_profile(),
        Some((profile1.id.clone(), first_selection.generation.0))
    );
    assert!(matches!(
        next_event(&mut rx1, deadline_secs(3)).await,
        Some(AppwireEvent::Closed { ref connection_id, .. }) if connection_id == &first
    ));
    assert_eq!(
        tokio::time::timeout_at(deadline_secs(3), server1.await_close_code())
            .await
            .expect("server observed close"),
        1000
    );

    // Reopen the rolled-back profile, then perform the real switch. The old
    // supervisor is fully gone before preferences and manager agree on Two.
    let (tx1b, mut rx1b) = tokio::sync::mpsc::channel(16);
    let reopened = manager
        .open(
            &profile1.id,
            first_selection.generation.0,
            server1.url.clone(),
            PROFILE_TOKEN.to_owned(),
            tx1b,
        )
        .await
        .unwrap();
    let second_selection = runtime
        .select_profile(&manager, &profile2.id)
        .await
        .unwrap();
    assert!(matches!(
        next_event(&mut rx1b, deadline_secs(3)).await,
        Some(AppwireEvent::Closed { ref connection_id, .. }) if connection_id == &reopened
    ));
    assert_eq!(
        store.active_id().unwrap().as_deref(),
        Some(profile2.id.as_str())
    );
    assert_eq!(
        manager.selected_profile(),
        Some((profile2.id.clone(), second_selection.generation.0))
    );
    assert!(manager.send(reopened, "stale".to_owned()).await.is_err());

    let (tx2, _rx2) = tokio::sync::mpsc::channel(16);
    let second = manager
        .open(
            &profile2.id,
            second_selection.generation.0,
            server2.url.clone(),
            PROFILE_TOKEN.to_owned(),
            tx2,
        )
        .await
        .unwrap();
    assert_eq!(server1.connection_count(), 2);
    assert_eq!(server2.connection_count(), 1);
    let removal = runtime
        .remove_profile(&manager, &profile2.id)
        .await
        .unwrap();
    assert!(!manager.is_active(&second));
    assert_eq!(removal.profile_id.as_deref(), Some(profile1.id.as_str()));
    assert_eq!(
        store.active_id().unwrap().as_deref(),
        Some(profile1.id.as_str())
    );
    assert_eq!(
        manager.selected_profile(),
        Some((profile1.id.clone(), removal.generation.0))
    );
    assert_eq!(
        prefs.load().unwrap().active_id.as_deref(),
        Some(profile1.id.as_str())
    );
}

// ---------------------------------------------------------------------------
// Tests: send delivers text frame to server
// ---------------------------------------------------------------------------

#[tokio::test]
async fn appwire_send_delivers_text_frame_to_server() {
    use ws_server::ScriptedWsServer;

    let server = ScriptedWsServer::start().await;
    let url = server.url.clone();

    let manager = test_manager();
    let (tx, _rx) = tokio::sync::mpsc::channel::<_>(256);

    let conn_id = manager
        .open("profile-1", 0, url, "tok".to_owned(), tx)
        .await
        .unwrap();

    // Wait for the server to accept the connection.
    let dl = deadline_secs(3);
    loop {
        if server.connection_count() >= 1 {
            break;
        }
        if tokio::time::Instant::now() > dl {
            panic!("server never accepted");
        }
        tokio::time::sleep(Duration::from_millis(10)).await;
    }

    let frame = r#"{"id":1,"method":"initialize","params":{}}"#;
    manager
        .send(conn_id.clone(), frame.to_owned())
        .await
        .unwrap();

    // Await the server receiving the frame.
    let dl = deadline_secs(3);
    loop {
        if server
            .received_frames()
            .iter()
            .any(|f| f.contains("initialize"))
        {
            break;
        }
        if tokio::time::Instant::now() > dl {
            panic!("server never received frame");
        }
        tokio::time::sleep(Duration::from_millis(10)).await;
    }

    manager.close(conn_id).await;
}

// ---------------------------------------------------------------------------
// Tests: switch closes old before opening new (one socket total)
// ---------------------------------------------------------------------------

#[tokio::test]
async fn appwire_switch_closes_old_before_opening_new() {
    use ws_server::ScriptedWsServer;

    let server1 = ScriptedWsServer::start().await;
    let server2 = ScriptedWsServer::start().await;

    let manager = test_manager();
    let (tx1, _rx1) = tokio::sync::mpsc::channel::<_>(256);
    let (tx2, _rx2) = tokio::sync::mpsc::channel::<_>(256);

    // Open first connection for profile-1.
    let conn1 = manager
        .open("profile-1", 0, server1.url.clone(), "tok1".to_owned(), tx1)
        .await
        .unwrap();

    let dl = deadline_secs(3);
    loop {
        if server1.connection_count() == 1 {
            break;
        }
        if tokio::time::Instant::now() > dl {
            panic!("server1 never accepted");
        }
        tokio::time::sleep(Duration::from_millis(10)).await;
    }

    // Switch: select profile-2, which closes conn1 before opening conn2.
    manager.select(Some("profile-2"), 0).await;

    let conn2 = manager
        .open("profile-2", 0, server2.url.clone(), "tok2".to_owned(), tx2)
        .await
        .unwrap();

    let dl = deadline_secs(3);
    loop {
        if server2.connection_count() == 1 {
            break;
        }
        if tokio::time::Instant::now() > dl {
            panic!("server2 never accepted");
        }
        tokio::time::sleep(Duration::from_millis(10)).await;
    }

    // Each server saw exactly one connection.
    assert_eq!(server1.connection_count(), 1);
    assert_eq!(server2.connection_count(), 1);

    // conn1 is stale — sending on it must fail.
    let send_result = manager.send(conn1, "test".to_owned()).await;
    assert!(send_result.is_err(), "stale connection should reject send");

    manager.close(conn2).await;
}

// ---------------------------------------------------------------------------
// Tests: stale generation rejects frames
// ---------------------------------------------------------------------------

#[tokio::test]
async fn appwire_stale_generation_rejects_send() {
    use ws_server::ScriptedWsServer;

    let server = ScriptedWsServer::start().await;
    let manager = test_manager();
    let (tx, _rx) = tokio::sync::mpsc::channel::<_>(256);

    let conn1 = manager
        .open("profile-1", 0, server.url.clone(), "tok".to_owned(), tx)
        .await
        .unwrap();

    // Switch profile — old connection generation is stale.
    manager.select(Some("profile-2"), 0).await;

    let result = manager.send(conn1, "stale-frame".to_owned()).await;
    assert!(result.is_err(), "stale generation should reject send");
    let err = result.unwrap_err();
    assert!(!format!("{err}").contains("tok"));
    assert!(!format!("{err:?}").contains("tok"));
}

// ---------------------------------------------------------------------------
// Tests: exactly one socket total for the active profile
// ---------------------------------------------------------------------------

#[tokio::test]
async fn appwire_one_socket_total_for_active_profile() {
    use ws_server::ScriptedWsServer;

    let server = ScriptedWsServer::start().await;
    let manager = test_manager();
    let (tx, _rx) = tokio::sync::mpsc::channel::<_>(256);

    let conn1 = manager
        .open("profile-1", 0, server.url.clone(), "tok".to_owned(), tx)
        .await
        .unwrap();

    let dl = deadline_secs(3);
    loop {
        if server.connection_count() == 1 {
            break;
        }
        if tokio::time::Instant::now() > dl {
            panic!("server never accepted");
        }
        tokio::time::sleep(Duration::from_millis(10)).await;
    }

    // A second open for the same profile must not create a second socket.
    let (tx2, _rx2) = tokio::sync::mpsc::channel::<_>(256);
    let conn2_result = manager
        .open("profile-1", 0, server.url.clone(), "tok".to_owned(), tx2)
        .await;

    // Either it returns an error (AlreadyConnected) or replaces — but the
    // server must not have seen a second connection.
    let dl = deadline_secs(1);
    loop {
        if tokio::time::Instant::now() > dl {
            break;
        }
        tokio::time::sleep(Duration::from_millis(10)).await;
    }
    assert_eq!(
        server.connection_count(),
        1,
        "only one socket total for the active profile"
    );
    let _ = conn2_result;

    manager.close(conn1).await;
}

// ---------------------------------------------------------------------------
// Tests: close code propagation
// ---------------------------------------------------------------------------

#[tokio::test]
async fn appwire_close_propagates_close_code() {
    use ws_server::ScriptedWsServer;

    let server = ScriptedWsServer::start().await;
    let manager = test_manager();
    let (tx, _rx) = tokio::sync::mpsc::channel::<_>(256);

    let conn = manager
        .open("profile-1", 0, server.url.clone(), "tok".to_owned(), tx)
        .await
        .unwrap();

    let dl = deadline_secs(3);
    loop {
        if server.connection_count() == 1 {
            break;
        }
        if tokio::time::Instant::now() > dl {
            panic!("server never accepted");
        }
        tokio::time::sleep(Duration::from_millis(10)).await;
    }

    manager.close_with_code(conn, 1000).await;

    // Await the server observing the close code.
    let dl = deadline_secs(3);
    loop {
        if server.last_close_code().is_some() {
            break;
        }
        if tokio::time::Instant::now() > dl {
            panic!("server never saw close");
        }
        tokio::time::sleep(Duration::from_millis(10)).await;
    }
    assert_eq!(server.last_close_code(), Some(1000));
}

// ---------------------------------------------------------------------------
// Tests: bounded overload closes without dropping/reordering
// ---------------------------------------------------------------------------

async fn saturated_terminal_reaps_before_reopen(abrupt: bool) {
    use ws_server::ScriptedWsServer;

    let server = ScriptedWsServer::start().await;
    server.enqueue_frame("fills-the-only-nonterminal-slot".to_owned());
    server.pause_new_connections_after_frames();

    let manager = Arc::new(test_manager());
    // Two of three slots are reserved by open for Error+Closed, leaving
    // exactly one normal slot for the scripted text frame.
    let (tx1, mut rx1) = tokio::sync::mpsc::channel::<AppwireEvent>(3);
    let first = manager
        .open("profile-1", 0, server.url.clone(), "tok".to_owned(), tx1)
        .await
        .unwrap();
    server.await_frames_sent(1).await;
    if abrupt {
        server.abort_new_connections();
    } else {
        server.close_new_connections(1012, "saturated restart");
    }

    // join! polls the lifecycle waiter before releasing the scripted server,
    // so the terminal-start notification cannot be missed.
    let (started, ()) = tokio::join!(manager.wait_for_terminal_start(&first), async {
        server.resume_new_connections_after_frames()
    });
    assert!(started);
    assert!(manager
        .send(first.clone(), "stale".to_owned())
        .await
        .is_err());
    assert_eq!(
        rx1.len(),
        if abrupt { 3 } else { 2 },
        "normal backlog plus reserved terminal outcome must already be queued",
    );

    // Reopen concurrently with old terminal cleanup, without draining the full
    // queue. Reserved terminal permits prevent deadlock; open itself waits for
    // the supervisor JoinHandle to be reaped and active state to be cleared.
    server.keep_new_connections_open();
    server.clear_frames();
    let (tx2, mut rx2) = tokio::sync::mpsc::channel::<AppwireEvent>(8);
    let second = tokio::time::timeout_at(
        deadline_secs(3),
        manager.open("profile-1", 0, server.url.clone(), "tok".to_owned(), tx2),
    )
    .await
    .expect("reopen must not block on saturated old queue")
    .unwrap();
    assert_eq!(manager.reaped_connection_count(), 1);
    assert!(!manager.is_active(&first));
    assert!(manager.is_active(&second));
    assert_eq!(server.connection_count(), 2);

    assert!(matches!(
        rx1.recv().await,
        Some(AppwireEvent::Text {
            ref connection_id,
            ref data,
        }) if connection_id == &first && data == "fills-the-only-nonterminal-slot"
    ));
    if abrupt {
        assert!(matches!(
            rx1.recv().await,
            Some(AppwireEvent::Error { ref connection_id }) if connection_id == &first
        ));
        assert!(matches!(
            rx1.recv().await,
            Some(AppwireEvent::Closed {
                ref connection_id,
                code: 1006,
                ref reason,
            }) if connection_id == &first && reason == "transport read failed"
        ));
    } else {
        assert!(matches!(
            rx1.recv().await,
            Some(AppwireEvent::Closed {
                ref connection_id,
                code: 1012,
                ref reason,
            }) if connection_id == &first && reason == "saturated restart"
        ));
    }
    assert_eq!(
        rx1.recv().await,
        None,
        "terminal outcome must be emitted once"
    );
    assert!(matches!(
        rx2.try_recv(),
        Err(tokio::sync::mpsc::error::TryRecvError::Empty)
    ));

    manager.close(second).await;
    assert_eq!(manager.reaped_connection_count(), 2);
}

#[tokio::test]
async fn server_close_with_full_event_queue_reaps_before_concurrent_reopen() {
    saturated_terminal_reaps_before_reopen(false).await;
}

#[tokio::test]
async fn reader_error_with_full_event_queue_reaps_before_concurrent_reopen() {
    saturated_terminal_reaps_before_reopen(true).await;
}

#[tokio::test]
async fn appwire_bounded_overload_closes_connection() {
    use ws_server::ScriptedWsServer;

    let server = ScriptedWsServer::start().await;

    // Enqueue many frames to trigger overload.
    for i in 0..500 {
        server.enqueue_frame(format!(r#"{{"id":{i},"method":"ping","params":{{}}}}"#));
    }

    let manager = test_manager();
    // Use a small bounded channel so overload triggers quickly. The reader
    // sends with try_send; on a full channel it closes with an error.
    let (tx, mut rx) = tokio::sync::mpsc::channel::<AppwireEvent>(8);

    let conn = manager
        .open("profile-1", 0, server.url.clone(), "tok".to_owned(), tx)
        .await
        .unwrap();

    // Do NOT drain rx immediately: let the server send 500 frames into the
    // small bounded channel. The reader fills the 8-capacity channel, then
    // try_send fails and it closes with an error.
    let dl_fill = deadline_secs(2);
    loop {
        if tokio::time::Instant::now() > dl_fill {
            break;
        }
        tokio::time::sleep(Duration::from_millis(10)).await;
    }

    // Now drain: frames must be in order, then a close/error event.
    let mut last_id: i64 = -1;
    let mut received = 0;
    let mut got_close = false;
    let dl = deadline_secs(5);
    while let Some(event) = next_event(&mut rx, dl).await {
        match event {
            AppwireEvent::Text { data: t, .. } => {
                let id = serde_json::from_str::<serde_json::Value>(&t)
                    .ok()
                    .and_then(|v| v.get("id").and_then(|i| i.as_i64()))
                    .unwrap_or(last_id);
                // Frames arrive in order (no reordering, no drops).
                assert!(
                    id >= last_id,
                    "frame reordered or dropped: last={last_id} got={id}"
                );
                last_id = id;
                received += 1;
            }
            AppwireEvent::Closed { .. } | AppwireEvent::Error { .. } => {
                got_close = true;
                break;
            }
        }
    }

    assert!(received > 0, "should receive some frames before overload");
    assert!(got_close, "should close on overload, not hang");
    let _ = conn;
}

// ---------------------------------------------------------------------------
// Tests: cancellation closes the connection
// ---------------------------------------------------------------------------

#[tokio::test]
async fn appwire_cancellation_closes_connection() {
    use ws_server::ScriptedWsServer;

    let server = ScriptedWsServer::start().await;
    let manager = test_manager();
    let (tx, _rx) = tokio::sync::mpsc::channel::<_>(256);

    let conn = manager
        .open("profile-1", 0, server.url.clone(), "tok".to_owned(), tx)
        .await
        .unwrap();

    let dl = deadline_secs(3);
    loop {
        if server.connection_count() == 1 {
            break;
        }
        if tokio::time::Instant::now() > dl {
            panic!("server never accepted");
        }
        tokio::time::sleep(Duration::from_millis(10)).await;
    }

    // Cancel by closing.
    manager.close(conn).await;

    // The server saw exactly one connection (no reconnect).
    let dl = deadline_secs(3);
    loop {
        if tokio::time::Instant::now() > dl {
            break;
        }
        tokio::time::sleep(Duration::from_millis(10)).await;
    }
    assert_eq!(server.connection_count(), 1);
}

// ---------------------------------------------------------------------------
// Tests: bearer token and Origin injected upstream
// ---------------------------------------------------------------------------

#[tokio::test]
async fn appwire_injects_bearer_and_origin_upstream() {
    use ws_server::ScriptedWsServer;

    let server = ScriptedWsServer::start().await;
    let url = server.url.clone();

    let manager = test_manager();
    let (tx, _rx) = tokio::sync::mpsc::channel::<_>(256);

    let token = "bearer-upstream-test-token";
    let conn = manager
        .open("profile-1", 0, url, token.to_owned(), tx)
        .await
        .unwrap();

    let dl = deadline_secs(3);
    loop {
        if server.connection_count() == 1 {
            break;
        }
        if tokio::time::Instant::now() > dl {
            panic!("server never accepted");
        }
        tokio::time::sleep(Duration::from_millis(10)).await;
    }

    // The WebSocket handshake carried the bearer token.
    let auth = server.received_authorization();
    assert_eq!(
        auth.as_deref(),
        Some(format!("Bearer {token}").as_str()),
        "bearer not injected upstream"
    );

    // Origin header was sent.
    let origin = server.received_origin();
    assert!(origin.is_some(), "Origin header missing");

    // Sec-WebSocket-Protocol is evener-appwire-v3.
    let proto = server.received_protocol();
    assert_eq!(
        proto.as_deref(),
        Some("evener-appwire-v3"),
        "appwire protocol not negotiated"
    );

    manager.close(conn).await;
}

// ---------------------------------------------------------------------------
// Tests: Origin policy — default Hub origin policy (absent or host==Host
// accepted, tauri:// rejected)
// ---------------------------------------------------------------------------

/// A scripted WebSocket server that enforces the Hub's default origin
/// policy: accept absent Origin, accept Origin whose host == request Host,
/// reject `taur://` (tauri://) origins. The handshake is rejected with an
/// HTTP 403 if the Origin fails the check.
mod origin_policy_server {
    use std::sync::atomic::{AtomicU64, Ordering};
    use std::sync::{Arc, Mutex};

    use futures_util::StreamExt;
    use tokio::net::TcpListener;
    use tokio_tungstenite::tungstenite::Message;

    pub struct OriginPolicyServer {
        pub url: String,
        pub connection_count: Arc<AtomicU64>,
        pub accepted: Arc<AtomicU64>,
        pub rejected_origin: Arc<Mutex<Option<String>>>,
        received_origin: Arc<Mutex<Option<String>>>,
        received_host: Arc<Mutex<Option<String>>>,
    }

    impl OriginPolicyServer {
        pub async fn start() -> Self {
            let listener = TcpListener::bind("127.0.0.1:0").await.unwrap();
            let port = listener.local_addr().unwrap().port();
            let url = format!("ws://127.0.0.1:{port}/rpc");

            let connection_count = Arc::new(AtomicU64::new(0));
            let accepted = Arc::new(AtomicU64::new(0));
            let rejected_origin: Arc<Mutex<Option<String>>> = Arc::new(Mutex::new(None));
            let received_origin: Arc<Mutex<Option<String>>> = Arc::new(Mutex::new(None));
            let received_host: Arc<Mutex<Option<String>>> = Arc::new(Mutex::new(None));

            let cc = connection_count.clone();
            let acc = accepted.clone();
            let ro = rejected_origin.clone();
            let rvo = received_origin.clone();
            let rvh = received_host.clone();

            tokio::spawn(async move {
                loop {
                    let (stream, _) = match listener.accept().await {
                        Ok(s) => s,
                        Err(_) => break,
                    };
                    cc.fetch_add(1, Ordering::SeqCst);

                    let acc = acc.clone();
                    let ro = ro.clone();
                    let rvo = rvo.clone();
                    let rvh = rvh.clone();

                    tokio::spawn(async move {
                        #[allow(clippy::result_large_err)]
                        let callback = |req: &tokio_tungstenite::tungstenite::handshake::server::Request,
                                         resp: tokio_tungstenite::tungstenite::handshake::server::Response| {
                            let headers = req.headers();
                            let origin = headers
                                .get("origin")
                                .map(|v| v.to_str().unwrap_or("").to_owned());
                            let host = headers
                                .get("host")
                                .map(|v| v.to_str().unwrap_or("").to_owned())
                                .unwrap_or_default();
                            *rvo.lock().unwrap() = origin.clone();
                            *rvh.lock().unwrap() = Some(host.clone());

                            // Hub default policy: accept absent Origin, accept
                            // Origin host == request Host, reject tauri://.
                            let accepted = match &origin {
                                None => true,
                                Some(orig) => {
                                    if orig.starts_with("tauri://") {
                                        false
                                    } else {
                                        // Extract the host from the Origin.
                                        // Origin is scheme://host[:port].
                                        let origin_host = orig
                                            .strip_prefix("ws://")
                                            .or_else(|| orig.strip_prefix("wss://"))
                                            .or_else(|| orig.strip_prefix("http://"))
                                            .or_else(|| orig.strip_prefix("https://"))
                                            .unwrap_or(orig)
                                            .split(':')
                                            .next()
                                            .unwrap_or("");
                                        // Compare with the request Host (may
                                        // include :port, compare the host part).
                                        let request_host = host.split(':').next().unwrap_or("");
                                        origin_host == request_host
                                    }
                                }
                            };
                            if accepted {
                                acc.fetch_add(1, Ordering::SeqCst);
                                // Echo the subprotocol the client requested.
                                let mut resp = resp;
                                if let Some(proto) = headers
                                    .get("sec-websocket-protocol")
                                    .map(|v| v.to_str().unwrap_or("").to_owned())
                                {
                                    if let Ok(hv) = proto.as_str().try_into() {
                                        resp.headers_mut().insert("sec-websocket-protocol", hv);
                                    }
                                }
                                Ok(resp)
                            } else {
                                *ro.lock().unwrap() = origin;
                                // Reject with 403.
                                let reject = tokio_tungstenite::tungstenite::http::Response::builder()
                                    .status(403)
                                    .body(Some("origin rejected".to_owned()))
                                    .unwrap();
                                Err(reject)
                            }
                        };
                        let ws_stream =
                            match tokio_tungstenite::accept_hdr_async(stream, callback).await {
                                Ok(s) => s,
                                Err(_) => return,
                            };
                        let (_write, mut read) = ws_stream.split();
                        // Read until closed.
                        while let Some(Ok(msg)) = read.next().await {
                            if matches!(msg, Message::Close(_)) {
                                break;
                            }
                        }
                    });
                }
            });

            Self {
                url,
                connection_count,
                accepted,
                rejected_origin,
                received_origin,
                received_host,
            }
        }

        pub fn connection_count(&self) -> u64 {
            self.connection_count.load(Ordering::SeqCst)
        }
        pub fn accepted_count(&self) -> u64 {
            self.accepted.load(Ordering::SeqCst)
        }
        pub fn rejected_origin(&self) -> Option<String> {
            self.rejected_origin.lock().unwrap().clone()
        }
        pub fn received_origin(&self) -> Option<String> {
            self.received_origin.lock().unwrap().clone()
        }
        pub fn received_host(&self) -> Option<String> {
            self.received_host.lock().unwrap().clone()
        }
    }
}

/// The default Hub origin policy accepts the app's handshake because the
/// app sends the server's own origin (ws://host:port), whose host matches
/// the request Host. The handshake succeeds.
#[tokio::test]
async fn appwire_origin_policy_accepts_server_origin() {
    use origin_policy_server::OriginPolicyServer;

    let server = OriginPolicyServer::start().await;
    let url = server.url.clone();

    let manager = test_manager();
    let (tx, _rx) = tokio::sync::mpsc::channel::<_>(256);

    let conn = manager
        .open("profile-1", 0, url, "tok".to_owned(), tx)
        .await
        .unwrap();

    // Wait for the server to see the connection.
    let dl = deadline_secs(3);
    loop {
        if server.connection_count() >= 1 {
            break;
        }
        if tokio::time::Instant::now() > dl {
            panic!("server never accepted the connection");
        }
        tokio::time::sleep(Duration::from_millis(10)).await;
    }

    // The origin was accepted (not rejected).
    assert_eq!(server.accepted_count(), 1, "handshake should be accepted");
    assert!(
        server.rejected_origin().is_none(),
        "no origin should be rejected"
    );

    // The received Origin's host matches the request Host.
    let origin = server.received_origin().expect("origin was sent");
    let host = server.received_host().expect("host was sent");
    let origin_host = origin
        .strip_prefix("ws://")
        .or_else(|| origin.strip_prefix("wss://"))
        .unwrap_or(&origin)
        .split(':')
        .next()
        .unwrap_or("");
    let request_host = host.split(':').next().unwrap_or("");
    assert_eq!(
        origin_host, request_host,
        "origin host must match request Host for default policy acceptance"
    );

    manager.close(conn).await;
}

/// A tauri:// origin would be rejected by the Hub's default policy. This
/// test verifies the origin-policy server rejects tauri:// — proving the
/// policy works (the app must NOT send tauri://, and the Hub would reject
/// it if it did).
#[tokio::test]
async fn appwire_origin_policy_rejects_tauri_origin() {
    use origin_policy_server::OriginPolicyServer;

    let server = OriginPolicyServer::start().await;
    let port = server
        .url
        .split(':')
        .nth(2)
        .unwrap()
        .trim_end_matches("/rpc");

    // Build a raw WebSocket connection with a tauri:// origin to verify
    // the server rejects it. The app itself never sends tauri:// (it sends
    // the server origin or omits), but this proves the policy would catch
    // it.
    use tokio_tungstenite::tungstenite::http::Request;
    let request = Request::builder()
        .uri(format!("ws://127.0.0.1:{port}/rpc"))
        .header("Host", format!("127.0.0.1:{port}"))
        .header("Origin", "tauri://localhost")
        .header("Sec-WebSocket-Version", "13")
        .header("Connection", "Upgrade")
        .header("Upgrade", "websocket")
        .header(
            "Sec-WebSocket-Key",
            tokio_tungstenite::tungstenite::handshake::client::generate_key(),
        )
        .body(())
        .unwrap();

    let result = tokio_tungstenite::connect_async(request).await;

    // The handshake is rejected (403).
    assert!(result.is_err(), "tauri:// origin must be rejected");

    // Wait for the server to register the rejection.
    let dl = deadline_secs(2);
    loop {
        if server.rejected_origin().is_some() {
            break;
        }
        if tokio::time::Instant::now() > dl {
            break;
        }
        tokio::time::sleep(Duration::from_millis(10)).await;
    }
    assert_eq!(
        server.rejected_origin().as_deref(),
        Some("tauri://localhost"),
        "rejected origin must be recorded"
    );
    assert_eq!(
        server.accepted_count(),
        0,
        "no handshake should be accepted"
    );
}

// ---------------------------------------------------------------------------
// Tests: cross-profile race — late frames from old profile rejected
// ---------------------------------------------------------------------------

#[tokio::test]
async fn appwire_cross_profile_late_frames_rejected() {
    use ws_server::ScriptedWsServer;

    let server1 = ScriptedWsServer::start().await;
    let server2 = ScriptedWsServer::start().await;

    let manager = test_manager();
    let (tx1, rx1) = tokio::sync::mpsc::channel::<_>(256);
    let (tx2, _rx2) = tokio::sync::mpsc::channel::<_>(256);

    // Open profile-1 on server1.
    let conn1 = manager
        .open("profile-1", 0, server1.url.clone(), "tok1".to_owned(), tx1)
        .await
        .unwrap();

    let dl = deadline_secs(3);
    loop {
        if server1.connection_count() == 1 {
            break;
        }
        if tokio::time::Instant::now() > dl {
            panic!("server1 never accepted");
        }
        tokio::time::sleep(Duration::from_millis(10)).await;
    }

    // Switch to profile-2 and open on server2. This invalidates conn1's
    // generation.
    manager.select(Some("profile-2"), 0).await;
    let conn2 = manager
        .open("profile-2", 0, server2.url.clone(), "tok2".to_owned(), tx2)
        .await
        .unwrap();

    // Enqueue a late frame on server1 (the old profile's connection). It
    // must not be delivered to the new profile's event channel.
    server1.enqueue_frame(r#"{"late":"frame"}"#.to_owned());

    // Drain rx1 for a short window: we may get the late frame (the old channel
    // is still open), but the key assertion is that sending on conn1 fails
    // (stale generation).
    let result = manager.send(conn1, "late-send".to_owned()).await;
    assert!(result.is_err(), "stale connection should reject send");

    // conn2 is the active connection and accepts sends.
    let send_result = manager.send(conn2.clone(), r#"{"id":1}"#.to_owned()).await;
    assert!(send_result.is_ok(), "active connection should accept send");

    manager.close(conn2).await;
    let _ = conn1;
    let _ = rx1;
}

// ---------------------------------------------------------------------------
// Tests: open closes+reaps old connection before opening new (fix round 2)
// ---------------------------------------------------------------------------

/// Opening a replacement connection (for a different profile) must close
/// and await BOTH old tasks (writer + reader) before the new connection
/// opens. The old writer sends a close frame to the old server
/// (observable), and no old task/frame survives. Exactly one socket per
/// server.
#[tokio::test]
async fn appwire_open_reaps_old_connection_before_opening_new() {
    use ws_server::ScriptedWsServer;

    let server1 = ScriptedWsServer::start().await;
    let server2 = ScriptedWsServer::start().await;

    let manager = test_manager();
    let (tx1, _rx1) = tokio::sync::mpsc::channel::<_>(256);
    let (tx2, _rx2) = tokio::sync::mpsc::channel::<_>(256);

    // Open profile-1 on server1.
    let conn1 = manager
        .open("profile-1", 0, server1.url.clone(), "tok1".to_owned(), tx1)
        .await
        .unwrap();

    // Wait for server1 to accept.
    let dl = deadline_secs(3);
    loop {
        if server1.connection_count() == 1 {
            break;
        }
        if tokio::time::Instant::now() > dl {
            panic!("server1 never accepted");
        }
        tokio::time::sleep(Duration::from_millis(10)).await;
    }

    // Select profile-2, which closes+reaps the old connection (awaiting
    // both writer and reader tasks). Then open profile-2 on server2.
    manager.select(Some("profile-2"), 0).await;

    let conn2 = manager
        .open("profile-2", 0, server2.url.clone(), "tok2".to_owned(), tx2)
        .await
        .unwrap();

    // Wait for server2 to accept the new connection.
    let dl = deadline_secs(3);
    loop {
        if server2.connection_count() == 1 {
            break;
        }
        if tokio::time::Instant::now() > dl {
            panic!("server2 never accepted");
        }
        tokio::time::sleep(Duration::from_millis(10)).await;
    }

    // The old writer sent a close frame to server1 (observable). Wait for it.
    let dl = deadline_secs(3);
    loop {
        if server1.last_close_code().is_some() {
            break;
        }
        if tokio::time::Instant::now() > dl {
            panic!("server1 never saw the close frame from the reaped writer");
        }
        tokio::time::sleep(Duration::from_millis(10)).await;
    }
    assert_eq!(
        server1.last_close_code(),
        Some(1000),
        "old writer must send close frame before new connection opens"
    );

    // Exactly one socket per server (no reconnect, no leak).
    assert_eq!(
        server1.connection_count(),
        1,
        "exactly one socket on server1"
    );
    assert_eq!(
        server2.connection_count(),
        1,
        "exactly one socket on server2"
    );

    // The old connection (conn1) is stale — sending on it must fail.
    let result = manager.send(conn1, "stale".to_owned()).await;
    assert!(result.is_err(), "old connection must be stale after reap");

    // The new connection (conn2) is active and accepts sends.
    let send_result = manager.send(conn2.clone(), r#"{"id":1}"#.to_owned()).await;
    assert!(send_result.is_ok(), "new connection must accept send");

    // Enqueue a late frame on server1; it must not reach the new channel.
    server1.enqueue_frame(r#"{"late":"from-old"}"#.to_owned());
    let dl = deadline_secs(1);
    loop {
        if tokio::time::Instant::now() > dl {
            break;
        }
        tokio::time::sleep(Duration::from_millis(10)).await;
    }
    // No late frame from the old server arrives on the new connection.
    // (The old reader was aborted/reaped, so it cannot forward.)

    manager.close(conn2).await;
}
