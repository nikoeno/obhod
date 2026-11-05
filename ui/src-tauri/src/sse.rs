//! Server-Sent Events: a byte-level parser and the reconnecting reader that forwards engine events
//! to the frontend (`api/desktop/API.md`, Events).
//!
//! The engine keeps no replay buffer: after a reconnect its first event is a full `state`, which the
//! frontend takes as the truth. Local events `stream-open` and `stream-lost` tell the frontend when
//! the stream came back or broke, so it can refetch what events do not carry (the route table).

use std::sync::Arc;
use std::time::Duration;

use serde::Serialize;
use serde_json::{json, Value};
use tokio::sync::watch;
use tokio::time::{sleep, timeout};

use crate::http::{BodyReader, Client, ClientError};

const MAX_PENDING: usize = 1024 * 1024;

/// What the frontend receives on the `engine-event` Tauri event.
#[derive(Debug, Clone, PartialEq, Serialize)]
pub struct EngineEvent {
    /// `state`, `routes`, `check`, `traffic`, `shutdown` (from the engine) or `stream-open`,
    /// `stream-lost` (from this module).
    pub kind: String,
    pub data: Value,
}

impl EngineEvent {
    pub fn local(kind: &str, data: Value) -> Self {
        Self { kind: kind.to_string(), data }
    }
}

/// Where events go. The Tauri build emits them to the webview; tests collect them.
pub trait EventSink: Send + Sync {
    fn emit(&self, event: EngineEvent);
}

/// One parsed SSE event.
#[derive(Debug, Clone, PartialEq)]
pub struct SseEvent {
    pub id: Option<String>,
    pub event: String,
    pub data: String,
}

/// Incremental parser (WHATWG event stream format; `\n`, `\r\n` and `\r` line ends).
#[derive(Default)]
pub struct SseParser {
    buf: Vec<u8>,
    id: Option<String>,
    event: Option<String>,
    data: Vec<String>,
    retry_ms: Option<u64>,
}

impl SseParser {
    pub fn new() -> Self {
        Self::default()
    }

    /// The last `retry:` value the server sent.
    pub fn retry_ms(&self) -> Option<u64> {
        self.retry_ms
    }

    /// Adds bytes and returns the events completed by them.
    pub fn feed(&mut self, bytes: &[u8]) -> Vec<SseEvent> {
        self.buf.extend_from_slice(bytes);
        let mut out = Vec::new();
        while let Some((end, skip)) = self.next_line_end() {
            let line: Vec<u8> = self.buf.drain(..end + skip).take(end).collect();
            let line = String::from_utf8_lossy(&line).into_owned();
            if let Some(ev) = self.line(&line) {
                out.push(ev);
            }
        }
        if self.buf.len() > MAX_PENDING {
            // A line that never ends: drop it instead of growing without bound.
            self.buf.clear();
        }
        out
    }

    /// Position of the first complete line end: (index of the terminator, terminator length).
    fn next_line_end(&self) -> Option<(usize, usize)> {
        for (i, &b) in self.buf.iter().enumerate() {
            if b == b'\n' {
                return Some((i, 1));
            }
            if b == b'\r' {
                return match self.buf.get(i + 1) {
                    Some(b'\n') => Some((i, 2)),
                    Some(_) => Some((i, 1)),
                    None => None, // may be the first half of \r\n
                };
            }
        }
        None
    }

    fn line(&mut self, line: &str) -> Option<SseEvent> {
        if line.is_empty() {
            let had_data = !self.data.is_empty();
            let ev = SseEvent {
                id: self.id.clone(),
                event: self.event.take().unwrap_or_else(|| "message".to_string()),
                data: self.data.join("\n"),
            };
            self.data.clear();
            return had_data.then_some(ev);
        }
        if line.starts_with(':') {
            return None; // comment / keep-alive ping
        }
        let (field, value) = match line.split_once(':') {
            Some((f, v)) => (f, v.strip_prefix(' ').unwrap_or(v)),
            None => (line, ""),
        };
        match field {
            "event" => self.event = Some(value.to_string()),
            "data" => self.data.push(value.to_string()),
            "id" => self.id = Some(value.to_string()),
            "retry" => {
                if let Ok(ms) = value.parse::<u64>() {
                    self.retry_ms = Some(ms);
                }
            }
            _ => {}
        }
        None
    }
}

/// Timing of the reader. Tests use small values.
#[derive(Debug, Clone, Copy)]
pub struct LoopConfig {
    /// No bytes at all (not even a ping) for this long: the stream is dead.
    pub idle_timeout: Duration,
    /// Reconnect delay when the server sent no `retry:`.
    pub default_retry: Duration,
    /// The delay grows with consecutive failures but never beyond this.
    pub max_backoff: Duration,
}

impl Default for LoopConfig {
    fn default() -> Self {
        Self { idle_timeout: Duration::from_secs(45), default_retry: Duration::from_secs(2), max_backoff: Duration::from_secs(10) }
    }
}

/// Why `run_event_loop` returned.
#[derive(Debug, PartialEq, Eq)]
pub enum LoopEnd {
    /// The stop signal fired.
    Stopped,
    /// The engine sent its final `shutdown` event.
    EngineShutdown,
    /// The engine refused the token; reconnecting cannot help.
    Unauthorized,
}

enum Session {
    Stopped,
    Shutdown,
    Lost,
}

/// Reads `/api/v1/events` until stopped, reconnecting after every break.
pub async fn run_event_loop(client: Client, sink: Arc<dyn EventSink>, mut stop: watch::Receiver<bool>, cfg: LoopConfig) -> LoopEnd {
    let mut failures: u32 = 0;
    let mut retry = cfg.default_retry;
    let mut announced_lost = false;
    loop {
        if *stop.borrow() {
            return LoopEnd::Stopped;
        }
        match client.open_stream("/api/v1/events").await {
            Ok(mut body) => {
                failures = 0;
                sink.emit(EngineEvent::local("stream-open", json!({})));
                let mut parser = SseParser::new();
                let s = session(&mut body, &mut parser, sink.as_ref(), &mut stop, cfg.idle_timeout).await;
                if let Some(ms) = parser.retry_ms() {
                    retry = Duration::from_millis(ms);
                }
                match s {
                    Session::Stopped => return LoopEnd::Stopped,
                    Session::Shutdown => return LoopEnd::EngineShutdown,
                    Session::Lost => {
                        sink.emit(EngineEvent::local("stream-lost", json!({})));
                        announced_lost = true;
                    }
                }
            }
            Err(ClientError::Status(r)) if r.status == 401 => {
                sink.emit(EngineEvent::local("stream-lost", json!({ "code": "engine_unreachable" })));
                return LoopEnd::Unauthorized;
            }
            Err(_) => {
                failures = failures.saturating_add(1);
                if !announced_lost {
                    sink.emit(EngineEvent::local("stream-lost", json!({})));
                    announced_lost = true;
                }
            }
        }
        let delay = (retry * failures.clamp(1, 4)).min(cfg.max_backoff.max(retry));
        tokio::select! {
            _ = sleep(delay) => {}
            r = stop.changed() => {
                if r.is_err() {
                    return LoopEnd::Stopped; // the owner is gone
                }
            }
        }
    }
}

async fn session(
    body: &mut BodyReader,
    parser: &mut SseParser,
    sink: &dyn EventSink,
    stop: &mut watch::Receiver<bool>,
    idle: Duration,
) -> Session {
    loop {
        if *stop.borrow() {
            return Session::Stopped;
        }
        let next = tokio::select! {
            r = stop.changed() => {
                if r.is_err() {
                    return Session::Stopped; // the owner is gone
                }
                continue;
            }
            r = timeout(idle, body.next()) => r,
        };
        let bytes = match next {
            Ok(Ok(Some(b))) => b,
            _ => return Session::Lost,
        };
        for ev in parser.feed(&bytes) {
            // Events are JSON by contract; anything else is dropped without being logged.
            let Ok(data) = serde_json::from_str::<Value>(&ev.data) else { continue };
            let is_shutdown = ev.event == "shutdown";
            sink.emit(EngineEvent { kind: ev.event, data });
            if is_shutdown {
                return Session::Shutdown;
            }
        }
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use crate::testutil::{After, FakeServer};
    use std::sync::Mutex;

    const TOKEN: &str = "sse-token-sse-token-sse-token-sse-token-01";

    #[derive(Default)]
    struct Collect(Mutex<Vec<EngineEvent>>);
    impl EventSink for Collect {
        fn emit(&self, e: EngineEvent) {
            self.0.lock().unwrap().push(e);
        }
    }
    impl Collect {
        fn kinds(&self) -> Vec<String> {
            self.0.lock().unwrap().iter().map(|e| e.kind.clone()).collect()
        }
    }

    fn fast() -> LoopConfig {
        LoopConfig {
            idle_timeout: Duration::from_millis(500),
            default_retry: Duration::from_millis(20),
            max_backoff: Duration::from_millis(100),
        }
    }

    #[test]
    fn parses_events_split_across_feeds_with_crlf_and_comments() {
        let mut p = SseParser::new();
        assert!(p.feed(b"retry: 2000\r\n\r\n: ping\n\nid: 7\nevent: sta").is_empty());
        assert_eq!(p.retry_ms(), Some(2000));
        let evs = p.feed(b"te\ndata: {\"a\":1}\n\n");
        assert_eq!(evs, vec![SseEvent { id: Some("7".into()), event: "state".into(), data: "{\"a\":1}".into() }]);
    }

    #[test]
    fn multi_line_data_default_event_name_and_lone_cr() {
        let mut p = SseParser::new();
        let evs = p.feed(b"data: a\rdata: b\r\r\n");
        assert_eq!(evs.len(), 1);
        assert_eq!(evs[0].event, "message");
        assert_eq!(evs[0].data, "a\nb");
        // A terminator split between feeds is not lost.
        let mut p = SseParser::new();
        assert!(p.feed(b"data: x\r").is_empty());
        let evs = p.feed(b"\n\r\n");
        assert_eq!(evs[0].data, "x");
    }

    #[test]
    fn ignores_events_without_data_and_unknown_fields() {
        let mut p = SseParser::new();
        assert!(p.feed(b"event: state\n\nfoo: bar\n\n").is_empty());
        let evs = p.feed(b"event: ok\ndata\n\n");
        assert_eq!(evs[0].data, "");
    }

    #[test]
    fn runaway_line_is_dropped() {
        let mut p = SseParser::new();
        p.feed(&vec![b'x'; MAX_PENDING + 1]);
        assert_eq!(p.feed(b"data: 1\n\n")[0].data, "1");
    }

    #[tokio::test]
    async fn forwards_events_and_stops_at_engine_shutdown() {
        let srv = FakeServer::start(TOKEN).await;
        srv.script_events(
            vec![
                "retry: 2000\n\n",
                "id: 1\nevent: state\ndata: {\"state\":\"disconnected\"}\n\n: ping\n\n",
                "id: 2\nevent: traffic\ndata: {\"uploadTotal\":1}\n\nid: 3\nevent: bad\ndata: not json\n\n",
                "id: 4\nevent: shutdown\ndata: {}\n\n",
            ],
            After::Hold,
        );
        let sink = Arc::new(Collect::default());
        let (_tx, rx) = watch::channel(false);
        let end = run_event_loop(Client::new(srv.addr, TOKEN).unwrap(), sink.clone(), rx, fast()).await;
        assert_eq!(end, LoopEnd::EngineShutdown);
        assert_eq!(sink.kinds(), ["stream-open", "state", "traffic", "shutdown"]);
        assert_eq!(sink.0.lock().unwrap()[1].data["state"], "disconnected");
    }

    #[tokio::test]
    async fn the_degraded_state_event_keeps_the_additive_tunnel_field() {
        let srv = FakeServer::start(TOKEN).await;
        let golden: serde_json::Value = serde_json::from_str(&crate::testutil::testdata("state_degraded.golden.json")).unwrap();
        let chunk = format!("id: 1
event: state
data: {}

", serde_json::to_string(&golden).unwrap());
        srv.script_events(vec![chunk.as_str(), "id: 2
event: shutdown
data: {}

"], After::Hold);
        let sink = Arc::new(Collect::default());
        let (_tx, rx) = watch::channel(false);
        run_event_loop(Client::new(srv.addr, TOKEN).unwrap(), sink.clone(), rx, fast()).await;
        let got = sink.0.lock().unwrap()[1].data.clone();
        assert_eq!(got, golden);
        assert_eq!(got["snapshot"]["Tunnel"], "none");
    }

    #[tokio::test]
    async fn a_state_event_keeps_the_additive_problems_list() {
        let srv = FakeServer::start(TOKEN).await;
        let golden: serde_json::Value = serde_json::from_str(&crate::testutil::testdata("state_problems.golden.json")).unwrap();
        let chunk = format!("id: 1
event: state
data: {}

", serde_json::to_string(&golden).unwrap());
        srv.script_events(vec![chunk.as_str(), "id: 2
event: shutdown
data: {}

"], After::Hold);
        let sink = Arc::new(Collect::default());
        let (_tx, rx) = watch::channel(false);
        run_event_loop(Client::new(srv.addr, TOKEN).unwrap(), sink.clone(), rx, fast()).await;
        let got = sink.0.lock().unwrap()[1].data.clone();
        assert_eq!(got, golden);
        assert_eq!(got["snapshot"]["Problems"][0]["Code"], "desync_failed");
        assert_eq!(got["snapshot"]["Problems"].as_array().unwrap().len(), 2);
    }

    #[tokio::test]
    async fn reconnects_after_the_stream_breaks_and_resyncs_with_a_new_state() {
        let srv = FakeServer::start(TOKEN).await;
        srv.script_events(vec!["event: state\ndata: {\"state\":\"connecting\"}\n\n"], After::Close);
        srv.script_events(vec!["event: state\ndata: {\"state\":\"connected\"}\n\n"], After::Hold);
        let sink = Arc::new(Collect::default());
        let (tx, rx) = watch::channel(false);
        let task = tokio::spawn(run_event_loop(Client::new(srv.addr, TOKEN).unwrap(), sink.clone(), rx, fast()));
        for _ in 0..200 {
            if sink.kinds().iter().filter(|k| *k == "state").count() == 2 {
                break;
            }
            sleep(Duration::from_millis(10)).await;
        }
        assert_eq!(sink.kinds(), ["stream-open", "state", "stream-lost", "stream-open", "state"]);
        assert_eq!(sink.0.lock().unwrap()[4].data["state"], "connected");
        tx.send(true).unwrap();
        assert_eq!(timeout(Duration::from_secs(2), task).await.unwrap().unwrap(), LoopEnd::Stopped);
        assert_eq!(srv.count("GET", "/api/v1/events"), 2);
    }

    #[tokio::test]
    async fn keeps_retrying_while_the_engine_is_down_but_announces_loss_once() {
        let l = tokio::net::TcpListener::bind("127.0.0.1:0").await.unwrap();
        let addr = l.local_addr().unwrap();
        drop(l);
        let sink = Arc::new(Collect::default());
        let (tx, rx) = watch::channel(false);
        let task = tokio::spawn(run_event_loop(Client::new(addr, TOKEN).unwrap(), sink.clone(), rx, fast()));
        sleep(Duration::from_millis(250)).await;
        tx.send(true).unwrap();
        assert_eq!(timeout(Duration::from_secs(2), task).await.unwrap().unwrap(), LoopEnd::Stopped);
        assert_eq!(sink.kinds(), ["stream-lost"]);
    }

    #[tokio::test]
    async fn rejected_token_ends_the_loop() {
        let srv = FakeServer::start(TOKEN).await;
        let sink = Arc::new(Collect::default());
        let (_tx, rx) = watch::channel(false);
        let end = run_event_loop(Client::new(srv.addr, "other-token-other-token-other-token-00").unwrap(), sink.clone(), rx, fast()).await;
        assert_eq!(end, LoopEnd::Unauthorized);
        let evs = sink.0.lock().unwrap();
        assert_eq!(evs[0].kind, "stream-lost");
        assert_eq!(evs[0].data["code"], "engine_unreachable");
        assert!(!format!("{evs:?}").contains(TOKEN));
    }

    #[tokio::test]
    async fn silent_stream_is_treated_as_lost() {
        let srv = FakeServer::start(TOKEN).await;
        srv.script_events(vec!["event: state\ndata: {}\n\n"], After::Hold);
        srv.script_events(vec!["event: state\ndata: {}\n\n"], After::Hold);
        let sink = Arc::new(Collect::default());
        let (tx, rx) = watch::channel(false);
        let cfg = LoopConfig { idle_timeout: Duration::from_millis(80), ..fast() };
        let task = tokio::spawn(run_event_loop(Client::new(srv.addr, TOKEN).unwrap(), sink.clone(), rx, cfg));
        sleep(Duration::from_millis(400)).await;
        tx.send(true).unwrap();
        timeout(Duration::from_secs(2), task).await.unwrap().unwrap();
        assert!(sink.kinds().contains(&"stream-lost".to_string()));
        assert!(srv.count("GET", "/api/v1/events") >= 2);
    }
}
