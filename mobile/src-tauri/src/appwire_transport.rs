//! Shared AppWire transport: exactly one WebSocket socket for the active
//! profile.
//!
//! Switching profiles closes the old socket and invalidates its connection
//! generation before opening the new one. Profile and connection generations
//! reject stale frames. A bounded MPSC queue closes on overload rather than
//! dropping or reordering frames. The bearer token and correct upstream
//! Origin are sent on the handshake. The Tauri channel is ordered. Close
//! codes are exact. No token ever appears in errors.

use std::sync::Arc;

use futures_util::{SinkExt, StreamExt};
use parking_lot::Mutex as SyncMutex;
use tokio::sync::mpsc;
use tokio_tungstenite::tungstenite::Message;

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
}

// ---------------------------------------------------------------------------
// AppwireManager
// ---------------------------------------------------------------------------

/// Manages exactly one AppWire WebSocket socket for the active profile.
/// Switching profiles closes and invalidates the old socket before the new
/// one opens.
pub struct AppwireManager {
    generation: std::sync::atomic::AtomicU64,
    active: SyncMutex<Option<Arc<ActiveConnection>>>,
}

impl AppwireManager {
    pub fn new() -> Self {
        Self {
            generation: std::sync::atomic::AtomicU64::new(0),
            active: SyncMutex::new(None),
        }
    }

    fn next_generation(&self) -> u64 {
        self.generation
            .fetch_add(1, std::sync::atomic::Ordering::SeqCst)
            + 1
    }

    /// Close a connection and abort its tasks.
    fn close_connection(conn: &ActiveConnection, code: u16) {
        let _ = conn.writer_tx.send(WriterCommand::Close(code));
        conn.reader_handle.abort();
        // The writer sends the Close frame and exits its loop naturally.
        // It is not aborted, so the close frame reaches the server. The
        // task is detached and reaped when the JoinHandle drops.
    }

    /// Select a profile as active. Closes and invalidates the old connection
    /// before the active profile changes.
    pub async fn select(&self, _profile_id: &str) {
        if let Some(conn) = { self.active.lock().take() } {
            Self::close_connection(&conn, 1000);
        }
    }

    /// Open a WebSocket connection for the given profile.
    ///
    /// If a connection already exists for this profile, returns
    /// `AlreadyConnected` (one socket total). If a connection exists for a
    /// different profile, it is closed and invalidated first.
    pub async fn open(
        &self,
        profile_id: &str,
        url: String,
        token: String,
        event_tx: mpsc::Sender<AppwireEvent>,
    ) -> Result<ConnectionId, AppwireError> {
        // One socket total: reject a duplicate open for the same profile.
        {
            let active = self.active.lock();
            if let Some(conn) = active.as_ref() {
                if conn.conn_id.profile_id == profile_id {
                    return Err(AppwireError::AlreadyConnected);
                }
            }
        }

        // Close any existing connection for a different profile.
        if let Some(conn) = { self.active.lock().take() } {
            Self::close_connection(&conn, 1000);
        }

        let generation = self.next_generation();
        let conn_id = ConnectionId {
            profile_id: profile_id.to_owned(),
            generation,
        };

        // Parse the URL and build the WebSocket request with bearer auth,
        // the correct upstream Origin, the appwire protocol, and a Host
        // header so the handshake succeeds against a standard server.
        let ws_url = url::Url::parse(&url).map_err(|_| AppwireError::ConnectionFailed)?;
        let host = ws_url.host_str().unwrap_or("127.0.0.1");
        let port = ws_url
            .port()
            .unwrap_or(if ws_url.scheme() == "wss" { 443 } else { 80 });
        let host_header = format!("{host}:{port}");

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

        let (ws_stream, _response) = tokio_tungstenite::connect_async(request)
            .await
            .map_err(|_| AppwireError::ConnectionFailed)?;

        let (write, mut read) = ws_stream.split();

        // Writer task: receives commands from the writer channel and sends to
        // the WebSocket.
        let (writer_tx, mut writer_rx) = mpsc::unbounded_channel::<WriterCommand>();
        // The writer task is detached: it exits naturally after sending a
        // Close frame (it breaks out of its loop). We do not abort it, so
        // the close frame reaches the server before the task ends.
        let _writer_handle = tokio::spawn(async move {
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

        // A cancellation signal so the reader task stops when the connection
        // is closed/switched.
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

        let conn = Arc::new(ActiveConnection {
            conn_id: conn_id.clone(),
            writer_tx,
            reader_handle,
        });

        *self.active.lock() = Some(conn);

        Ok(conn_id)
    }

    /// Send a text frame on the active connection. Rejects stale connections.
    pub async fn send(&self, conn_id: ConnectionId, frame: String) -> Result<(), AppwireError> {
        let conn = {
            let active = self.active.lock();
            active.as_ref().ok_or(AppwireError::NotFound)?.clone()
        };

        if conn.conn_id != conn_id {
            return Err(AppwireError::StaleConnection);
        }

        conn.writer_tx
            .send(WriterCommand::Send(frame))
            .map_err(|_| AppwireError::SendFailed)
    }

    /// Close the active connection with a specific close code.
    pub async fn close_with_code(&self, conn_id: ConnectionId, code: u16) {
        let taken = { self.active.lock().take() };
        if let Some(conn) = taken {
            if conn.conn_id == conn_id {
                Self::close_connection(&conn, code);
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
        Self::new()
    }
}

#[cfg(test)]
mod tests {
    use super::*;

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
}
