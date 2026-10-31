//! Test support: an in-process fake of the control API (`api/desktop/API.md`) on a loopback
//! socket. It checks the bearer token and the `Host` header like the real gateway, records every
//! request, answers canned JSON and plays scripted event streams.

use std::collections::{HashMap, VecDeque};
use std::net::SocketAddr;
use std::path::PathBuf;
use std::sync::{Arc, Mutex};
use std::time::Duration;

use tokio::io::{AsyncReadExt, AsyncWriteExt};
use tokio::net::{TcpListener, TcpStream};

#[derive(Debug, Clone)]
pub struct Recorded {
    pub method: String,
    pub path: String,
    pub headers: Vec<(String, String)>,
    pub body: Vec<u8>,
}

impl Recorded {
    pub fn header(&self, name: &str) -> Option<&str> {
        self.headers.iter().find(|(k, _)| k == name).map(|(_, v)| v.as_str())
    }
}

/// What a scripted event stream does after its chunks.
#[derive(Debug, Clone, Copy)]
pub enum After {
    /// End the chunked body properly and close.
    Close,
    /// Keep the connection open (until the client goes away).
    Hold,
}

struct Script {
    chunks: Vec<String>,
    after: After,
}

struct Inner {
    token: Mutex<String>,
    replies: Mutex<HashMap<String, (u16, String)>>,
    requests: Mutex<Vec<Recorded>>,
    scripts: Mutex<VecDeque<Script>>,
    addr: Mutex<Option<SocketAddr>>,
}

pub struct FakeServer {
    pub addr: SocketAddr,
    inner: Arc<Inner>,
    task: tokio::task::JoinHandle<()>,
}

impl Drop for FakeServer {
    fn drop(&mut self) {
        self.task.abort();
    }
}

/// Path of the golden JSON files the Go gateway tests produce.
pub fn testdata(name: &str) -> String {
    let p: PathBuf = [env!("CARGO_MANIFEST_DIR"), "..", "..", "api", "desktop", "testdata", name].iter().collect();
    std::fs::read_to_string(&p).unwrap_or_else(|e| panic!("golden file {}: {e}", p.display()))
}

impl FakeServer {
    pub async fn start(token: &str) -> Self {
        let listener = TcpListener::bind("127.0.0.1:0").await.unwrap();
        let addr = listener.local_addr().unwrap();
        let inner = Arc::new(Inner {
            token: Mutex::new(token.to_string()),
            replies: Mutex::new(HashMap::new()),
            requests: Mutex::new(Vec::new()),
            scripts: Mutex::new(VecDeque::new()),
            addr: Mutex::new(Some(addr)),
        });
        let task = {
            let inner = inner.clone();
            tokio::spawn(async move {
                loop {
                    let Ok((sock, _)) = listener.accept().await else { return };
                    let inner = inner.clone();
                    tokio::spawn(async move {
                        let _ = handle(sock, inner).await;
                    });
                }
            })
        };
        Self { addr, inner, task }
    }

    /// Canned answer for `METHOD path` (query string ignored when matching).
    pub fn reply(&self, method: &str, path: &str, status: u16, body: &str) {
        self.inner.replies.lock().unwrap().insert(format!("{method} {path}"), (status, body.to_string()));
    }

    /// Queues one event-stream connection: the chunks are sent as separate HTTP chunks.
    pub fn script_events(&self, chunks: Vec<&str>, after: After) {
        self.inner.scripts.lock().unwrap().push_back(Script { chunks: chunks.into_iter().map(String::from).collect(), after });
    }

    /// Changes the token the fake accepts (the real engine reads it from the token file).
    pub fn set_token(&self, token: &str) {
        *self.inner.token.lock().unwrap() = token.to_string();
    }

    pub fn requests(&self) -> Vec<Recorded> {
        self.inner.requests.lock().unwrap().clone()
    }

    pub fn count(&self, method: &str, path: &str) -> usize {
        self.requests().iter().filter(|r| r.method == method && r.path == path).count()
    }
}

fn reason(status: u16) -> &'static str {
    match status {
        200 => "OK",
        201 => "Created",
        202 => "Accepted",
        400 => "Bad Request",
        401 => "Unauthorized",
        403 => "Forbidden",
        404 => "Not Found",
        409 => "Conflict",
        429 => "Too Many Requests",
        503 => "Service Unavailable",
        _ => "Status",
    }
}

async fn write_simple(sock: &mut TcpStream, status: u16, extra: &str, body: &str) -> std::io::Result<()> {
    let head = format!(
        "HTTP/1.1 {status} {}\r\n{extra}Content-Type: application/json; charset=utf-8\r\nContent-Length: {}\r\nConnection: close\r\n\r\n",
        reason(status),
        body.len()
    );
    sock.write_all(head.as_bytes()).await?;
    sock.write_all(body.as_bytes()).await?;
    sock.shutdown().await
}

async fn handle(mut sock: TcpStream, inner: Arc<Inner>) -> std::io::Result<()> {
    // Read head.
    let mut buf = Vec::new();
    let mut tmp = [0u8; 2048];
    let head_end = loop {
        if let Some(p) = buf.windows(4).position(|w| w == b"\r\n\r\n") {
            break p + 4;
        }
        let n = sock.read(&mut tmp).await?;
        if n == 0 {
            return Ok(());
        }
        buf.extend_from_slice(&tmp[..n]);
    };
    let head = String::from_utf8_lossy(&buf[..head_end]).to_string();
    let mut lines = head.split("\r\n");
    let first = lines.next().unwrap_or("");
    let mut parts = first.split(' ');
    let method = parts.next().unwrap_or("").to_string();
    let target = parts.next().unwrap_or("").to_string();
    let path = target.split('?').next().unwrap_or("").to_string();
    let headers: Vec<(String, String)> = lines
        .filter(|l| !l.is_empty())
        .filter_map(|l| l.split_once(':').map(|(k, v)| (k.trim().to_ascii_lowercase(), v.trim().to_string())))
        .collect();
    let len: usize = headers.iter().find(|(k, _)| k == "content-length").and_then(|(_, v)| v.parse().ok()).unwrap_or(0);
    let mut body = buf[head_end..].to_vec();
    while body.len() < len {
        let n = sock.read(&mut tmp).await?;
        if n == 0 {
            break;
        }
        body.extend_from_slice(&tmp[..n]);
    }
    let rec = Recorded { method: method.clone(), path: path.clone(), headers: headers.clone(), body };
    inner.requests.lock().unwrap().push(rec.clone());

    // Host guard, then token (same order as the real gateway).
    let addr = inner.addr.lock().unwrap().unwrap();
    if rec.header("host") != Some(addr.to_string().as_str()) {
        return write_simple(&mut sock, 403, "", r#"{"error":"Forbidden.","code":"forbidden"}"#).await;
    }
    let want = format!("Bearer {}", inner.token.lock().unwrap());
    if rec.header("authorization") != Some(want.as_str()) {
        return write_simple(&mut sock, 401, "WWW-Authenticate: Bearer\r\n", "").await;
    }

    if method == "GET" && path == "/api/v1/events" {
        let script = inner.scripts.lock().unwrap().pop_front();
        if let Some(s) = script {
            sock.write_all(b"HTTP/1.1 200 OK\r\nContent-Type: text/event-stream; charset=utf-8\r\nCache-Control: no-store\r\nTransfer-Encoding: chunked\r\nConnection: close\r\n\r\n").await?;
            for c in &s.chunks {
                sock.write_all(format!("{:x}\r\n{c}\r\n", c.len()).as_bytes()).await?;
                sock.flush().await?;
                tokio::time::sleep(Duration::from_millis(5)).await;
            }
            match s.after {
                After::Close => {
                    sock.write_all(b"0\r\n\r\n").await?;
                    return sock.shutdown().await;
                }
                After::Hold => {
                    // Wait until the client closes.
                    let mut b = [0u8; 16];
                    while sock.read(&mut b).await.unwrap_or(0) > 0 {}
                    return Ok(());
                }
            }
        }
    }

    let reply = inner.replies.lock().unwrap().get(&format!("{method} {path}")).cloned();
    match reply {
        Some((status, body)) => write_simple(&mut sock, status, "", &body).await,
        None => write_simple(&mut sock, 404, "", r#"{"error":"Not found.","code":"not_found"}"#).await,
    }
}
