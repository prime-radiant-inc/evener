//! Typed Tauri commands for profile lifecycle, HTTP, and AppWire transport.
//!
//! All profile lifecycle commands serialize through the `ProfileRuntime`'s
//! async lifecycle mutex. JavaScript receives no capability, raw QR text, or
//! native path — only redacted `{id,name,origin}` summaries and opaque
//! preview IDs.

use tauri::State;

use crate::error::ReleaseMode;
use crate::profile_runtime::{
    ConfirmPairingRequest, HealthResponse, PreviewPasteRequest, PreviewRepairRequest,
    PreviewResponse, ProfileRuntime, ProfileSummaryResponse, RemoveRequest, RenameRequest,
    SelectRequest, SelectResponse,
};

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
    request: RemoveRequest,
) -> Result<SelectResponse, String> {
    runtime
        .serialized(|store| store.remove(&request.profile_id).map_err(|e| e.to_string()))
        .await
        .map(SelectResponse::from)
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
// HTTP transport command (delegates to HubHttp — implemented in slice B)
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

#[tauri::command]
pub async fn hub_http_request(
    _runtime: State<'_, ProfileRuntime>,
    _request: HubHttpRequest,
) -> Result<serde_json::Value, String> {
    // Implemented in slice B (http_transport.rs).
    Err("http transport not yet initialized".to_owned())
}

// ---------------------------------------------------------------------------
// AppWire transport commands (delegates to AppwireManager — slice B)
// ---------------------------------------------------------------------------

#[derive(Debug, Clone, serde::Deserialize)]
#[serde(rename_all = "camelCase")]
pub struct AppwireOpenRequest {
    pub profile_id: String,
}

#[tauri::command]
pub async fn appwire_open(
    _runtime: State<'_, ProfileRuntime>,
    _request: AppwireOpenRequest,
) -> Result<String, String> {
    Err("appwire transport not yet initialized".to_owned())
}

#[tauri::command]
pub async fn appwire_send(
    _runtime: State<'_, ProfileRuntime>,
    _frame: String,
) -> Result<(), String> {
    Err("appwire transport not yet initialized".to_owned())
}

#[tauri::command]
pub async fn appwire_close(_runtime: State<'_, ProfileRuntime>) -> Result<(), String> {
    Err("appwire transport not yet initialized".to_owned())
}
