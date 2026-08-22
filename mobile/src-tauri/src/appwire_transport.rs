//! Shared AppWire transport: exactly one WebSocket socket for the selected
//! profile.
//!
//! All manager lifecycle operations serialize through one async mutex. A
//! connection is owned by one supervisor task, so close, profile switch, and
//! terminal reader outcomes have one deterministic cleanup path. Reserved queue
//! permits make terminal delivery nonblocking. Active state remains owned until
//! a reaper has awaited the supervisor and observed socket-half drop.

use std::sync::atomic::{AtomicBool, Ordering};
use std::sync::Arc;

use futures_util::{SinkExt, StreamExt};
use parking_lot::Mutex as SyncMutex;
use tokio::net::TcpStream;
use tokio::sync::{mpsc, oneshot};
use tokio_tungstenite::tungstenite::Message;

use crate::error::ReleaseMode;
use crate::network_policy::{NetworkPolicy, PinnedOrigin};

/// A connection identifier. Profile and connection generation reject stale
/// commands and events.
#[derive(Debug, Clone, PartialEq, Eq)]
pub struct ConnectionId {
    profile_id: String,
    generation: u64,
}

impl ConnectionId {
    pub fn profile_id(&self) -> &str {
        &self.profile_id
    }

    pub fn generation(&self) -> u64 {
        self.generation
    }

    pub fn new(profile_id: String, generation: u64) -> Self {
        Self {
            profile_id,
            generation,
        }
    }
}

/// Events delivered to JavaScript. Every variant carries the complete routing
/// identity, allowing both Rust and TypeScript to reject stale queued events.
#[derive(Debug, Clone, PartialEq, Eq)]
pub enum AppwireEvent {
    Text {
        connection_id: ConnectionId,
        data: String,
    },
    Closed {
        connection_id: ConnectionId,
        code: u16,
        reason: String,
    },
    Error {
        connection_id: ConnectionId,
    },
}

#[derive(Debug, thiserror::Error)]
pub enum AppwireError {
    #[error("stale connection")]
    StaleConnection,
    #[error("connection not found")]
    NotFound,
    #[error("connection failed")]
    ConnectionFailed,
    #[error("send failed")]
    SendFailed,
    #[error("already connected")]
    AlreadyConnected,
    #[error("profile mismatch")]
    ProfileMismatch,
    #[error("network policy rejected origin")]
    PolicyRejected,
}

enum WriterCommand {
    Send(String),
    Close { code: u16, reason: String },
}

#[derive(Clone)]
struct ActiveConnection {
    conn_id: ConnectionId,
    command_tx: mpsc::UnboundedSender<WriterCommand>,
    lifecycle: Arc<ConnectionLifecycle>,
}

#[derive(Default)]
struct ConnectionLifecycle {
    terminal_started: AtomicBool,
    terminal_started_notify: tokio::sync::Notify,
    completed: AtomicBool,
    completed_notify: tokio::sync::Notify,
}

impl ConnectionLifecycle {
    fn mark_terminal_started(&self) {
        self.terminal_started.store(true, Ordering::SeqCst);
        self.terminal_started_notify.notify_waiters();
    }

    async fn wait_terminal_started(&self) {
        loop {
            let notified = self.terminal_started_notify.notified();
            if self.terminal_started.load(Ordering::SeqCst) {
                return;
            }
            notified.await;
        }
    }

    fn mark_completed(&self) {
        self.completed.store(true, Ordering::SeqCst);
        self.completed_notify.notify_waiters();
    }

    async fn wait_completed(&self) {
        loop {
            let notified = self.completed_notify.notified();
            if self.completed.load(Ordering::SeqCst) {
                return;
            }
            notified.await;
        }
    }
}

#[derive(Debug, Clone, PartialEq, Eq)]
struct SelectedProfile {
    profile_id: String,
    profile_generation: u64,
}

#[derive(Default)]
struct ManagerState {
    selected: Option<SelectedProfile>,
    active: Option<ActiveConnection>,
}

/// Manages exactly one AppWire socket. `profile_select` and active-profile
/// removal call [`select`] while holding the profile lifecycle lock, before
/// preferences change. Opening and closing serialize with those transitions.
pub struct AppwireManager {
    generation: std::sync::atomic::AtomicU64,
    reaped_connections: Arc<std::sync::atomic::AtomicU64>,
    state: Arc<SyncMutex<ManagerState>>,
    lifecycle: tokio::sync::Mutex<()>,
    policy: Arc<NetworkPolicy>,
    mode: ReleaseMode,
}

impl AppwireManager {
    pub fn new(policy: Arc<NetworkPolicy>, mode: ReleaseMode) -> Self {
        Self {
            generation: std::sync::atomic::AtomicU64::new(0),
            reaped_connections: Arc::new(std::sync::atomic::AtomicU64::new(0)),
            state: Arc::new(SyncMutex::new(ManagerState::default())),
            lifecycle: tokio::sync::Mutex::new(()),
            policy,
            mode,
        }
    }

    fn next_generation(&self) -> u64 {
        self.generation
            .fetch_add(1, std::sync::atomic::Ordering::SeqCst)
            + 1
    }

    /// Selected profile identity as understood by the transport manager.
    pub fn selected_profile(&self) -> Option<(String, u64)> {
        self.state
            .lock()
            .selected
            .as_ref()
            .map(|selected| (selected.profile_id.clone(), selected.profile_generation))
    }

    /// Number of socket supervisors whose JoinHandle has completed and been
    /// awaited by the manager reaper.
    pub fn reaped_connection_count(&self) -> u64 {
        self.reaped_connections.load(Ordering::SeqCst)
    }

    /// Whether the given connection still owns the one active socket.
    pub fn is_active(&self, conn_id: &ConnectionId) -> bool {
        self.state
            .lock()
            .active
            .as_ref()
            .is_some_and(|active| active.conn_id == *conn_id)
    }

    /// Await the exact point at which a connection has selected a terminal
    /// outcome. Primarily useful to coordinate lifecycle observers without
    /// polling; completion and active-state removal happen later, after socket
    /// drop and supervisor reaping.
    pub async fn wait_for_terminal_start(&self, conn_id: &ConnectionId) -> bool {
        let lifecycle = {
            let state = self.state.lock();
            state
                .active
                .as_ref()
                .filter(|active| active.conn_id == *conn_id)
                .map(|active| active.lifecycle.clone())
        };
        let Some(lifecycle) = lifecycle else {
            return false;
        };
        lifecycle.wait_terminal_started().await;
        true
    }

    /// Close and reap the old connection before changing the manager's selected
    /// profile identity. `None` represents no selected profile.
    pub async fn select(&self, profile_id: Option<&str>, profile_generation: u64) {
        let _lifecycle = self.lifecycle.lock().await;
        let old = self.active_control();
        if let Some(old) = old {
            Self::close_and_wait(old, 1000, "profile selection changed").await;
        }
        self.state.lock().selected = profile_id.map(|profile_id| SelectedProfile {
            profile_id: profile_id.to_owned(),
            profile_generation,
        });
    }

    /// Reconcile selection after a persistence transition succeeds or rolls
    /// back. The transition already reaped the socket, so this never creates a
    /// duplicate close path.
    pub async fn reconcile_selection(&self, profile_id: Option<&str>, profile_generation: u64) {
        let _lifecycle = self.lifecycle.lock().await;
        debug_assert!(self.state.lock().active.is_none());
        self.state.lock().selected = profile_id.map(|profile_id| SelectedProfile {
            profile_id: profile_id.to_owned(),
            profile_generation,
        });
    }

    /// Open a connection from one immutable active-profile snapshot.
    pub async fn open(
        &self,
        profile_id: &str,
        profile_generation: u64,
        url: String,
        token: String,
        event_tx: mpsc::Sender<AppwireEvent>,
    ) -> Result<ConnectionId, AppwireError> {
        let _lifecycle = self.lifecycle.lock().await;

        {
            let mut state = self.state.lock();
            match state.selected.as_ref() {
                None => {
                    state.selected = Some(SelectedProfile {
                        profile_id: profile_id.to_owned(),
                        profile_generation,
                    });
                }
                Some(selected)
                    if selected.profile_id == profile_id
                        && selected.profile_generation == profile_generation => {}
                Some(_) => return Err(AppwireError::ProfileMismatch),
            }
        }

        // A terminal supervisor retains active ownership until its reaper has
        // observed task exit and socket-half drop. A reconnect waits for that
        // completion; a duplicate open against a healthy socket still fails.
        loop {
            let existing = self.active_control();
            let Some(existing) = existing else {
                break;
            };
            if existing.conn_id.profile_id == profile_id
                && !existing.lifecycle.terminal_started.load(Ordering::SeqCst)
            {
                return Err(AppwireError::AlreadyConnected);
            }
            if existing.lifecycle.terminal_started.load(Ordering::SeqCst) {
                existing.lifecycle.wait_completed().await;
            } else {
                Self::close_and_wait(existing, 1000, "connection replaced").await;
            }
        }

        // Permanently reserve two queue slots for terminal Error+Closed. Text
        // frames can fill only the nonterminal capacity, so terminal delivery
        // is nonblocking even when that backlog is saturated.
        let error_permit = event_tx
            .clone()
            .try_reserve_owned()
            .map_err(|_| AppwireError::ConnectionFailed)?;
        let closed_permit = event_tx
            .clone()
            .try_reserve_owned()
            .map_err(|_| AppwireError::ConnectionFailed)?;

        let generation = self.next_generation();
        let conn_id = ConnectionId::new(profile_id.to_owned(), generation);
        let ws_url = url::Url::parse(&url).map_err(|_| AppwireError::ConnectionFailed)?;
        let pinned = self
            .policy
            .resolve(&ws_url, self.mode)
            .map_err(|_| AppwireError::PolicyRejected)?;
        let addr = pinned.addrs().first().ok_or(AppwireError::PolicyRejected)?;
        let socket = TcpStream::connect((*addr, pinned.port()))
            .await
            .map_err(|_| AppwireError::ConnectionFailed)?;
        socket
            .set_nodelay(true)
            .map_err(|_| AppwireError::ConnectionFailed)?;

        let request = tokio_tungstenite::tungstenite::http::Request::builder()
            .uri(ws_url.as_str())
            .header("Host", host_with_port(&pinned))
            .header("Authorization", format!("Bearer {token}"))
            .header("Origin", ws_url.origin().ascii_serialization())
            .header("Sec-WebSocket-Protocol", "evener-appwire-v3")
            .header("Sec-WebSocket-Version", "13")
            .header("Connection", "Upgrade")
            .header("Upgrade", "websocket")
            .header(
                "Sec-WebSocket-Key",
                tokio_tungstenite::tungstenite::handshake::client::generate_key(),
            )
            .body(())
            .map_err(|_| AppwireError::ConnectionFailed)?;

        let (ws_stream, _) =
            tokio_tungstenite::client_async_tls_with_config(request, socket, None, None)
                .await
                .map_err(|_| AppwireError::ConnectionFailed)?;
        let (mut write, mut read) = ws_stream.split();
        let (command_tx, mut command_rx) = mpsc::unbounded_channel();
        let (start_tx, start_rx) = oneshot::channel();
        let task_conn_id = conn_id.clone();
        let connection_lifecycle = Arc::new(ConnectionLifecycle::default());
        let supervisor_lifecycle = connection_lifecycle.clone();

        let supervisor_handle = tokio::spawn(async move {
            // The start gate prevents a terminal server frame from racing the
            // manager's insertion of this connection.
            if start_rx.await.is_err() {
                return;
            }

            enum Terminal {
                Closed { code: u16, reason: String },
                Error { code: u16, reason: String },
                LocalClose { code: u16, reason: String },
            }

            let terminal = loop {
                tokio::select! {
                    command = command_rx.recv() => {
                        match command {
                            Some(WriterCommand::Send(text)) => {
                                if write.send(Message::Text(text.into())).await.is_err() {
                                    break Terminal::Error {
                                        code: 1006,
                                        reason: "transport send failed".to_owned(),
                                    };
                                }
                            }
                            Some(WriterCommand::Close { code, reason }) => {
                                let frame = tokio_tungstenite::tungstenite::protocol::CloseFrame {
                                    code: code.into(),
                                    reason: reason.clone().into(),
                                };
                                // Give the close frame one nonblocking poll. A
                                // peer that stops reading must not retain socket
                                // ownership or block lifecycle completion.
                                let send = write.send(Message::Close(Some(frame)));
                                tokio::pin!(send);
                                let _ = futures_util::poll!(&mut send);
                                break Terminal::LocalClose { code, reason };
                            }
                            None => {
                                break Terminal::Error {
                                    code: 1006,
                                    reason: "transport command channel closed".to_owned(),
                                };
                            }
                        }
                    }
                    message = read.next() => {
                        match message {
                            Some(Ok(Message::Text(text))) => {
                                let event = AppwireEvent::Text {
                                    connection_id: task_conn_id.clone(),
                                    data: text.to_string(),
                                };
                                if event_tx.try_send(event).is_err() {
                                    break Terminal::Error {
                                        code: 1013,
                                        reason: "event queue overloaded".to_owned(),
                                    };
                                }
                            }
                            Some(Ok(Message::Close(frame))) => {
                                let (code, reason) = frame
                                    .map(|frame| (u16::from(frame.code), frame.reason.to_string()))
                                    .unwrap_or((1005, String::new()));
                                break Terminal::Closed { code, reason };
                            }
                            Some(Ok(Message::Binary(_)
                                | Message::Ping(_)
                                | Message::Pong(_)
                                | Message::Frame(_))) => {}
                            Some(Err(_)) => {
                                break Terminal::Error {
                                    code: 1006,
                                    reason: "transport read failed".to_owned(),
                                };
                            }
                            None => {
                                break Terminal::Closed {
                                    code: 1006,
                                    reason: "connection ended without a close frame".to_owned(),
                                };
                            }
                        }
                    }
                }
            };

            supervisor_lifecycle.mark_terminal_started();

            // Best-effort one nonblocking close poll, then force socket-half
            // drop before publishing terminal outcome. Awaiting a WebSocket
            // flush here can depend indefinitely on peer cooperation, while
            // every reconnect/profile lifecycle operation waits for this
            // supervisor to release active ownership.
            {
                let close = write.close();
                tokio::pin!(close);
                let _ = futures_util::poll!(&mut close);
            }
            drop(write);
            drop(read);

            match terminal {
                Terminal::Error { code, reason } => {
                    error_permit.send(AppwireEvent::Error {
                        connection_id: task_conn_id.clone(),
                    });
                    closed_permit.send(AppwireEvent::Closed {
                        connection_id: task_conn_id,
                        code,
                        reason,
                    });
                }
                Terminal::Closed { code, reason } | Terminal::LocalClose { code, reason } => {
                    drop(error_permit);
                    closed_permit.send(AppwireEvent::Closed {
                        connection_id: task_conn_id,
                        code,
                        reason,
                    });
                }
            }
        });

        self.state.lock().active = Some(ActiveConnection {
            conn_id: conn_id.clone(),
            command_tx,
            lifecycle: connection_lifecycle.clone(),
        });
        let state = self.state.clone();
        let reaped_connections = self.reaped_connections.clone();
        let reaped_conn_id = conn_id.clone();
        tokio::spawn(async move {
            let _ = supervisor_handle.await;
            reaped_connections.fetch_add(1, Ordering::SeqCst);
            {
                let mut state = state.lock();
                if state
                    .active
                    .as_ref()
                    .is_some_and(|active| active.conn_id == reaped_conn_id)
                {
                    state.active.take();
                }
            }
            connection_lifecycle.mark_completed();
        });
        // The receiver cannot disappear before the task starts unless the task
        // was externally aborted, which this handle is not yet exposed for.
        start_tx
            .send(())
            .map_err(|_| AppwireError::ConnectionFailed)?;

        Ok(conn_id)
    }

    pub async fn send(&self, conn_id: ConnectionId, frame: String) -> Result<(), AppwireError> {
        let command_tx = {
            let state = self.state.lock();
            let active = state.active.as_ref().ok_or(AppwireError::NotFound)?;
            if active.conn_id != conn_id {
                return Err(AppwireError::StaleConnection);
            }
            if active.lifecycle.terminal_started.load(Ordering::SeqCst) {
                return Err(AppwireError::StaleConnection);
            }
            active.command_tx.clone()
        };
        command_tx
            .send(WriterCommand::Send(frame))
            .map_err(|_| AppwireError::SendFailed)
    }

    pub async fn close_with_code(&self, conn_id: ConnectionId, code: u16) {
        let _lifecycle = self.lifecycle.lock().await;
        let connection = {
            let state = self.state.lock();
            if state
                .active
                .as_ref()
                .is_some_and(|active| active.conn_id == conn_id)
            {
                state.active.clone()
            } else {
                None
            }
        };
        if let Some(connection) = connection {
            Self::close_and_wait(connection, code, "client closed").await;
        }
    }

    pub async fn close(&self, conn_id: ConnectionId) {
        self.close_with_code(conn_id, 1000).await;
    }

    fn active_control(&self) -> Option<ActiveConnection> {
        self.state.lock().active.clone()
    }

    async fn close_and_wait(connection: ActiveConnection, code: u16, reason: &str) {
        let _ = connection.command_tx.send(WriterCommand::Close {
            code,
            reason: reason.to_owned(),
        });
        connection.lifecycle.wait_completed().await;
    }
}

impl Default for AppwireManager {
    fn default() -> Self {
        Self::new(
            Arc::new(NetworkPolicy::new(Box::new(
                crate::network_policy::StaticResolver::new(),
            ))),
            ReleaseMode::Release,
        )
    }
}

fn host_with_port(pinned: &PinnedOrigin) -> String {
    if is_default_port(pinned.scheme(), pinned.port()) {
        pinned.host().to_owned()
    } else {
        format!("{}:{}", pinned.host(), pinned.port())
    }
}

fn is_default_port(scheme: &str, port: u16) -> bool {
    matches!(
        (scheme, port),
        ("http", 80) | ("https", 443) | ("ws", 80) | ("wss", 443)
    )
}

#[cfg(test)]
mod tests {
    use super::*;
    use std::net::IpAddr;

    #[test]
    fn connection_id_is_stale_after_generation_bump() {
        let a = ConnectionId::new("p1".to_owned(), 1);
        let b = ConnectionId::new("p1".to_owned(), 2);
        assert_ne!(a, b);
    }

    #[test]
    fn appwire_error_never_contains_token() {
        let err = AppwireError::ConnectionFailed;
        assert!(!format!("{err}").contains("secret-token"));
        assert!(!format!("{err:?}").contains("secret-token"));
    }

    #[test]
    fn host_with_port_omits_default_port() {
        let policy = Arc::new(NetworkPolicy::new(Box::new(
            crate::network_policy::AlwaysPrivateResolver,
        )));
        let url = url::Url::parse("https://hub.example.com/api").unwrap();
        let pinned = policy.resolve(&url, ReleaseMode::Release).unwrap();
        assert_eq!(host_with_port(&pinned), "hub.example.com");
    }

    #[test]
    fn host_with_port_includes_non_default_port() {
        let policy = Arc::new(NetworkPolicy::new(Box::new(
            crate::network_policy::AlwaysPrivateResolver,
        )));
        let url = url::Url::parse("https://hub.example.com:8443/api").unwrap();
        let pinned = policy.resolve(&url, ReleaseMode::Release).unwrap();
        assert_eq!(host_with_port(&pinned), "hub.example.com:8443");
    }

    #[tokio::test]
    async fn open_rejects_profile_or_profile_generation_mismatch() {
        let policy = Arc::new(NetworkPolicy::new(Box::new(
            crate::network_policy::AlwaysPrivateResolver,
        )));
        let manager = AppwireManager::new(policy, ReleaseMode::Release);
        manager.select(Some("profile-1"), 7).await;
        let (tx, _rx) = tokio::sync::mpsc::channel::<AppwireEvent>(8);
        let wrong_profile = manager
            .open(
                "profile-2",
                7,
                "ws://hub.example.com/rpc".to_owned(),
                "tok".to_owned(),
                tx.clone(),
            )
            .await;
        assert!(matches!(wrong_profile, Err(AppwireError::ProfileMismatch)));
        let wrong_generation = manager
            .open(
                "profile-1",
                8,
                "ws://hub.example.com/rpc".to_owned(),
                "tok".to_owned(),
                tx,
            )
            .await;
        assert!(matches!(
            wrong_generation,
            Err(AppwireError::ProfileMismatch)
        ));
    }

    #[tokio::test]
    async fn open_rejects_public_http_address() {
        struct PublicResolver;
        impl crate::network_policy::DnsResolver for PublicResolver {
            fn resolve(&self, _host: &str) -> Result<Vec<IpAddr>, String> {
                Ok(vec![IpAddr::V4(std::net::Ipv4Addr::new(203, 0, 113, 10))])
            }
        }
        let policy = Arc::new(NetworkPolicy::new(Box::new(PublicResolver)));
        let manager = AppwireManager::new(policy, ReleaseMode::Release);
        let (tx, _rx) = tokio::sync::mpsc::channel::<AppwireEvent>(8);
        let result = manager
            .open(
                "profile-1",
                0,
                "ws://hub.example.com/rpc".to_owned(),
                "tok".to_owned(),
                tx,
            )
            .await;
        assert!(matches!(result, Err(AppwireError::PolicyRejected)));
    }
}
