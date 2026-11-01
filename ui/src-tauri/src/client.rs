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

/// A per-site rule (`GET /sites`, answer of `POST /sites`). `action` is `direct`, `desync`, `tunnel` or `block`.
#[derive(Debug, Clone, PartialEq, Serialize, Deserialize)]
#[serde(rename_all = "camelCase")]
pub struct SiteRule {
    pub domain: String,
    pub action: String,
    pub added_at: String,
}

/// The actions a rule can have. "No rule" is made by deleting the rule.
pub const SITE_ACTIONS: [&str; 4] = ["direct", "desync", "tunnel", "block"];

/// Fixed sentences for failed site calls. The engine's own sentences are the same; these replace
/// whatever came back, so the answer to a site call never carries anything but a fixed sentence
/// (never the domain the user typed).
const SITE_INVALID: &str = "That is not a plain site name. Enter only the name, for example example.com.";
const SITE_LIMIT: &str = "The list of site rules is full. Remove a rule first.";
const SITE_NOT_FOUND: &str = "There is no rule for that site.";
const SITE_BAD_REQUEST: &str = "The engine did not accept this site rule.";

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
            return ApiError { code: b.code, message: b.error, commands: b.commands, details: None };
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

    /// `GET /sites`: the user's rules, sorted by domain.
    pub async fn sites(&self) -> Result<Vec<SiteRule>, ApiError> {
        parse(&self.raw("GET", "/sites", None).await?)
    }

    /// `POST /sites` (upsert). The domain is sent in the body only; it is never logged or echoed.
    pub async fn set_site(&self, domain: &str, action: &str) -> Result<SiteRule, ApiError> {
        let domain = site_domain(domain)?;
        if !SITE_ACTIONS.contains(&action) {
            return Err(ApiError::bad_request(SITE_BAD_REQUEST));
        }
        let body = json!({ "domain": domain, "action": action });
        parse(&self.raw("POST", "/sites", Some(&body)).await.map_err(site_error)?)
    }

    /// `DELETE /sites/{domain}`: back to the automatic behaviour.
    pub async fn delete_site(&self, domain: &str) -> Result<Ack, ApiError> {
        let domain = site_domain(domain)?;
        parse(&self.raw("DELETE", &format!("/sites/{}", path_segment(domain)), None).await.map_err(site_error)?)
    }

    pub async fn test_server(&self, req: &TestRequest) -> Result<ServerTest, ApiError> {
        if req.id.is_some() == req.input.is_some() {
            return Err(ApiError::bad_request("Send either an id or an input."));
        }
        let body = serde_json::to_value(req).map_err(|_| ApiError::internal())?;
        parse(&self.raw("POST", "/servers/test", Some(&body)).await?)
    }
}

/// Replaces the message of the codes site calls can fail with by a fixed sentence.
fn site_error(mut e: ApiError) -> ApiError {
    let fixed = match e.code.as_str() {
        "invalid_input" => SITE_INVALID,
        "limit_reached" => SITE_LIMIT,
        "not_found" => SITE_NOT_FOUND,
        "bad_request" => SITE_BAD_REQUEST,
        _ => return e,
    };
    e.message = fixed.to_string();
    e
}

/// A domain as sent to the engine: not empty, at most 253 bytes (the longest name), no control
/// characters or spaces. Everything else (labels, IP addresses, URLs) is the engine's 422.
fn site_domain(domain: &str) -> Result<&str, ApiError> {
    if domain.is_empty() || domain.len() > 253 || domain.chars().any(|c| c.is_control() || c.is_whitespace()) {
        return Err(ApiError::new("invalid_input", SITE_INVALID));
    }
    Ok(domain)
}

/// Percent-encodes everything but unreserved characters, so a name can only ever be one path segment.
fn path_segment(s: &str) -> String {
    let mut out = String::with_capacity(s.len());
    for b in s.bytes() {
        if b.is_ascii_alphanumeric() || matches!(b, b'-' | b'.' | b'_' | b'~') {
            out.push(b as char);
        } else {
            out.push_str(&format!("%{b:02X}"));
        }
    }
    out
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
    async fn the_additive_tunnel_and_degraded_fields_pass_through() {
        let (srv, api) = setup().await;
        let body = testdata("state_degraded.golden.json");
        srv.reply("GET", "/api/v1/state", 200, &body);
        let got = api.state().await.unwrap();
        assert_eq!(got["snapshot"]["Tunnel"], "none");
        assert_eq!(got, serde_json::from_str::<Value>(&body).unwrap());
        // The connected-with-a-server golden says "ready" and an older engine has no field at all: both pass through unchanged.
        srv.reply("GET", "/api/v1/state", 200, &testdata("state_connected.golden.json"));
        assert_eq!(api.state().await.unwrap()["snapshot"]["Tunnel"], "ready");

        let body = testdata("routes_degraded.golden.json");
        srv.reply("GET", "/api/v1/routes", 200, &body);
        let rows = api.routes().await.unwrap();
        let flagged: Vec<&str> = rows.as_array().unwrap().iter().filter(|r| r["degraded"] == true).map(|r| r["domain"].as_str().unwrap()).collect();
        assert_eq!(flagged, ["vpn-only.example"]);
        assert_eq!(rows, serde_json::from_str::<Value>(&body).unwrap());
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

    #[tokio::test]
    async fn site_rules_round_trip() {
        let (srv, api) = setup().await;
        srv.reply("GET", "/api/v1/sites", 200, &testdata("sites_filled.golden.json"));
        let list = api.sites().await.unwrap();
        assert_eq!(list.len(), 2);
        assert_eq!(list[1].action, "block");
        srv.reply("GET", "/api/v1/sites", 200, &testdata("sites_empty.golden.json"));
        assert!(api.sites().await.unwrap().is_empty());

        srv.reply("POST", "/api/v1/sites", 201, &testdata("site_created.golden.json"));
        let r = api.set_site("example.com", "tunnel").await.unwrap();
        assert_eq!((r.domain.as_str(), r.action.as_str()), ("example.com", "tunnel"));
        srv.reply("POST", "/api/v1/sites", 200, &testdata("site_updated.golden.json"));
        assert_eq!(api.set_site("example.com", "direct").await.unwrap().action, "direct");
        srv.reply("DELETE", "/api/v1/sites/example.com", 200, &testdata("site_deleted.golden.json"));
        assert!(api.delete_site("example.com").await.unwrap().ok);

        let reqs = srv.requests();
        let posts: Vec<_> = reqs.iter().filter(|r| r.method == "POST").collect();
        let sent: Value = serde_json::from_slice(&posts[0].body).unwrap();
        assert_eq!(sent, json!({ "domain": "example.com", "action": "tunnel" }));
        assert_eq!(reqs.last().unwrap().path, "/api/v1/sites/example.com");
        assert!(reqs.last().unwrap().body.is_empty());
    }

    #[tokio::test]
    async fn site_errors_map_to_fixed_sentences() {
        let (srv, api) = setup().await;
        let cases = [
            ("POST", "/api/v1/sites", 422, "error_site_invalid_domain.golden.json", "invalid_input"),
            ("POST", "/api/v1/sites", 409, "error_site_limit.golden.json", "limit_reached"),
            ("DELETE", "/api/v1/sites/my-site.test", 404, "error_site_not_found.golden.json", "not_found"),
            ("POST", "/api/v1/sites", 400, "error_site_domain_missing.golden.json", "bad_request"),
        ];
        for (method, path, status, golden, code) in cases {
            srv.reply(method, path, status, &testdata(golden));
            let e = if method == "POST" {
                api.set_site("my-site.test", "block").await.unwrap_err()
            } else {
                api.delete_site("my-site.test").await.unwrap_err()
            };
            assert_eq!(e.code, code, "{golden}");
            assert!(!e.message.contains("my-site"), "{golden}");
        }
        // A hostile engine body cannot put its own text (or the typed name) into the message.
        srv.reply("POST", "/api/v1/sites", 422, r#"{"error":"bad: secret-site.example","code":"invalid_input"}"#);
        let e = api.set_site("secret-site.example", "block").await.unwrap_err();
        assert!(!format!("{e:?}").contains("secret-site"));
        // Other codes keep the engine's sentence; a lost engine stays recognisable.
        srv.reply("POST", "/api/v1/sites", 500, &testdata("error_site_internal.golden.json"));
        assert_eq!(api.set_site("example.com", "block").await.unwrap_err().code, "internal");
        srv.reply("POST", "/api/v1/sites", 503, "");
        assert!(api.set_site("example.com", "block").await.unwrap_err().is_engine_lost());
    }

    #[tokio::test]
    async fn bad_site_arguments_never_reach_the_engine() {
        let (srv, api) = setup().await;
        let long = "a".repeat(254);
        for bad in ["", "a b.example", "x\n.example", long.as_str()] {
            assert_eq!(api.set_site(bad, "block").await.unwrap_err().code, "invalid_input");
            assert_eq!(api.delete_site(bad).await.unwrap_err().code, "invalid_input");
        }
        for bad in ["", "none", "proxy", "BLOCK"] {
            assert_eq!(api.set_site("example.com", bad).await.unwrap_err().code, "bad_request", "{bad}");
        }
        assert!(srv.requests().is_empty());
    }

    #[test]
    fn site_names_stay_one_path_segment() {
        assert_eq!(path_segment("example.com"), "example.com");
        assert_eq!(path_segment("a/b?c#d"), "a%2Fb%3Fc%23d");
        assert_eq!(path_segment("../x"), "..%2Fx");
        assert_eq!(path_segment("\u{43f}"), "%D0%BF");
    }

    #[tokio::test]
    async fn site_calls_do_not_leak_the_token() {
        let (srv, api) = setup().await;
        srv.reply("POST", "/api/v1/sites", 422, &testdata("error_site_invalid_domain.golden.json"));
        let e = api.set_site("example.com", "block").await.unwrap_err();
        let shown = format!("{e} {e:?} {}", serde_json::to_string(&e).unwrap());
        assert!(!shown.contains(TOKEN));
    }
}
