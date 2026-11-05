//! Tauri commands: the webview's only way to reach the engine.
//!
//! Every command resolves the live [`Api`] from the [`Manager`], makes one call and returns typed
//! data or an [`ApiError`]. Nothing here returns or logs the token. Command names are the
//! `snake_case` Rust names; arguments are camelCase on the JavaScript side.

use std::future::Future;
use std::sync::Arc;

use serde_json::Value;
use tauri::{AppHandle, Emitter, State};

use crate::client::{Ack, Activated, Api, Desync, DesyncPreset, DesyncStarted, Logs,Server, ServerTest, SiteRule, TestRequest, Version};
use crate::engine::Manager;
use crate::error::ApiError;
use crate::sse::{EngineEvent, EventSink};

/// The webview event all engine events arrive on: payload `{ kind, data }`.
pub const EVENT_NAME: &str = "engine-event";

/// Emits engine events to the webview.
pub struct TauriSink(pub AppHandle);

impl EventSink for TauriSink {
    fn emit(&self, event: EngineEvent) {
        let _ = self.0.emit(EVENT_NAME, event);
    }
}

/// Shared state of the app.
pub struct AppState {
    pub manager: Arc<Manager>,
}

async fn with_api<T, F, Fut>(state: &AppState, f: F) -> Result<T, ApiError>
where
    F: FnOnce(Api) -> Fut,
    Fut: Future<Output = Result<T, ApiError>>,
{
    let api = state.manager.api().await?;
    let r = f(api).await;
    if let Err(e) = &r {
        state.manager.observe(e).await;
    }
    r
}

/// The UI's own version (the engine's `GET /version` has none).
#[tauri::command]
pub fn ui_version() -> String {
    env!("CARGO_PKG_VERSION").to_string()
}

/// Starts the engine (UAC / pkexec prompt) unless it runs. Resolves when it answers.
#[tauri::command]
pub async fn engine_start(state: State<'_, AppState>) -> Result<(), ApiError> {
    state.manager.ensure_started().await
}

/// Stops the engine and starts a new one.
#[tauri::command]
pub async fn engine_restart(state: State<'_, AppState>) -> Result<(), ApiError> {
    state.manager.restart().await
}

#[tauri::command]
pub async fn api_state(state: State<'_, AppState>) -> Result<Value, ApiError> {
    with_api(&state, |a| async move { a.state().await }).await
}

#[tauri::command]
pub async fn api_routes(state: State<'_, AppState>) -> Result<Value, ApiError> {
    with_api(&state, |a| async move { a.routes().await }).await
}

#[tauri::command]
pub async fn api_processes(state: State<'_, AppState>) -> Result<Value, ApiError> {
    with_api(&state, |a| async move { a.processes().await }).await
}

#[tauri::command]
pub async fn api_logs(state: State<'_, AppState>, name: String) -> Result<Logs, ApiError> {
    with_api(&state, |a| async move { a.logs(&name).await }).await
}

#[tauri::command]
pub async fn api_version(state: State<'_, AppState>) -> Result<Version, ApiError> {
    with_api(&state, |a| async move { a.version().await }).await
}

#[tauri::command]
pub async fn api_connect(state: State<'_, AppState>) -> Result<Ack, ApiError> {
    with_api(&state, |a| async move { a.connect().await }).await
}

#[tauri::command]
pub async fn api_disconnect(state: State<'_, AppState>) -> Result<Ack, ApiError> {
    with_api(&state, |a| async move { a.disconnect().await }).await
}

#[tauri::command]
pub async fn api_check_now(state: State<'_, AppState>, domains: Option<Vec<String>>) -> Result<Ack, ApiError> {
    with_api(&state, |a| async move { a.check_now(domains).await }).await
}

/// Stops the engine and quits the app.
#[tauri::command]
pub async fn api_shutdown(app: AppHandle, state: State<'_, AppState>) -> Result<(), ApiError> {
    state.manager.stop().await;
    app.exit(0);
    Ok(())
}

#[tauri::command]
pub async fn servers_list(state: State<'_, AppState>) -> Result<Vec<Server>, ApiError> {
    with_api(&state, |a| async move { a.servers().await }).await
}

#[tauri::command]
pub async fn servers_add(state: State<'_, AppState>, input: String, name: Option<String>) -> Result<Server, ApiError> {
    with_api(&state, |a| async move { a.add_server(&input, name.as_deref()).await }).await
}

#[tauri::command]
pub async fn servers_activate(state: State<'_, AppState>, id: String) -> Result<Activated, ApiError> {
    with_api(&state, |a| async move { a.activate_server(&id).await }).await
}

#[tauri::command]
pub async fn servers_delete(state: State<'_, AppState>, id: String) -> Result<Ack, ApiError> {
    with_api(&state, |a| async move { a.delete_server(&id).await }).await
}

#[tauri::command]
pub async fn servers_test(state: State<'_, AppState>, request: TestRequest) -> Result<ServerTest, ApiError> {
    with_api(&state, |a| async move { a.test_server(&request).await }).await
}

#[tauri::command]
pub async fn sites_list(state: State<'_, AppState>) -> Result<Vec<SiteRule>, ApiError> {
    with_api(&state, |a| async move { a.sites().await }).await
}

/// Adds or changes the rule of a site. `action` is `direct`, `desync`, `tunnel` or `block`.
#[tauri::command]
pub async fn site_set(state: State<'_, AppState>, domain: String, action: String) -> Result<SiteRule, ApiError> {
    with_api(&state, |a| async move { a.set_site(&domain, &action).await }).await
}

/// Removes the rule of a site (back to automatic).
#[tauri::command]
pub async fn site_delete(state: State<'_, AppState>, domain: String) -> Result<Ack, ApiError> {
    with_api(&state, |a| async move { a.delete_site(&domain).await }).await
}

#[tauri::command]
pub async fn desync_get(state: State<'_, AppState>) -> Result<Desync, ApiError> {
    with_api(&state, |a| async move { a.desync().await }).await
}

#[tauri::command]
pub async fn desync_presets(state: State<'_, AppState>) -> Result<Vec<DesyncPreset>, ApiError> {
    with_api(&state, |a| async move { a.desync_presets().await }).await
}

/// Chooses the desync preset: `auto`, `custom` or a catalogue id.
#[tauri::command]
pub async fn desync_set(state: State<'_, AppState>, preset: String) -> Result<Desync, ApiError> {
    with_api(&state, |a| async move { a.set_desync(&preset).await }).await
}

/// Starts a preset search now (automatic mode, connected).
#[tauri::command]
pub async fn desync_search(state: State<'_, AppState>) -> Result<DesyncStarted, ApiError> {
    with_api(&state, |a| async move { a.start_desync_search().await }).await
}
