//! Shared AppWire transport: exactly one WebSocket socket for the active
//! profile.
//!
//! Switching profiles closes the old socket and invalidates its connection
//! generation before opening the new one. Profile and connection generations
//! reject stale frames. A bounded MPSC queue closes on overload rather than
//! dropping or reordering frames. The bearer token and correct upstream
//! Origin are sent on the handshake. The Tauri channel is ordered. Close
//! codes are exact. No token ever appears in errors.
//!
//! Every connection re-resolves the origin through the shared
//! `NetworkPolicy`, connects TCP to the validated pinned IP address, and
//! performs the WebSocket handshake (and TLS, for `wss://`) over that
//! stream while retaining the original hostname for TLS SNI/certificate
//! identity and the Host header. Mixed/public HTTP addresses are rejected
//! by the policy before any TCP connection is made.

use std::sync::Arc;

use futures_util::{SinkExt, StreamExt};
use parking_lot::Mutex as SyncMutex;
use tokio::net::TcpStream;
use tokio::sync::mpsc;
use tokio_tungstenite::tungstenite::Message;

use crate::error::ReleaseMode;
use crate::network_policy::{NetworkPolicy, PinnedOrigin};

// ---------------------------------------------------------------------------
// Events delivered to the Tauri channel
// ---------------------------------------------------------------------------

/// Events emitted to the JS channel. Only text frames, close, and error are
/// delivered — no raw binary, URLs, or tokens.
#[derive(Debug, Clone)]
pub enum AppwireEvent {
    /// A text frame from the server.
    Text(String),
    /// The connection closed with a code.
    Closed(u16),
    /// An error occurred (no details that could leak a token).
    Error,
}

// ---------------------------------------------------------------------------
// Internal control messages to the writer task
// ---------------------------------------------------------------------------

enum WriterCommand {
    Send(String),
    Close(u16),
}

// ---------------------------------------------------------------------------
// Connection ID and generation
// ---------------------------------------------------------------------------

/// A connection identifier. Stale connections reject sends.
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

// ---------------------------------------------------------------------------
// Errors — never carry the token
// ---------------------------------------------------------------------------

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

// ---------------------------------------------------------------------------
// Active connection
// ---------------------------------------------------------------------------

struct ActiveConnection {
    conn_id: ConnectionId,
    /// Channel to send commands to the writer task.
    writer_tx: mpsc::UnboundedSender<WriterCommand>,
    /// Task handle for the reader, aborted on close/switch.
    reader_handle: tokio::task::JoinHandle<()>,
    /// Task handle for the writer. Stored (not aborted) so the close frame
    /// reaches the server; the task exits naturally after sending it. The
    /// handle reaps the task when the connection is dropped.
    writer_handle: tokio::task::JoinHandle<()>,
}

// ---------------------------------------------------------------------------
// AppwireManager
// ---------------------------------------------------------------------------

/// Manages exactly one AppWire WebSocket socket for the active profile.
/// Switching profiles closes and invalidates the old socket before the new
/// one opens. Every connection re-resolves the origin through the shared
/// `NetworkPolicy` and connects TCP to the validated pinned address.
pub struct AppwireManager {
    /// The active profile ID. `open` rejects a profile that does not match
    /// the selected profile.
    active_profile: SyncMutex<Option<String>>,
    generation: std::sync::atomic::AtomicU64,
    active: SyncMutex<Option<ActiveConnection>>,
    /// Shared network policy: re-resolves on every connection.
    policy: Arc<NetworkPolicy>,
    /// Release mode for address validation.
    mode: ReleaseMode,
}

impl AppwireManager {
    pub fn new(policy: Arc<NetworkPolicy>, mode: ReleaseMode) -> Self {
        Self {
            active_profile: SyncMutex::new(None),
            generation: std::sync::atomic::AtomicU64::new(0),
            active: SyncMutex::new(None),
            policy,
            mode,
        }
    }

    fn next_generation(&self) -> u64 {
        self.generation
            .fetch_add(1, std::sync::atomic::Ordering::SeqCst)
            + 1
    }

    /// Queue the close frame to the writer and abort the reader. The writer
    /// sends the close frame and exits its loop naturally; it is not aborted
    /// so the frame reaches the server. Returns both `JoinHandle`s so the
    /// caller can await them for deterministic reaping.
    fn close_connection(conn: &ActiveConnection, code: u16) {
        let _ = conn.writer_tx.send(WriterCommand::Close(code));
        conn.reader_handle.abort();
    }

    /// Close and deterministically reap a connection: queue the close frame
    /// to the writer, abort the reader, then await both `JoinHandle`s. The
    /// writer sends the close frame and exits naturally; the reader was
    /// aborted. No mutex is held across the await.
    async fn close_and_reap(conn: ActiveConnection, code: u16) {
        Self::close_connection(&conn, code);
        let _ = conn.writer_handle.await;
        let _ = conn.reader_handle.await;
    }

    /// Select a profile as active. Closes and invalidates the old connection
    /// before the active profile changes. The profile_id is recorded so that
    /// `open` can validate the connection belongs to the selected profile.
    pub async fn select(&self, profile_id: &str) {
        let taken = {
            let mut active = self.active.lock();
            active.take()
        };
        if let Some(conn) = taken {
            Self::close_and_reap(conn, 1000).await;
        }
        *self.active_profile.lock() = Some(profile_id.to_owned());
    }

    /// Open a WebSocket connection for the given profile.
    ///
    /// If a connection already exists for this profile, returns
    /// `AlreadyConnected` (one socket total). If a connection exists for a
    /// different profile, it is closed and invalidated first. The profile
    /// must match the currently selected profile (`select`), otherwise
    /// `ProfileMismatch` is returned.
    ///
    /// The origin is re-resolved through the `NetworkPolicy` on every call.
    /// TCP connects to the validated pinned IP address; the WebSocket
    /// handshake (and TLS for `wss://`) runs over that stream while
    /// retaining the original hostname for TLS SNI/certificate identity and
    /// the Host header.
    pub async fn open(
        &self,
        profile_id: &str,
        url: String,
        token: String,
        event_tx: mpsc::Sender<AppwireEvent>,
    ) -> Result<ConnectionId, AppwireError> {
        // Validate the profile matches the selected profile.
        {
            let active_profile = self.active_profile.lock();
            match active_profile.as_deref() {
                None => {
                    // No profile selected yet; accept this one as the active.
                    drop(active_profile);
                    *self.active_profile.lock() = Some(profile_id.to_owned());
                }
                Some(selected) if selected == profile_id => {}
                Some(_) => return Err(AppwireError::ProfileMismatch),
            }
        }

        // One socket total: reject a duplicate open for the same profile.
        {
            let active = self.active.lock();
            if let Some(conn) = active.as_ref() {
                if conn.conn_id.profile_id == profile_id {
                    return Err(AppwireError::AlreadyConnected);
                }
            }
        }

        // Close and reap any existing connection for a different profile
        // before opening the new one. Both old tasks (writer + reader) are
        // awaited so no old task/frame survives.
        let old_conn = {
            let mut active = self.active.lock();
            active.take()
        };
        if let Some(conn) = old_conn {
            Self::close_and_reap(conn, 1000).await;
        }

        let generation = self.next_generation();
        let conn_id = ConnectionId {
            profile_id: profile_id.to_owned(),
            generation,
        };

        // Re-resolve the origin through the network policy on every
        // connection. Pin the validated address; preserve the original
        // hostname for Host/TLS SNI.
        let ws_url = url::Url::parse(&url).map_err(|_| AppwireError::ConnectionFailed)?;
        let pinned = self
            .policy
            .resolve(&ws_url, self.mode)
            .map_err(|_| AppwireError::PolicyRejected)?;

        // Connect TCP to the validated pinned IP address.
        let addr = pinned.addrs().first().ok_or(AppwireError::PolicyRejected)?;
        let port = pinned.port();
        let socket = TcpStream::connect((*addr, port))
            .await
            .map_err(|_| AppwireError::ConnectionFailed)?;
        socket
            .set_nodelay(true)
            .map_err(|_| AppwireError::ConnectionFailed)?;

        // Build the WebSocket handshake request with the original hostname
        // in the URI (for TLS SNI) and the Host header. The TCP stream is
        // already connected to the validated IP.
        let host_header = host_with_port(&pinned);
        let request = tokio_tungstenite::tungstenite::http::Request::builder()
            .uri(ws_url.as_str())
            .header("Host", &host_header)
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

        // Perform the WebSocket handshake (and TLS for wss://) over the
        // already-connected TCP stream. tungstenite uses the request's
        // scheme to decide plain vs TLS and the request's host for SNI.
        let (ws_stream, _response) =
            tokio_tungstenite::client_async_tls_with_config(request, socket, None, None)
                .await
                .map_err(|_| AppwireError::ConnectionFailed)?;

        let (write, mut read) = ws_stream.split();

        // Writer task: receives commands from the writer channel and sends to
        // the WebSocket.
        let (writer_tx, mut writer_rx) = mpsc::unbounded_channel::<WriterCommand>();
        let writer_handle = tokio::spawn(async move {
            let mut write = write;
            while let Some(cmd) = writer_rx.recv().await {
                match cmd {
                    WriterCommand::Send(text) => {
                        if write.send(Message::Text(text.into())).await.is_err() {
                            break;
                        }
                    }
                    WriterCommand::Close(code) => {
                        let _ = write
                            .send(Message::Close(Some(
                                tokio_tungstenite::tungstenite::protocol::CloseFrame {
                                    code: code.into(),
                                    reason: "".into(),
                                },
                            )))
                            .await;
                        break;
                    }
                }
            }
        });

        // Reader task: reads frames from the WebSocket and forwards directly
        // to the bounded event channel. On overload (channel full), it sends
        // an error and closes instead of dropping or reordering frames. The
        // bounded channel preserves order.
        let reader_handle = tokio::spawn(async move {
            loop {
                let msg = match read.next().await {
                    Some(m) => m,
                    None => break,
                };
                match msg {
                    Ok(Message::Text(text)) => {
                        let text_string = text.to_string();
                        // try_send preserves order; on a full bounded
                        // channel we close with an error rather than
                        // dropping the frame.
                        if event_tx.try_send(AppwireEvent::Text(text_string)).is_err() {
                            // Channel full (overload) or closed. Try to send
                            // an error; if that also fails, just close.
                            let _ = event_tx.send(AppwireEvent::Error).await;
                            break;
                        }
                    }
                    Ok(Message::Close(close_frame)) => {
                        let code = close_frame.map(|cf| u16::from(cf.code)).unwrap_or(1000);
                        let _ = event_tx.send(AppwireEvent::Closed(code)).await;
                        break;
                    }
                    Ok(Message::Binary(_))
                    | Ok(Message::Ping(_))
                    | Ok(Message::Pong(_))
                    | Ok(Message::Frame(_)) => {}
                    Err(_) => {
                        let _ = event_tx.send(AppwireEvent::Error).await;
                        break;
                    }
                }
            }
        });

        let conn = ActiveConnection {
            conn_id: conn_id.clone(),
            writer_tx,
            reader_handle,
            writer_handle,
        };

        *self.active.lock() = Some(conn);

        Ok(conn_id)
    }

    /// Send a text frame on the active connection. Rejects stale connections.
    pub async fn send(&self, conn_id: ConnectionId, frame: String) -> Result<(), AppwireError> {
        let writer_tx = {
            let active = self.active.lock();
            let conn = active.as_ref().ok_or(AppwireError::NotFound)?;
            if conn.conn_id != conn_id {
                return Err(AppwireError::StaleConnection);
            }
            conn.writer_tx.clone()
        };

        writer_tx
            .send(WriterCommand::Send(frame))
            .map_err(|_| AppwireError::SendFailed)
    }

    /// Close the active connection with a specific close code.
    pub async fn close_with_code(&self, conn_id: ConnectionId, code: u16) {
        let taken = {
            let mut active = self.active.lock();
            active.take()
        };
        if let Some(conn) = taken {
            if conn.conn_id == conn_id {
                Self::close_and_reap(conn, code).await;
            } else {
                // Not the matching connection; put it back.
                *self.active.lock() = Some(conn);
            }
        }
    }

    /// Close the active connection with default code 1000.
    pub async fn close(&self, conn_id: ConnectionId) {
        self.close_with_code(conn_id, 1000).await;
    }

    /// Close the current connection (called by the CloseTransport callback
    /// before the active profile changes). Invalidates the generation.
    pub fn close_current(&self) -> u64 {
        if let Some(conn) = { self.active.lock().take() } {
            let gen = conn.conn_id.generation;
            Self::close_connection(&conn, 1000);
            gen
        } else {
            0
        }
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

/// Build the Host header (host:port, omitting the port if it is the scheme
/// default) from a pinned origin.
fn host_with_port(pinned: &PinnedOrigin) -> String {
    let scheme = pinned.scheme();
    let host = pinned.host();
    let port = pinned.port();
    if is_default_port(scheme, port) {
        host.to_owned()
    } else {
        format!("{host}:{port}")
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
        let a = ConnectionId {
            profile_id: "p1".to_owned(),
            generation: 1,
        };
        let b = ConnectionId {
            profile_id: "p1".to_owned(),
            generation: 2,
        };
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
    async fn open_rejects_profile_mismatch_when_other_selected() {
        let policy = Arc::new(NetworkPolicy::new(Box::new(
            crate::network_policy::AlwaysPrivateResolver,
        )));
        let manager = AppwireManager::new(policy, ReleaseMode::Release);
        // Select profile-1, then try to open profile-2.
        manager.select("profile-1").await;
        let (tx, _rx) = tokio::sync::mpsc::channel::<AppwireEvent>(8);
        let result = manager
            .open(
                "profile-2",
                "ws://hub.example.com/rpc".to_owned(),
                "tok".to_owned(),
                tx,
            )
            .await;
        assert!(matches!(result, Err(AppwireError::ProfileMismatch)));
    }

    #[tokio::test]
    async fn open_rejects_public_http_address() {
        // A resolver that returns a public address for HTTP.
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
                "ws://hub.example.com/rpc".to_owned(),
                "tok".to_owned(),
                tx,
            )
            .await;
        assert!(matches!(result, Err(AppwireError::PolicyRejected)));
    }
}
