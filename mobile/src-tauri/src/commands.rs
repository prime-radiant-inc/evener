//! Typed Tauri commands for profile lifecycle, HTTP, and AppWire transport.
//!
//! All profile lifecycle commands serialize through the `ProfileRuntime`'s
//! async lifecycle mutex. The mutex is held only around the brief state
//! transition (origin/generation lookup, token retrieval setup) — never
//! across a network await. JavaScript receives no capability, raw QR text, or
//! native path — only redacted `{id,name,origin}` summaries and opaque
//! preview IDs.

use tauri::State;

use crate::appwire_transport::{AppwireEvent, ConnectionId};
use crate::diagnostics::DiagnosticEntry;
use crate::error::ReleaseMode;
use crate::profile_runtime::{
    ConfirmPairingRequest, HealthResponse, PreviewPasteRequest, PreviewRepairRequest,
    PreviewResponse, ProfileRuntime, ProfileSummaryResponse, RemoveRequest, RenameRequest,
    SelectRequest, SelectResponse,
};
use crate::transport_state::TransportState;

// ---------------------------------------------------------------------------
// Profile lifecycle commands — all serialize through the runtime mutex
// ---------------------------------------------------------------------------

#[tauri::command]
pub async fn profile_list(
    runtime: State<'_, ProfileRuntime>,
) -> Result<Vec<ProfileSummaryResponse>, String> {
    runtime
        .serialized(|store| store.list().map_err(|e| e.to_string()))
        .await
        .map(|list| list.into_iter().map(ProfileSummaryResponse::from).collect())
}

#[tauri::command]
pub async fn profile_preview_paste(
    runtime: State<'_, ProfileRuntime>,
    request: PreviewPasteRequest,
) -> Result<PreviewResponse, String> {
    runtime
        .serialized(|store| {
            store
                .preview_pairing(&request.raw)
                .map_err(|e| e.to_string())
        })
        .await
        .map(|p| PreviewResponse {
            preview_id: p.preview_id,
            origin: p.origin,
        })
}

#[tauri::command]
pub async fn profile_preview_repair(
    runtime: State<'_, ProfileRuntime>,
    request: PreviewRepairRequest,
) -> Result<PreviewResponse, String> {
    runtime
        .serialized(|store| {
            store
                .preview_repair(&request.profile_id, &request.raw)
                .map_err(|e| e.to_string())
        })
        .await
        .map(|p| PreviewResponse {
            preview_id: p.preview_id,
            origin: p.origin,
        })
}

#[tauri::command]
pub async fn profile_confirm_pairing(
    runtime: State<'_, ProfileRuntime>,
    request: ConfirmPairingRequest,
) -> Result<ProfileSummaryResponse, String> {
    runtime
        .serialized(|store| {
            store
                .confirm_pairing(
                    &request.preview_id,
                    &request.name,
                    request.allow_duplicate_origin,
                    ReleaseMode::Release,
                )
                .map_err(|e| e.to_string())
        })
        .await
        .map(ProfileSummaryResponse::from)
}

#[tauri::command]
pub async fn profile_rename(
    runtime: State<'_, ProfileRuntime>,
    request: RenameRequest,
) -> Result<ProfileSummaryResponse, String> {
    runtime
        .serialized(|store| {
            store
                .rename(&request.profile_id, &request.new_name)
                .map_err(|e| e.to_string())
        })
        .await
        .map(ProfileSummaryResponse::from)
}

#[tauri::command]
pub async fn profile_remove(
    runtime: State<'_, ProfileRuntime>,
    transport: State<'_, TransportState>,
    request: RemoveRequest,
) -> Result<SelectResponse, String> {
    let result = runtime
        .serialized(|store| store.remove(&request.profile_id).map_err(|e| e.to_string()))
        .await;
    // Clear diagnostics for the removed profile (spec: entries for a removed
    // profile are deleted). Only cleared after a successful removal.
    if result.is_ok() {
        transport
            .diagnostics()
            .clear_for_profile(&request.profile_id);
    }
    result.map(SelectResponse::from)
}

#[tauri::command]
pub async fn profile_select(
    runtime: State<'_, ProfileRuntime>,
    request: SelectRequest,
) -> Result<SelectResponse, String> {
    runtime
        .serialized(|store| store.select(&request.profile_id).map_err(|e| e.to_string()))
        .await
        .map(SelectResponse::from)
}

#[tauri::command]
pub async fn profile_health(runtime: State<'_, ProfileRuntime>) -> Result<HealthResponse, String> {
    runtime
        .serialized(|store| {
            let profiles = store.list().map_err(|e| e.to_string())?;
            let active_id = store.active_id().map_err(|e| e.to_string())?;
            let generation = store.generation();
            Ok::<_, String>(HealthResponse {
                active_profile_id: active_id,
                profiles: profiles
                    .into_iter()
                    .map(ProfileSummaryResponse::from)
                    .collect(),
                generation: generation.0,
            })
        })
        .await
}

// ---------------------------------------------------------------------------
// HTTP transport command
// ---------------------------------------------------------------------------

#[derive(Debug, Clone, serde::Deserialize)]
#[serde(rename_all = "camelCase")]
pub struct HubHttpRequest {
    pub active_profile_id: String,
    pub method: String,
    pub path: String,
    #[serde(default)]
    pub body: Option<serde_json::Value>,
    #[serde(default)]
    pub media_type: Option<String>,
}

/// Redacted HTTP response DTO. Never carries the token, URL, or headers.
#[derive(Debug, Clone, serde::Serialize)]
#[serde(rename_all = "camelCase")]
pub struct HubHttpResponse {
    pub status: u16,
    pub body: serde_json::Value,
}

#[tauri::command]
pub async fn hub_http_request(
    runtime: State<'_, ProfileRuntime>,
    transport: State<'_, TransportState>,
    request: HubHttpRequest,
) -> Result<HubHttpResponse, String> {
    // Brief state transition under the lifecycle mutex: look up the active
    // profile's origin and verify the profile ID matches. The token is
    // retrieved outside the mutex to avoid holding it across Keychain I/O.
    let (origin, generation) = runtime
        .serialized(|store| {
            let profiles = store.list().map_err(|e| e.to_string())?;
            let profile = profiles
                .iter()
                .find(|p| p.id == request.active_profile_id)
                .ok_or_else(|| "profile not active".to_string())?;
            let generation = store.generation();
            Ok::<_, String>((profile.origin.clone(), generation.0))
        })
        .await?;

    // Retrieve the token from the native Keychain adapter (outside the
    // mutex). The token never crosses to JavaScript.
    let token = transport
        .get_token(&request.active_profile_id)
        .map_err(|e| e.to_string())?
        .ok_or_else(|| "no capability for active profile".to_owned())?;

    // Construct a HubHttp with the active profile's origin and token, using
    // the shared client and diagnostics. The network request runs outside
    // the lifecycle mutex.
    let http = transport.make_http(origin, token);
    let response = http
        .request_with_profile(
            crate::http_transport::HubRequest {
                method: request.method,
                path: request.path,
                body: request.body,
                media_type: request.media_type,
            },
            Some(&request.active_profile_id),
            generation,
        )
        .await
        .map_err(|e| e.to_string())?;

    Ok(HubHttpResponse {
        status: response.status,
        body: response.body,
    })
}

// ---------------------------------------------------------------------------
// AppWire transport commands
// ---------------------------------------------------------------------------

/// A serializable AppWire channel event delivered to JavaScript. Never
/// carries the token or URL.
#[derive(Debug, Clone, serde::Serialize)]
#[serde(rename_all = "camelCase", tag = "type")]
pub enum AppwireChannelEvent {
    /// A text frame from the server.
    Text { data: String },
    /// The connection closed with a code.
    Closed { code: u16 },
    /// An error occurred (no details that could leak a token).
    Error,
}

#[derive(Debug, Clone, serde::Deserialize)]
#[serde(rename_all = "camelCase")]
pub struct AppwireOpenRequest {
    pub profile_id: String,
}

/// Redacted AppWire open response. Carries the opaque connection ID (profile
/// + generation) and the Tauri channel identifier. Never carries the token
///   or URL.
#[derive(Debug, Clone, serde::Serialize)]
#[serde(rename_all = "camelCase")]
pub struct AppwireOpenResponse {
    /// Opaque connection identifier (base64 of profile_id:generation).
    pub connection_id: String,
    /// The connection generation.
    pub generation: u64,
}

#[tauri::command]
pub async fn appwire_open(
    runtime: State<'_, ProfileRuntime>,
    transport: State<'_, TransportState>,
    request: AppwireOpenRequest,
    on_event: tauri::ipc::Channel,
) -> Result<AppwireOpenResponse, String> {
    // Brief state transition: look up the profile's origin and verify it
    // exists. The generation is read for staleness checking.
    let (origin, _generation) = runtime
        .serialized(|store| {
            let profiles = store.list().map_err(|e| e.to_string())?;
            let profile = profiles
                .iter()
                .find(|p| p.id == request.profile_id)
                .ok_or_else(|| "profile not found".to_owned())?;
            let generation = store.generation();
            Ok::<_, String>((profile.origin.clone(), generation.0))
        })
        .await?;

    // Retrieve the token outside the mutex.
    let token = transport
        .get_token(&request.profile_id)
        .map_err(|e| e.to_string())?
        .ok_or_else(|| "no capability for profile".to_owned())?;

    // Build the WebSocket URL from the origin.
    let ws_url = appwire_url(&origin);

    // Create a bounded channel for events. The Tauri Channel relays events
    // to JavaScript. The bounded queue closes on overload.
    let (tx, mut rx) = tokio::sync::mpsc::channel::<AppwireEvent>(256);

    // Spawn a forwarder that relays AppwireEvents to the Tauri Channel as
    // serialized JSON. The Tauri Channel is the JS consumer.
    let channel = on_event.clone();
    tokio::spawn(async move {
        while let Some(event) = rx.recv().await {
            let payload = match event {
                AppwireEvent::Text(text) => AppwireChannelEvent::Text { data: text },
                AppwireEvent::Closed(code) => AppwireChannelEvent::Closed { code },
                AppwireEvent::Error => AppwireChannelEvent::Error,
            };
            let json = serde_json::to_string(&payload).unwrap_or_default();
            let _ = channel.send(tauri::ipc::InvokeResponseBody::Json(json));
        }
    });

    let conn_id = transport
        .appwire()
        .open(&request.profile_id, ws_url, token, tx)
        .await
        .map_err(|e| e.to_string())?;

    Ok(AppwireOpenResponse {
        connection_id: encode_conn_id(&conn_id),
        generation: conn_id.generation(),
    })
}

#[derive(Debug, Clone, serde::Deserialize)]
#[serde(rename_all = "camelCase")]
pub struct AppwireSendRequest {
    pub connection_id: String,
    pub frame: String,
}

#[tauri::command]
pub async fn appwire_send(
    transport: State<'_, TransportState>,
    request: AppwireSendRequest,
) -> Result<(), String> {
    let conn_id = decode_conn_id(&request.connection_id)?;
    transport
        .appwire()
        .send(conn_id, request.frame)
        .await
        .map_err(|e| e.to_string())
}

#[derive(Debug, Clone, serde::Deserialize)]
#[serde(rename_all = "camelCase")]
pub struct AppwireCloseRequest {
    pub connection_id: String,
}

#[tauri::command]
pub async fn appwire_close(
    transport: State<'_, TransportState>,
    request: AppwireCloseRequest,
) -> Result<(), String> {
    let conn_id = decode_conn_id(&request.connection_id)?;
    transport.appwire().close(conn_id).await;
    Ok(())
}

// ---------------------------------------------------------------------------
// Diagnostics command
// ---------------------------------------------------------------------------

#[tauri::command]
pub async fn diagnostics_snapshot(
    transport: State<'_, TransportState>,
) -> Result<Vec<DiagnosticEntry>, String> {
    Ok(transport.diagnostics().snapshot())
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

/// Build the AppWire WebSocket URL from an HTTP/HTTPS origin.
fn appwire_url(origin: &str) -> String {
    if let Some(rest) = origin.strip_prefix("https://") {
        format!("wss://{rest}/rpc")
    } else if let Some(rest) = origin.strip_prefix("http://") {
        format!("ws://{rest}/rpc")
    } else {
        format!("{origin}/rpc")
    }
}

/// Encode a ConnectionId as an opaque string for JavaScript. Never exposes
/// the token.
fn encode_conn_id(conn_id: &ConnectionId) -> String {
    format!("{}:{}", conn_id.profile_id(), conn_id.generation())
}

/// Decode an opaque connection ID string back to a ConnectionId.
fn decode_conn_id(s: &str) -> Result<ConnectionId, String> {
    let (profile_id, gen_str) = s
        .rsplit_once(':')
        .ok_or_else(|| "invalid connection id".to_owned())?;
    let generation = gen_str
        .parse::<u64>()
        .map_err(|_| "invalid generation".to_owned())?;
    Ok(ConnectionId::new(profile_id.to_owned(), generation))
}
