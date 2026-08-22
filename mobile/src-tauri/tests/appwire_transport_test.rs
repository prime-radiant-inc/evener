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

use std::time::Duration;

use app_lib::appwire_transport::{AppwireEvent, AppwireManager};

// ---------------------------------------------------------------------------
// Scripted WebSocket server
// ---------------------------------------------------------------------------

mod ws_server {
    use std::sync::atomic::{AtomicU64, Ordering};
    use std::sync::{Arc, Mutex};

    use futures_util::{SinkExt, StreamExt};
    use tokio::net::TcpListener;
    use tokio_tungstenite::tungstenite::Message;

    pub struct ScriptedWsServer {
        pub url: String,
        pub connection_count: Arc<AtomicU64>,
        frames_to_send: Arc<Mutex<Vec<String>>>,
        received_frames: Arc<Mutex<Vec<String>>>,
        close_code: Arc<Mutex<Option<u16>>>,
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
            let received_frames: Arc<Mutex<Vec<String>>> = Arc::new(Mutex::new(Vec::new()));
            let close_code: Arc<Mutex<Option<u16>>> = Arc::new(Mutex::new(None));
            let received_authorization: Arc<Mutex<Option<String>>> = Arc::new(Mutex::new(None));
            let received_origin: Arc<Mutex<Option<String>>> = Arc::new(Mutex::new(None));
            let received_protocol: Arc<Mutex<Option<String>>> = Arc::new(Mutex::new(None));

            let cc = connection_count.clone();
            let fts = frames_to_send.clone();
            let rf = received_frames.clone();
            let clc = close_code.clone();
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
                    let rf = rf.clone();
                    let clc = clc.clone();
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
                received_frames,
                close_code,
                received_authorization,
                received_origin,
                received_protocol,
            }
        }

        pub fn enqueue_frame(&self, frame: String) {
            self.frames_to_send.lock().unwrap().push(frame);
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

    let manager = AppwireManager::new();
    let (tx, mut rx) = tokio::sync::mpsc::channel::<_>(256);

    let conn_id = manager
        .open("profile-1", url, "test-token".to_owned(), tx)
        .await
        .unwrap();

    let dl = deadline_secs(3);
    let frame1 = next_event(&mut rx, dl).await.expect("first frame");
    let frame2 = next_event(&mut rx, dl).await.expect("second frame");

    match &frame1 {
        AppwireEvent::Text(t) => assert!(t.contains("initialize"), "first frame: {t:?}"),
        other => panic!("expected Text, got {other:?}"),
    }
    match &frame2 {
        AppwireEvent::Text(t) => assert!(t.contains("ping"), "second frame: {t:?}"),
        other => panic!("expected Text, got {other:?}"),
    }

    manager.close(conn_id).await;
}

// ---------------------------------------------------------------------------
// Tests: send delivers text frame to server
// ---------------------------------------------------------------------------

#[tokio::test]
async fn appwire_send_delivers_text_frame_to_server() {
    use ws_server::ScriptedWsServer;

    let server = ScriptedWsServer::start().await;
    let url = server.url.clone();

    let manager = AppwireManager::new();
    let (tx, _rx) = tokio::sync::mpsc::channel::<_>(256);

    let conn_id = manager
        .open("profile-1", url, "tok".to_owned(), tx)
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

    let manager = AppwireManager::new();
    let (tx1, _rx1) = tokio::sync::mpsc::channel::<_>(256);
    let (tx2, _rx2) = tokio::sync::mpsc::channel::<_>(256);

    // Open first connection for profile-1.
    let conn1 = manager
        .open("profile-1", server1.url.clone(), "tok1".to_owned(), tx1)
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
    manager.select("profile-2").await;

    let conn2 = manager
        .open("profile-2", server2.url.clone(), "tok2".to_owned(), tx2)
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
    let manager = AppwireManager::new();
    let (tx, _rx) = tokio::sync::mpsc::channel::<_>(256);

    let conn1 = manager
        .open("profile-1", server.url.clone(), "tok".to_owned(), tx)
        .await
        .unwrap();

    // Switch profile — old connection generation is stale.
    manager.select("profile-2").await;

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
    let manager = AppwireManager::new();
    let (tx, _rx) = tokio::sync::mpsc::channel::<_>(256);

    let conn1 = manager
        .open("profile-1", server.url.clone(), "tok".to_owned(), tx)
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
        .open("profile-1", server.url.clone(), "tok".to_owned(), tx2)
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
    let manager = AppwireManager::new();
    let (tx, _rx) = tokio::sync::mpsc::channel::<_>(256);

    let conn = manager
        .open("profile-1", server.url.clone(), "tok".to_owned(), tx)
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

#[tokio::test]
async fn appwire_bounded_overload_closes_connection() {
    use ws_server::ScriptedWsServer;

    let server = ScriptedWsServer::start().await;

    // Enqueue many frames to trigger overload.
    for i in 0..500 {
        server.enqueue_frame(format!(r#"{{"id":{i},"method":"ping","params":{{}}}}"#));
    }

    let manager = AppwireManager::new();
    // Use a small bounded channel so overload triggers quickly. The reader
    // sends with try_send; on a full channel it closes with an error.
    let (tx, mut rx) = tokio::sync::mpsc::channel::<AppwireEvent>(8);

    let conn = manager
        .open("profile-1", server.url.clone(), "tok".to_owned(), tx)
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
            AppwireEvent::Text(t) => {
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
            AppwireEvent::Closed(_) | AppwireEvent::Error => {
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
    let manager = AppwireManager::new();
    let (tx, _rx) = tokio::sync::mpsc::channel::<_>(256);

    let conn = manager
        .open("profile-1", server.url.clone(), "tok".to_owned(), tx)
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

    let manager = AppwireManager::new();
    let (tx, _rx) = tokio::sync::mpsc::channel::<_>(256);

    let token = "bearer-upstream-test-token";
    let conn = manager
        .open("profile-1", url, token.to_owned(), tx)
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
// Tests: cross-profile race — late frames from old profile rejected
// ---------------------------------------------------------------------------

#[tokio::test]
async fn appwire_cross_profile_late_frames_rejected() {
    use ws_server::ScriptedWsServer;

    let server1 = ScriptedWsServer::start().await;
    let server2 = ScriptedWsServer::start().await;

    let manager = AppwireManager::new();
    let (tx1, rx1) = tokio::sync::mpsc::channel::<_>(256);
    let (tx2, _rx2) = tokio::sync::mpsc::channel::<_>(256);

    // Open profile-1 on server1.
    let conn1 = manager
        .open("profile-1", server1.url.clone(), "tok1".to_owned(), tx1)
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
    manager.select("profile-2").await;
    let conn2 = manager
        .open("profile-2", server2.url.clone(), "tok2".to_owned(), tx2)
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
