//! Obhod desktop UI: the Tauri app (ADR 0003).
//!
//! The Rust side is the only client of the engine's control API (`api/desktop/API.md`). It starts
//! `splitter` (elevated), keeps the access token to itself, and exposes typed commands plus one
//! event (`engine-event`) to the webview. Rules: `ui/AGENTS.md`.

pub mod client;
pub mod commands;
pub mod engine;
pub mod error;
pub mod http;
pub mod launcher;
pub mod sse;
pub mod token;

#[cfg(test)]
mod testutil;

use std::sync::Arc;
use std::time::Duration;

use tauri::{Manager as _, RunEvent};

use crate::commands::{AppState, TauriSink};
use crate::engine::{EngineConfig, Manager};

/// Builds and runs the app. Never returns until the app exits.
pub fn run() {
    let app = tauri::Builder::default()
        .setup(|app| {
            let run_dir = app.path().app_local_data_dir().map_err(|_| "no per-user data folder")?.join("run");
            let program = launcher::engine_program().ok_or("cannot locate the engine program")?;
            let sink = Arc::new(TauriSink(app.handle().clone()));
            let cfg = EngineConfig::new(program, run_dir);
            let manager = Arc::new(Manager::new(Arc::from(launcher::default_launcher()), cfg, sink));
            app.manage(AppState { manager });
            Ok(())
        })
        .invoke_handler(tauri::generate_handler![
            commands::ui_version,
            commands::engine_start,
            commands::engine_restart,
            commands::api_state,
            commands::api_routes,
            commands::api_processes,
            commands::api_logs,
            commands::api_version,
            commands::api_connect,
            commands::api_disconnect,
            commands::api_check_now,
            commands::api_shutdown,
            commands::servers_list,
            commands::servers_add,
            commands::servers_activate,
            commands::servers_delete,
            commands::servers_test,
        ])
        .build(tauri::generate_context!())
        .expect("error while building the app");

    app.run(|handle, event| {
        // Closing the window ends the app and the engine with it (ADR 0003, decision 5).
        if let RunEvent::Exit = event {
            if let Some(state) = handle.try_state::<AppState>() {
                let manager = state.manager.clone();
                tauri::async_runtime::block_on(async move {
                    let _ = tokio::time::timeout(Duration::from_secs(10), manager.stop()).await;
                });
            }
        }
    });
}
