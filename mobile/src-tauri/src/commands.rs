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
use crate::http_transport::{HubResponseHeaders, PreparedHttpRequest, REQUEST_ID_HEADER};
use crate::profile_runtime::{
    CancelPreviewRequest, ConfirmPairingRequest, HealthResponse, PreviewPasteRequest,
    PreviewRepairRequest, PreviewResponse, ProfileRuntime, ProfileSummaryResponse, RemoveRequest,
    RenameRequest, SelectRequest, SelectResponse,
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
    transport: State<'_, TransportState>,
    request: ConfirmPairingRequest,
) -> Result<ProfileSummaryResponse, String> {
    runtime
        .confirm_pairing(
            transport.appwire(),
            &request.preview_id,
            &request.name,
            request.allow_duplicate_origin,
            ReleaseMode::Release,
        )
        .await
        .map_err(|error| error.to_string())
        .map(ProfileSummaryResponse::from)
}

#[tauri::command]
pub async fn profile_preview_cancel(
    runtime: State<'_, ProfileRuntime>,
    request: CancelPreviewRequest,
) -> Result<(), String> {
    runtime
        .serialized(|store| {
            store
                .cancel_preview(&request.preview_id)
                .map_err(|e| e.to_string())
        })
        .await
}

#[tauri::command]
pub async fn profile_previews_clear(runtime: State<'_, ProfileRuntime>) -> Result<(), String> {
    runtime.serialized(|store| store.clear_previews()).await;
    Ok(())
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
        .remove_profile(transport.appwire(), &request.profile_id)
        .await
        .map_err(|error| error.to_string());
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
    transport: State<'_, TransportState>,
    request: SelectRequest,
) -> Result<SelectResponse, String> {
    runtime
        .select_profile(transport.appwire(), &request.profile_id)
        .await
        .map_err(|error| error.to_string())
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

#[derive(Debug, Clone, serde::Deserialize, serde::Serialize)]
#[serde(rename_all = "camelCase", deny_unknown_fields)]
pub struct HubHttpExecuteRequest {
    pub request_id: String,
}

#[derive(Debug, Clone, serde::Deserialize, serde::Serialize)]
#[serde(rename_all = "camelCase", deny_unknown_fields)]
pub struct HubHttpResponseMetadata {
    pub request_id: String,
    pub status: u16,
    pub headers: HubResponseHeaders,
    pub media_type: Option<String>,
    pub body_length: usize,
}

#[tauri::command]
pub async fn hub_http_prepare(
    transport: State<'_, TransportState>,
    request: PreparedHttpRequest,
) -> Result<(), String> {
    transport
        .http_requests()
        .prepare(request)
        .map_err(|error| error.to_string())
}

/// Store one raw IPC request body. Metadata was strictly decoded and validated
/// by `hub_http_prepare`; the only raw-request header is an opaque request ID.
#[tauri::command]
pub async fn hub_http_upload_body(
    transport: State<'_, TransportState>,
    request: tauri::ipc::Request<'_>,
) -> Result<(), String> {
    let request_id = request
        .headers()
        .get(REQUEST_ID_HEADER)
        .and_then(|value| value.to_str().ok())
        .ok_or_else(|| "invalid request id".to_owned())?;
    let body = match request.body() {
        tauri::ipc::InvokeBody::Raw(body) => body.clone(),
        tauri::ipc::InvokeBody::Json(_) => return Err("raw request body required".to_owned()),
    };
    transport
        .http_requests()
        .upload_body(request_id, body)
        .map_err(|error| error.to_string())
}

#[tauri::command]
pub async fn hub_http_request(
    runtime: State<'_, ProfileRuntime>,
    transport: State<'_, TransportState>,
    request: HubHttpExecuteRequest,
    on_response: tauri::ipc::Channel,
) -> Result<tauri::ipc::Response, String> {
    let registered = transport
        .http_requests()
        .begin(&request.request_id)
        .map_err(|error| error.to_string())?;
    let result = async {
        let metadata = registered.metadata;
        let snapshot = runtime
            .serialized(|store| {
                store
                    .active_snapshot(&metadata.active_profile_id)
                    .map_err(|error| error.to_string())
            })
            .await?;
        let (profile_id, generation, origin, token) = snapshot.into_parts();
        let response = transport
            .make_http(origin, token)
            .request_cancellable(
                crate::http_transport::HubRequest {
                    method: metadata.method,
                    path: metadata.path,
                    body: registered.body,
                    media_type: metadata.media_type,
                },
                Some(&profile_id),
                generation.0,
                registered.cancellation,
            )
            .await
            .map_err(|error| error.to_string())?;
        let response_metadata = HubHttpResponseMetadata {
            request_id: request.request_id.clone(),
            status: response.status,
            headers: response.headers,
            media_type: response.media_type,
            body_length: response.body.len(),
        };
        let json = serde_json::to_string(&response_metadata)
            .map_err(|_| "response metadata unavailable".to_owned())?;
        on_response
            .send(tauri::ipc::InvokeResponseBody::Json(json))
            .map_err(|_| "response metadata unavailable".to_owned())?;
        Ok(tauri::ipc::Response::new(response.body))
    }
    .await;
    transport.http_requests().finish(&request.request_id);
    result
}

#[tauri::command]
pub async fn hub_http_cancel(
    transport: State<'_, TransportState>,
    request: HubHttpExecuteRequest,
) -> Result<(), String> {
    transport
        .http_requests()
        .cancel(&request.request_id)
        .map_err(|error| error.to_string())
}

// ---------------------------------------------------------------------------
// AppWire transport commands
// ---------------------------------------------------------------------------

/// A serializable AppWire channel event delivered to JavaScript. Never
/// carries the token or URL.
#[derive(Debug, Clone, serde::Serialize)]
#[serde(
    rename_all = "camelCase",
    rename_all_fields = "camelCase",
    tag = "type"
)]
pub enum AppwireChannelEvent {
    /// A text frame from the server.
    Text {
        connection_id: String,
        profile_id: String,
        generation: u64,
        data: String,
    },
    /// The connection closed with a code.
    Closed {
        connection_id: String,
        profile_id: String,
        generation: u64,
        code: u16,
        reason: String,
    },
    /// An error occurred (no details that could leak a token).
    Error {
        connection_id: String,
        profile_id: String,
        generation: u64,
    },
}

#[derive(Debug, Clone, serde::Deserialize, serde::Serialize)]
#[serde(rename_all = "camelCase", deny_unknown_fields)]
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
    /// The selected profile ID (redacted routing identity, never a capability).
    pub profile_id: String,
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
    let snapshot = runtime
        .serialized(|store| {
            store
                .active_snapshot(&request.profile_id)
                .map_err(|e| e.to_string())
        })
        .await?;
    let (profile_id, profile_generation, origin, token) = snapshot.into_parts();

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
                AppwireEvent::Text {
                    connection_id,
                    data,
                } => AppwireChannelEvent::Text {
                    connection_id: encode_conn_id(&connection_id),
                    profile_id: connection_id.profile_id().to_owned(),
                    generation: connection_id.generation(),
                    data,
                },
                AppwireEvent::Closed {
                    connection_id,
                    code,
                    reason,
                } => AppwireChannelEvent::Closed {
                    connection_id: encode_conn_id(&connection_id),
                    profile_id: connection_id.profile_id().to_owned(),
                    generation: connection_id.generation(),
                    code,
                    reason,
                },
                AppwireEvent::Error { connection_id } => AppwireChannelEvent::Error {
                    connection_id: encode_conn_id(&connection_id),
                    profile_id: connection_id.profile_id().to_owned(),
                    generation: connection_id.generation(),
                },
            };
            let json = serde_json::to_string(&payload).unwrap_or_default();
            let _ = channel.send(tauri::ipc::InvokeResponseBody::Json(json));
        }
    });

    let conn_id = transport
        .appwire()
        .open(&profile_id, profile_generation.0, ws_url, token, tx)
        .await
        .map_err(|e| e.to_string())?;

    Ok(AppwireOpenResponse {
        connection_id: encode_conn_id(&conn_id),
        profile_id,
        generation: conn_id.generation(),
    })
}

#[derive(Debug, Clone, serde::Deserialize, serde::Serialize)]
#[serde(rename_all = "camelCase", deny_unknown_fields)]
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

#[derive(Debug, Clone, serde::Deserialize, serde::Serialize)]
#[serde(rename_all = "camelCase", deny_unknown_fields)]
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

#[cfg(test)]
mod tests {
    use super::{AppwireChannelEvent, AppwireOpenRequest, HubHttpExecuteRequest};

    #[test]
    fn request_dtos_reject_secret_bearing_extra_fields() {
        let extra = serde_json::json!({"profileId":"p", "token":"secret"});
        assert!(serde_json::from_value::<AppwireOpenRequest>(extra).is_err());
        let extra = serde_json::json!({"requestId":"request_123456789", "rawQr":"secret"});
        assert!(serde_json::from_value::<HubHttpExecuteRequest>(extra).is_err());
    }

    #[test]
    fn appwire_channel_dto_serializes_complete_identity_on_every_variant() {
        let text = serde_json::to_value(AppwireChannelEvent::Text {
            connection_id: "profile:7".to_owned(),
            profile_id: "profile".to_owned(),
            generation: 7,
            data: "frame".to_owned(),
        })
        .unwrap();
        let closed = serde_json::to_value(AppwireChannelEvent::Closed {
            connection_id: "profile:7".to_owned(),
            profile_id: "profile".to_owned(),
            generation: 7,
            code: 1012,
            reason: "service restart".to_owned(),
        })
        .unwrap();
        let error = serde_json::to_value(AppwireChannelEvent::Error {
            connection_id: "profile:7".to_owned(),
            profile_id: "profile".to_owned(),
            generation: 7,
        })
        .unwrap();

        assert_eq!(
            text,
            serde_json::json!({
                "type": "text",
                "connectionId": "profile:7",
                "profileId": "profile",
                "generation": 7,
                "data": "frame",
            })
        );
        assert_eq!(
            closed,
            serde_json::json!({
                "type": "closed",
                "connectionId": "profile:7",
                "profileId": "profile",
                "generation": 7,
                "code": 1012,
                "reason": "service restart",
            })
        );
        assert_eq!(
            error,
            serde_json::json!({
                "type": "error",
                "connectionId": "profile:7",
                "profileId": "profile",
                "generation": 7,
            })
        );
    }
}
