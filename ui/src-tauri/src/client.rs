//! Typed calls for every control API endpoint (`api/desktop/API.md`) and the mapping of error
//! bodies to [`ApiError`].
//!
//! `state`, `routes` and `processes` are passed through as validated JSON: their field names are
//! the engine's Go names (`Snapshot.Routes`, ...) and the frontend owns their TypeScript types.
//! The small, stable payloads (version, logs, servers, acknowledgements) are real structs, so a
//! malformed engine answer is rejected here instead of reaching the webview.

use serde::{Deserialize, Serialize};
use serde_json::{json, Value};

use crate::error::ApiError;
use crate::http::{Client, ClientError, Response};

const API: &str = "/api/v1";

/// `GET /version`.
#[derive(Debug, Clone, PartialEq, Serialize, Deserialize)]
#[serde(rename_all = "camelCase")]
pub struct Version {
    pub splitter: String,
    pub sing_box: String,
    pub desync: String,
    pub api: String,
}

/// `GET /logs?name=`.
#[derive(Debug, Clone, PartialEq, Serialize, Deserialize)]
pub struct Logs {
    pub name: String,
    pub lines: Vec<String>,
}

/// A stored detour server. The engine never returns a secret; host and port come back in full.
#[derive(Debug, Clone, PartialEq, Serialize, Deserialize)]
#[serde(rename_all = "camelCase")]
pub struct Server {
    pub id: String,
    pub name: String,
    pub protocol: String,
    pub engine: String,
    pub host: String,
    pub port: u16,
    pub active: bool,
    pub added_at: String,
}

/// `POST /servers/test` answer. `code` is `""`, `invalid_input` or `unsupported_protocol`.
#[derive(Debug, Clone, PartialEq, Serialize, Deserialize)]
pub struct ServerTest {
    pub ok: bool,
    pub protocol: String,
    pub code: String,
}

/// `POST /servers/{id}/activate` answer.
#[derive(Debug, Clone, PartialEq, Serialize, Deserialize)]
pub struct Activated {
    pub ok: bool,
    pub active: String,
    pub reloaded: bool,
}

/// `{ "ok": true, ... }` answers of connect, disconnect, check-now, shutdown, delete.
#[derive(Debug, Clone, PartialEq, Serialize, Deserialize)]
pub struct Ack {
    pub ok: bool,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub state: Option<String>,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub checking: Option<bool>,
}

/// What `POST /servers/test` takes: exactly one of the two.
#[derive(Debug, Clone, PartialEq, Serialize, Deserialize)]
#[serde(rename_all = "camelCase", deny_unknown_fields)]
pub struct TestRequest {
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub id: Option<String>,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub input: Option<String>,
}

/// The control API as seen from Rust. Cheap to clone.
#[derive(Clone, Debug)]
pub struct Api {
    client: Client,
}

fn client_error(e: ClientError) -> ApiError {
    match e {
        ClientError::Timeout => ApiError::engine_unreachable("The engine did not answer in time."),
        ClientError::Unreachable => ApiError::engine_unreachable("The engine is not running."),
        ClientError::Protocol(_) => ApiError::engine_unreachable("The engine sent an answer the app cannot read."),
        ClientError::Status(r) => error_from_response(&r),
    }
}

/// Maps a non-2xx response to an [`ApiError`].
///
/// 401 has an empty body by contract: the engine does not accept our token, which means it is not
/// the process we started (or is a stale one), so it counts as unreachable. Other bodies are
/// `{error, code, commands?}`; anything else is mapped from the status.
pub fn error_from_response(r: &Response) -> ApiError {
    if r.status == 401 {
        return ApiError::engine_unreachable("The engine did not accept the app's access.");
    }
    #[derive(Deserialize)]
    struct Body {
        error: String,
        code: String,
        #[serde(default)]
        commands: Option<Vec<String>>,
    }
    if let Ok(b) = serde_json::from_slice::<Body>(&r.body) {
        if !b.code.is_empty() {
            return ApiError { code: b.code, message: b.error, commands: b.commands };
        }
    }
    match r.status {
        503 => ApiError::engine_unreachable("The engine is not running."),
        403 => ApiError::new("forbidden", "The engine refused the request."),
        404 => ApiError::new("not_found", "Not found."),
        _ => ApiError::internal(),
    }
}

fn parse<T: for<'de> Deserialize<'de>>(r: &Response) -> Result<T, ApiError> {
    serde_json::from_slice(&r.body).map_err(|_| ApiError::engine_unreachable("The engine sent an answer the app cannot read."))
}

impl Api {
    pub fn new(client: Client) -> Self {
        Self { client }
    }

    async fn raw(&self, method: &str, path: &str, body: Option<&Value>) -> Result<Response, ApiError> {
        let bytes = match body {
            Some(v) => Some(serde_json::to_vec(v).map_err(|_| ApiError::internal())?),
            None => None,
        };
        let r = self.client.send(method, &format!("{API}{path}"), bytes.as_deref()).await.map_err(client_error)?;
        if (200..300).contains(&r.status) {
            Ok(r)
        } else {
            Err(error_from_response(&r))
        }
    }

    async fn json(&self, method: &str, path: &str, body: Option<&Value>) -> Result<Value, ApiError> {
        let r = self.raw(method, path, body).await?;
        parse(&r)
    }

    pub async fn state(&self) -> Result<Value, ApiError> {
        self.json("GET", "/state", None).await
    }

    pub async fn routes(&self) -> Result<Value, ApiError> {
        self.json("GET", "/routes", None).await
    }

    pub async fn processes(&self) -> Result<Value, ApiError> {
        self.json("GET", "/processes", None).await
    }

    /// `name` is `splitter`, `sing-box` or `desync`; anything else is rejected here.
    pub async fn logs(&self, name: &str) -> Result<Logs, ApiError> {
        if !matches!(name, "splitter" | "sing-box" | "desync") {
            return Err(ApiError::bad_request("Unknown log."));
        }
        parse(&self.raw("GET", &format!("/logs?name={name}"), None).await?)
    }

    pub async fn version(&self) -> Result<Version, ApiError> {
        parse(&self.raw("GET", "/version", None).await?)
    }

    pub async fn connect(&self) -> Result<Ack, ApiError> {
        parse(&self.raw("POST", "/connect", None).await?)
    }

    pub async fn disconnect(&self) -> Result<Ack, ApiError> {
        parse(&self.raw("POST", "/disconnect", None).await?)
    }

    /// `domains` empty or `None` checks everything.
    pub async fn check_now(&self, domains: Option<Vec<String>>) -> Result<Ack, ApiError> {
        let body = match domains {
            Some(d) if !d.is_empty() => json!({ "domains": d }),
            _ => json!({}),
        };
        parse(&self.raw("POST", "/check-now", Some(&body)).await?)
    }

    pub async fn shutdown(&self) -> Result<Ack, ApiError> {
        parse(&self.raw("POST", "/shutdown", None).await?)
    }

    pub async fn servers(&self) -> Result<Vec<Server>, ApiError> {
        parse(&self.raw("GET", "/servers", None).await?)
    }

    /// The pasted `input` goes into the request body only; it is never logged or returned.
    pub async fn add_server(&self, input: &str, name: Option<&str>) -> Result<Server, ApiError> {
        let body = match name.filter(|n| !n.is_empty()) {
            Some(n) => json!({ "input": input, "name": n }),
            None => json!({ "input": input }),
        };
        parse(&self.raw("POST", "/servers", Some(&body)).await?)
    }

    pub async fn activate_server(&self, id: &str) -> Result<Activated, ApiError> {
        let id = server_id(id)?;
        parse(&self.raw("POST", &format!("/servers/{id}/activate"), None).await?)
    }

    pub async fn delete_server(&self, id: &str) -> Result<Ack, ApiError> {
        let id = server_id(id)?;
        parse(&self.raw("DELETE", &format!("/servers/{id}"), None).await?)
    }

    pub async fn test_server(&self, req: &TestRequest) -> Result<ServerTest, ApiError> {
        if req.id.is_some() == req.input.is_some() {
            return Err(ApiError::bad_request("Send either an id or an input."));
        }
        let body = serde_json::to_value(req).map_err(|_| ApiError::internal())?;
        parse(&self.raw("POST", "/servers/test", Some(&body)).await?)
    }
}

/// Ids are 16 lowercase hex characters; anything else cannot exist (404 on the engine too), and
/// must not be put into a path.
fn server_id(id: &str) -> Result<&str, ApiError> {
    if id.len() == 16 && id.bytes().all(|b| b.is_ascii_digit() || (b'a'..=b'f').contains(&b)) {
        Ok(id)
    } else {
        Err(ApiError::new("not_found", "Not found."))
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use crate::testutil::{testdata, FakeServer};

    const TOKEN: &str = "client-token-client-token-client-token-01";

    async fn setup() -> (FakeServer, Api) {
        let srv = FakeServer::start(TOKEN).await;
        let api = Api::new(Client::new(srv.addr, TOKEN).unwrap());
        (srv, api)
    }

    #[tokio::test]
    async fn state_is_passed_through_from_the_golden_file() {
        let (srv, api) = setup().await;
        for name in ["state_connected", "state_disconnected", "state_error_crashed", "state_error_connect_failed"] {
            let body = testdata(&format!("{name}.golden.json"));
            srv.reply("GET", "/api/v1/state", 200, &body);
            let got = api.state().await.unwrap();
            assert_eq!(got, serde_json::from_str::<Value>(&body).unwrap(), "{name}");
        }
    }

    #[tokio::test]
    async fn get_endpoints_parse_their_golden_files() {
        let (srv, api) = setup().await;
        srv.reply("GET", "/api/v1/routes", 200, &testdata("routes_filled.golden.json"));
        assert!(api.routes().await.unwrap().as_array().unwrap().len() > 1);
        srv.reply("GET", "/api/v1/processes", 200, &testdata("processes.golden.json"));
        assert_eq!(api.processes().await.unwrap().as_array().unwrap().len(), 3);
        srv.reply("GET", "/api/v1/version", 200, &testdata("version.golden.json"));
        let v = api.version().await.unwrap();
        assert_eq!(v.api, "v1");
        assert!(v.splitter.starts_with("splitter"));
        for name in ["splitter", "sing-box", "desync"] {
            srv.reply("GET", "/api/v1/logs", 200, &testdata(&format!("logs_{name}.golden.json")));
            let l = api.logs(name).await.unwrap();
            assert_eq!(l.name, name);
            assert!(!l.lines.is_empty());
        }
        assert!(srv.requests().iter().any(|r| r.path == "/api/v1/logs"));
    }

    #[tokio::test]
    async fn log_names_are_validated_before_any_request() {
        let (srv, api) = setup().await;
        let e = api.logs("../etc/passwd").await.unwrap_err();
        assert_eq!(e.code, "bad_request");
        assert!(srv.requests().is_empty());
    }

    #[tokio::test]
    async fn actions_send_the_right_requests() {
        let (srv, api) = setup().await;
        srv.reply("POST", "/api/v1/connect", 202, r#"{"ok":true,"state":"connecting"}"#);
        srv.reply("POST", "/api/v1/disconnect", 202, r#"{"ok":true,"state":"disconnecting"}"#);
        srv.reply("POST", "/api/v1/check-now", 202, r#"{"ok":true,"checking":true}"#);
        srv.reply("POST", "/api/v1/shutdown", 200, r#"{"ok":true}"#);
        assert_eq!(api.connect().await.unwrap().state.as_deref(), Some("connecting"));
        assert_eq!(api.disconnect().await.unwrap().state.as_deref(), Some("disconnecting"));
        assert_eq!(api.check_now(None).await.unwrap().checking, Some(true));
        api.check_now(Some(vec!["a.example".into()])).await.unwrap();
        assert!(api.shutdown().await.unwrap().ok);
        let reqs = srv.requests();
        assert_eq!(reqs[2].body, b"{}");
        assert_eq!(reqs[3].body, br#"{"domains":["a.example"]}"#);
    }

    #[tokio::test]
    async fn server_management_round_trip() {
        let (srv, api) = setup().await;
        srv.reply("GET", "/api/v1/servers", 200, &testdata("servers_filled.golden.json"));
        let list = api.servers().await.unwrap();
        assert!(list.iter().any(|s| s.active));
        srv.reply("GET", "/api/v1/servers", 200, &testdata("servers_empty.golden.json"));
        assert!(api.servers().await.unwrap().is_empty());

        srv.reply("POST", "/api/v1/servers", 201, &testdata("server_created.golden.json"));
        let s = api.add_server("vless://pasted-secret-link", Some("Mine")).await.unwrap();
        assert_eq!(s.id, "0000000000000001");
        api.add_server("vless://pasted-secret-link", Some("")).await.unwrap();
        let reqs = srv.requests();
        let last = reqs.len();
        assert_eq!(reqs[last - 2].body, br#"{"input":"vless://pasted-secret-link","name":"Mine"}"#);
        assert_eq!(reqs[last - 1].body, br#"{"input":"vless://pasted-secret-link"}"#);

        srv.reply("POST", "/api/v1/servers/0000000000000001/activate", 200, &testdata("server_activate_disconnected.golden.json"));
        assert!(!api.activate_server("0000000000000001").await.unwrap().reloaded);
        srv.reply("POST", "/api/v1/servers/0000000000000001/activate", 202, &testdata("server_activate_connected.golden.json"));
        assert!(api.activate_server("0000000000000001").await.unwrap().reloaded);
        srv.reply("DELETE", "/api/v1/servers/0000000000000001", 200, &testdata("server_deleted.golden.json"));
        assert!(api.delete_server("0000000000000001").await.unwrap().ok);

        srv.reply("POST", "/api/v1/servers/test", 200, &testdata("server_test_ok.golden.json"));
        let t = api.test_server(&TestRequest { id: None, input: Some("x".into()) }).await.unwrap();
        assert!(t.ok);
        srv.reply("POST", "/api/v1/servers/test", 200, &testdata("server_test_unsupported_protocol.golden.json"));
        let t = api.test_server(&TestRequest { id: Some("0000000000000001".into()), input: None }).await.unwrap();
        assert_eq!(t.code, "unsupported_protocol");
    }

    #[tokio::test]
    async fn bad_server_ids_and_test_requests_never_reach_the_engine() {
        let (srv, api) = setup().await;
        for bad in ["", "../x", "0000000000000001/../../x", "ABCDEF0123456789", "000000000000000g"] {
            assert_eq!(api.activate_server(bad).await.unwrap_err().code, "not_found", "{bad}");
            assert_eq!(api.delete_server(bad).await.unwrap_err().code, "not_found", "{bad}");
        }
        let both = TestRequest { id: Some("a".into()), input: Some("b".into()) };
        let none = TestRequest { id: None, input: None };
        assert_eq!(api.test_server(&both).await.unwrap_err().code, "bad_request");
        assert_eq!(api.test_server(&none).await.unwrap_err().code, "bad_request");
        assert!(srv.requests().is_empty());
    }

    #[tokio::test]
    async fn every_golden_error_body_maps_to_code_message_and_commands() {
        let (srv, api) = setup().await;
        let dir = std::path::Path::new(env!("CARGO_MANIFEST_DIR")).join("../../api/desktop/testdata");
        let mut seen = 0;
        for entry in std::fs::read_dir(dir).unwrap() {
            let name = entry.unwrap().file_name().to_string_lossy().into_owned();
            if !name.starts_with("error_") || !name.ends_with(".golden.json") {
                continue;
            }
            let body = testdata(&name);
            let want: Value = serde_json::from_str(&body).unwrap();
            let status = match want["code"].as_str().unwrap() {
                "bad_request" => 400,
                "forbidden" => 403,
                "not_found" | "unknown_domain" => 404,
                "method_not_allowed" => 405,
                "busy" => 409,
                "too_large" => 413,
                "invalid_input" | "unsupported_protocol" => 422,
                "duplicate" | "limit_reached" => 409,
                "engine_unreachable" => 503,
                _ => 500,
            };
            srv.reply("GET", "/api/v1/state", status, &body);
            let e = api.state().await.unwrap_err();
            assert_eq!(e.code, want["code"].as_str().unwrap(), "{name}");
            assert_eq!(e.message, want["error"].as_str().unwrap(), "{name}");
            seen += 1;
        }
        assert!(seen >= 15, "found only {seen} golden error files");
    }

    #[tokio::test]
    async fn needs_setup_error_keeps_its_commands() {
        let (srv, api) = setup().await;
        let body =
            r#"{"error":"One-time setup is needed on this computer.","code":"needs_setup","commands":["nft add table inet splitter"]}"#;
        srv.reply("POST", "/api/v1/connect", 409, body);
        let e = api.connect().await.unwrap_err();
        assert_eq!(e.code, "needs_setup");
        assert_eq!(e.commands.as_deref(), Some(&["nft add table inet splitter".to_string()][..]));
    }

    #[tokio::test]
    async fn status_only_errors_without_a_json_body_are_mapped_by_status() {
        let (srv, api) = setup().await;
        srv.reply("GET", "/api/v1/state", 503, "");
        assert_eq!(api.state().await.unwrap_err().code, "engine_unreachable");
        srv.reply("GET", "/api/v1/state", 500, "<html>");
        assert_eq!(api.state().await.unwrap_err().code, "internal");
    }

    #[tokio::test]
    async fn a_401_means_the_engine_is_not_ours_and_leaks_nothing() {
        let srv = FakeServer::start(TOKEN).await;
        let wrong = "wrong-token-wrong-token-wrong-token-0001";
        let api = Api::new(Client::new(srv.addr, wrong).unwrap());
        let e = api.state().await.unwrap_err();
        assert!(e.is_engine_lost());
        let shown = format!("{e} {e:?} {}", serde_json::to_string(&e).unwrap());
        assert!(!shown.contains(wrong) && !shown.contains(TOKEN));
        assert!(!format!("{api:?}").contains(wrong));
    }

    #[tokio::test]
    async fn pasted_input_never_appears_in_errors() {
        let (srv, api) = setup().await;
        srv.reply("POST", "/api/v1/servers", 422, &testdata("error_server_invalid.golden.json"));
        let marker = "vless://MARKER-SECRET-1234@example.com:443";
        let e = api.add_server(marker, None).await.unwrap_err();
        assert_eq!(e.code, "invalid_input");
        assert!(!format!("{e:?}").contains("MARKER-SECRET"));
    }

    #[tokio::test]
    async fn malformed_success_bodies_are_rejected_not_forwarded() {
        let (srv, api) = setup().await;
        srv.reply("GET", "/api/v1/version", 200, r#"{"unexpected":true}"#);
        assert_eq!(api.version().await.unwrap_err().code, "engine_unreachable");
        srv.reply("GET", "/api/v1/state", 200, "not json");
        assert!(api.state().await.is_err());
    }
}
