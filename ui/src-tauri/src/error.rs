//! The error type that crosses the Tauri boundary.
//!
//! It mirrors the control API's error body (`{error, code, commands?}`, `api/desktop/API.md`) with
//! `error` renamed to `message`. It never carries the token, a request body or a header: every
//! constructor in this crate uses fixed sentences or the engine's own fixed sentences.

use serde::{Deserialize, Serialize};
use std::fmt;

/// A failed engine call or launch, as the frontend sees it.
#[derive(Debug, Clone, PartialEq, Eq, Serialize, Deserialize)]
pub struct ApiError {
    /// Machine code: one of the contract codes, or one the Rust side produces
    /// (`permission_denied`, `engine_unreachable`).
    pub code: String,
    /// Sentence for the user (the engine's own, or a fixed one from this crate).
    pub message: String,
    /// Only for `needs_setup`: the commands the human must run once.
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub commands: Option<Vec<String>>,
}

impl ApiError {
    pub fn new(code: &str, message: &str) -> Self {
        Self { code: code.to_string(), message: message.to_string(), commands: None }
    }

    /// The engine cannot be reached (not started, gone, refused the token, timed out).
    pub fn engine_unreachable(message: &str) -> Self {
        Self::new("engine_unreachable", message)
    }

    /// The user cancelled UAC / pkexec, or the elevation was refused.
    pub fn permission_denied() -> Self {
        Self::new("permission_denied", "Permission was not given.")
    }

    pub fn bad_request(message: &str) -> Self {
        Self::new("bad_request", message)
    }

    pub fn internal() -> Self {
        Self::new("internal", "Something went wrong.")
    }

    /// True when the call failed because the engine is gone, not because of the request.
    pub fn is_engine_lost(&self) -> bool {
        self.code == "engine_unreachable"
    }
}

impl fmt::Display for ApiError {
    fn fmt(&self, f: &mut fmt::Formatter<'_>) -> fmt::Result {
        write!(f, "{}: {}", self.code, self.message)
    }
}

impl std::error::Error for ApiError {}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn serializes_as_code_message_and_optional_commands() {
        let e = ApiError::new("busy", "Try again.");
        assert_eq!(serde_json::to_string(&e).unwrap(), r#"{"code":"busy","message":"Try again."}"#);
        let mut e = ApiError::new("needs_setup", "Setup.");
        e.commands = Some(vec!["a".into(), "b".into()]);
        assert_eq!(serde_json::to_string(&e).unwrap(), r#"{"code":"needs_setup","message":"Setup.","commands":["a","b"]}"#);
    }
}
