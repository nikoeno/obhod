//! A tiny HTTP/1.1 client for the control API: loopback only, bearer token, one request per
//! connection (`Connection: close`), plain or chunked bodies. It exists because the contract is
//! small (JSON and one event stream over loopback) and a full HTTP stack is a large dependency.
//!
//! Secrets: the token is wrapped in [`Secret`] (redacted `Debug`), is written only into the request
//! head, and no error value contains request bytes.

use std::fmt;
use std::io;
use std::net::SocketAddr;
use std::sync::Arc;
use std::time::Duration;

use tokio::io::{AsyncBufReadExt, AsyncReadExt, AsyncWriteExt, BufReader};
use tokio::net::TcpStream;
use tokio::time::timeout;

const MAX_HEAD: usize = 16 * 1024;
const MAX_BODY: usize = 8 * 1024 * 1024;
const READ_STEP: usize = 8 * 1024;

/// A value that must never be printed.
pub struct Secret(String);

impl Secret {
    pub fn new(s: impl Into<String>) -> Self {
        Self(s.into())
    }
    fn expose(&self) -> &str {
        &self.0
    }
}

impl fmt::Debug for Secret {
    fn fmt(&self, f: &mut fmt::Formatter<'_>) -> fmt::Result {
        f.write_str("Secret(<redacted>)")
    }
}

/// Why a request failed before an HTTP status could be interpreted.
#[derive(Debug)]
pub enum ClientError {
    /// Nothing is listening, or the connection broke.
    Unreachable,
    /// The connect or the whole request took too long.
    Timeout,
    /// The peer did not speak the expected HTTP.
    Protocol(&'static str),
    /// `open_stream` got a non-200 answer; the (small) response is attached.
    Status(Response),
}

impl From<io::Error> for ClientError {
    fn from(_: io::Error) -> Self {
        ClientError::Unreachable
    }
}

/// A complete response.
#[derive(Debug, Clone)]
pub struct Response {
    pub status: u16,
    /// Header names are lower-case.
    pub headers: Vec<(String, String)>,
    pub body: Vec<u8>,
}

impl Response {
    pub fn header(&self, name: &str) -> Option<&str> {
        self.headers.iter().find(|(k, _)| k == name).map(|(_, v)| v.as_str())
    }
}

/// Connection settings for one engine.
#[derive(Clone)]
pub struct Client {
    addr: SocketAddr,
    token: Arc<Secret>,
    connect_timeout: Duration,
    request_timeout: Duration,
}

impl fmt::Debug for Client {
    fn fmt(&self, f: &mut fmt::Formatter<'_>) -> fmt::Result {
        f.debug_struct("Client").field("addr", &self.addr).field("token", &"<redacted>").finish()
    }
}

/// Parses `host:port` from the ready line or the address file. Only loopback IP literals pass,
/// so a tampered address file can never make the app send the token to another host.
pub fn parse_loopback_addr(s: &str) -> Option<SocketAddr> {
    let addr: SocketAddr = s.trim().parse().ok()?;
    (addr.ip().is_loopback() && addr.port() != 0).then_some(addr)
}

impl Client {
    /// `None` if `addr` is not a loopback address.
    pub fn new(addr: SocketAddr, token: impl Into<String>) -> Option<Self> {
        if !addr.ip().is_loopback() {
            return None;
        }
        Some(Self {
            addr,
            token: Arc::new(Secret::new(token)),
            connect_timeout: Duration::from_secs(3),
            request_timeout: Duration::from_secs(20),
        })
    }

    pub fn with_timeouts(mut self, connect: Duration, request: Duration) -> Self {
        self.connect_timeout = connect;
        self.request_timeout = request;
        self
    }

    pub fn addr(&self) -> SocketAddr {
        self.addr
    }

    fn head(&self, method: &str, path: &str, body: Option<&[u8]>, stream: bool) -> Result<Vec<u8>, ClientError> {
        if !path.starts_with('/') || !path.bytes().all(|b| b.is_ascii_graphic()) {
            return Err(ClientError::Protocol("bad request path"));
        }
        let accept = if stream { "text/event-stream" } else { "application/json" };
        let mut h = format!(
            "{method} {path} HTTP/1.1\r\nHost: {}\r\nAuthorization: Bearer {}\r\nAccept: {accept}\r\nConnection: close\r\n",
            self.addr,
            self.token.expose()
        );
        match body {
            Some(b) => h.push_str(&format!("Content-Type: application/json\r\nContent-Length: {}\r\n", b.len())),
            None if method == "POST" => h.push_str("Content-Length: 0\r\n"),
            None => {}
        }
        h.push_str("\r\n");
        Ok(h.into_bytes())
    }

    async fn connect_and_send(
        &self,
        method: &str,
        path: &str,
        body: Option<&[u8]>,
        stream: bool,
    ) -> Result<(u16, Vec<(String, String)>, BodyReader), ClientError> {
        let head = self.head(method, path, body, stream)?;
        let sock = timeout(self.connect_timeout, TcpStream::connect(self.addr)).await.map_err(|_| ClientError::Timeout)??;
        let mut reader = BufReader::new(sock);
        reader.write_all(&head).await?;
        if let Some(b) = body {
            reader.write_all(b).await?;
        }
        reader.flush().await?;
        let (status, headers) = read_head(&mut reader).await?;
        let mode = body_mode(status, &headers)?;
        Ok((status, headers, BodyReader { r: reader, mode, done: false }))
    }

    /// One request, whole response. `body` is sent as JSON when present.
    pub async fn send(&self, method: &str, path: &str, body: Option<&[u8]>) -> Result<Response, ClientError> {
        let fut = async {
            let (status, headers, mut br) = self.connect_and_send(method, path, body, false).await?;
            let mut out = Vec::new();
            while let Some(chunk) = br.next().await? {
                if out.len() + chunk.len() > MAX_BODY {
                    return Err(ClientError::Protocol("response too large"));
                }
                out.extend_from_slice(&chunk);
            }
            Ok(Response { status, headers, body: out })
        };
        timeout(self.request_timeout, fut).await.map_err(|_| ClientError::Timeout)?
    }

    /// Opens a GET event stream. A non-200 answer is `Err(ClientError::Status(..))`.
    pub async fn open_stream(&self, path: &str) -> Result<BodyReader, ClientError> {
        let fut = async {
            let (status, headers, mut br) = self.connect_and_send("GET", path, None, true).await?;
            if status != 200 {
                let mut out = Vec::new();
                while let Some(chunk) = br.next().await? {
                    out.extend_from_slice(&chunk);
                    if out.len() > 64 * 1024 {
                        break;
                    }
                }
                return Err(ClientError::Status(Response { status, headers, body: out }));
            }
            Ok(br)
        };
        timeout(self.request_timeout, fut).await.map_err(|_| ClientError::Timeout)?
    }
}

async fn read_line_limited(r: &mut BufReader<TcpStream>, used: &mut usize) -> Result<String, ClientError> {
    let mut line = Vec::new();
    let n = r.read_until(b'\n', &mut line).await?;
    if n == 0 {
        return Err(ClientError::Unreachable);
    }
    *used += n;
    if *used > MAX_HEAD {
        return Err(ClientError::Protocol("header too large"));
    }
    String::from_utf8(line).map_err(|_| ClientError::Protocol("bad header encoding"))
}

async fn read_head(r: &mut BufReader<TcpStream>) -> Result<(u16, Vec<(String, String)>), ClientError> {
    let mut used = 0;
    let status_line = read_line_limited(r, &mut used).await?;
    let mut parts = status_line.trim_end().splitn(3, ' ');
    let version = parts.next().unwrap_or("");
    if version != "HTTP/1.1" && version != "HTTP/1.0" {
        return Err(ClientError::Protocol("not HTTP"));
    }
    let status: u16 = parts.next().and_then(|s| s.parse().ok()).ok_or(ClientError::Protocol("bad status line"))?;
    let mut headers = Vec::new();
    loop {
        let line = read_line_limited(r, &mut used).await?;
        let line = line.trim_end();
        if line.is_empty() {
            break;
        }
        let (k, v) = line.split_once(':').ok_or(ClientError::Protocol("bad header line"))?;
        headers.push((k.trim().to_ascii_lowercase(), v.trim().to_string()));
    }
    Ok((status, headers))
}

#[derive(Debug, Clone, Copy)]
enum Mode {
    Length(u64),
    /// Bytes left in the current chunk (0 = read the next chunk header).
    Chunked(u64),
    UntilClose,
}

fn body_mode(status: u16, headers: &[(String, String)]) -> Result<Mode, ClientError> {
    if status == 204 || status == 304 || (100..200).contains(&status) {
        return Ok(Mode::Length(0));
    }
    let get = |n: &str| headers.iter().find(|(k, _)| k == n).map(|(_, v)| v.as_str());
    if get("transfer-encoding").is_some_and(|v| v.to_ascii_lowercase().contains("chunked")) {
        return Ok(Mode::Chunked(0));
    }
    match get("content-length") {
        Some(v) => v.parse().map(Mode::Length).map_err(|_| ClientError::Protocol("bad content-length")),
        None => Ok(Mode::UntilClose),
    }
}

/// The body of one response, read piece by piece (plain, chunked or until close).
#[derive(Debug)]
pub struct BodyReader {
    r: BufReader<TcpStream>,
    mode: Mode,
    done: bool,
}

impl BodyReader {
    /// The next piece of the body, or `None` at the end. A connection that breaks early is an error.
    pub async fn next(&mut self) -> Result<Option<Vec<u8>>, ClientError> {
        if self.done {
            return Ok(None);
        }
        match self.mode {
            Mode::Length(0) => {
                self.done = true;
                Ok(None)
            }
            Mode::Length(n) => {
                let piece = self.read_piece(n).await?;
                self.mode = Mode::Length(n - piece.len() as u64);
                Ok(Some(piece))
            }
            Mode::UntilClose => {
                let mut buf = vec![0u8; READ_STEP];
                let k = self.r.read(&mut buf).await?;
                if k == 0 {
                    self.done = true;
                    return Ok(None);
                }
                buf.truncate(k);
                Ok(Some(buf))
            }
            Mode::Chunked(mut left) => {
                if left == 0 {
                    let mut used = 0;
                    let line = read_line_limited(&mut self.r, &mut used).await?;
                    let size_text = line.trim().split(';').next().unwrap_or("");
                    let size = u64::from_str_radix(size_text, 16).map_err(|_| ClientError::Protocol("bad chunk size"))?;
                    if size == 0 {
                        // Trailer lines until the empty line.
                        loop {
                            let t = read_line_limited(&mut self.r, &mut used).await?;
                            if t.trim().is_empty() {
                                break;
                            }
                        }
                        self.done = true;
                        return Ok(None);
                    }
                    left = size;
                }
                let piece = self.read_piece(left).await?;
                left -= piece.len() as u64;
                if left == 0 {
                    let mut used = 0;
                    let crlf = read_line_limited(&mut self.r, &mut used).await?;
                    if !crlf.trim().is_empty() {
                        return Err(ClientError::Protocol("bad chunk end"));
                    }
                }
                self.mode = Mode::Chunked(left);
                Ok(Some(piece))
            }
        }
    }

    async fn read_piece(&mut self, want: u64) -> Result<Vec<u8>, ClientError> {
        let n = want.min(READ_STEP as u64) as usize;
        let mut buf = vec![0u8; n];
        let k = self.r.read(&mut buf).await?;
        if k == 0 {
            return Err(ClientError::Unreachable);
        }
        buf.truncate(k);
        Ok(buf)
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use crate::testutil::FakeServer;

    const TOKEN: &str = "test-token-test-token-test-token-test-0001";

    #[test]
    fn only_loopback_addresses_are_accepted() {
        assert!(parse_loopback_addr("127.0.0.1:54321").is_some());
        assert!(parse_loopback_addr(" [::1]:8080\n").is_some());
        assert!(parse_loopback_addr("127.5.5.5:80").is_some());
        for bad in ["0.0.0.0:80", "192.0.2.1:80", "localhost:80", "example.com:80", "127.0.0.1", "127.0.0.1:0", "[::]:80", ""] {
            assert!(parse_loopback_addr(bad).is_none(), "{bad}");
        }
        let remote: SocketAddr = "192.0.2.7:80".parse().unwrap();
        assert!(Client::new(remote, TOKEN).is_none());
    }

    #[test]
    fn debug_output_hides_the_token() {
        let c = Client::new("127.0.0.1:9".parse().unwrap(), TOKEN).unwrap();
        let text = format!("{c:?} {:?}", Secret::new(TOKEN));
        assert!(!text.contains(TOKEN));
        assert!(text.contains("redacted"));
    }

    #[test]
    fn rejects_paths_that_could_inject_headers() {
        let c = Client::new("127.0.0.1:9".parse().unwrap(), TOKEN).unwrap();
        for bad in ["/a b", "/a\r\nX: y", "noslash", "/ü"] {
            assert!(matches!(c.head("GET", bad, None, false), Err(ClientError::Protocol(_))), "{bad}");
        }
    }

    #[tokio::test]
    async fn sends_host_and_bearer_token_and_reads_json() {
        let srv = FakeServer::start(TOKEN).await;
        srv.reply("GET", "/api/v1/version", 200, r#"{"api":"v1"}"#);
        let c = Client::new(srv.addr, TOKEN).unwrap();
        let r = c.send("GET", "/api/v1/version", None).await.unwrap();
        assert_eq!(r.status, 200);
        assert_eq!(r.body, br#"{"api":"v1"}"#);
        let req = &srv.requests()[0];
        assert_eq!(req.header("authorization"), Some(format!("Bearer {TOKEN}").as_str()));
        assert_eq!(req.header("host"), Some(srv.addr.to_string().as_str()));
        assert_eq!(req.header("connection"), Some("close"));
    }

    #[tokio::test]
    async fn wrong_token_gets_401_with_empty_body() {
        let srv = FakeServer::start(TOKEN).await;
        srv.reply("GET", "/api/v1/state", 200, "{}");
        let c = Client::new(srv.addr, "another-token-another-token-another-1").unwrap();
        let r = c.send("GET", "/api/v1/state", None).await.unwrap();
        assert_eq!(r.status, 401);
        assert!(r.body.is_empty());
    }

    #[tokio::test]
    async fn post_with_body_sends_json_headers() {
        let srv = FakeServer::start(TOKEN).await;
        srv.reply("POST", "/api/v1/check-now", 202, r#"{"ok":true}"#);
        let c = Client::new(srv.addr, TOKEN).unwrap();
        c.send("POST", "/api/v1/check-now", Some(br#"{"domains":["a.example"]}"#)).await.unwrap();
        c.send("POST", "/api/v1/connect", None).await.unwrap();
        let reqs = srv.requests();
        assert_eq!(reqs[0].header("content-type"), Some("application/json"));
        assert_eq!(reqs[0].body, br#"{"domains":["a.example"]}"#);
        assert_eq!(reqs[1].header("content-length"), Some("0"));
    }

    #[tokio::test]
    async fn refused_connection_is_unreachable() {
        // Bind and drop to get a port nobody listens on.
        let l = tokio::net::TcpListener::bind("127.0.0.1:0").await.unwrap();
        let addr = l.local_addr().unwrap();
        drop(l);
        let c = Client::new(addr, TOKEN).unwrap();
        assert!(matches!(c.send("GET", "/api/v1/state", None).await, Err(ClientError::Unreachable)));
    }

    #[tokio::test]
    async fn silent_server_times_out() {
        let l = tokio::net::TcpListener::bind("127.0.0.1:0").await.unwrap();
        let addr = l.local_addr().unwrap();
        tokio::spawn(async move {
            let (_s, _) = l.accept().await.unwrap();
            tokio::time::sleep(Duration::from_secs(30)).await;
        });
        let c = Client::new(addr, TOKEN).unwrap().with_timeouts(Duration::from_millis(200), Duration::from_millis(300));
        assert!(matches!(c.send("GET", "/api/v1/state", None).await, Err(ClientError::Timeout)));
    }

    #[tokio::test]
    async fn decodes_chunked_event_stream_across_chunk_boundaries() {
        let srv = FakeServer::start(TOKEN).await;
        srv.script_events(vec!["retry: 2000\n\nid: 1\nev", "ent: state\ndata: {}\n\n"], crate::testutil::After::Close);
        let c = Client::new(srv.addr, TOKEN).unwrap();
        let mut body = c.open_stream("/api/v1/events").await.unwrap();
        let mut all = Vec::new();
        while let Some(p) = body.next().await.unwrap() {
            all.extend_from_slice(&p);
        }
        assert_eq!(String::from_utf8(all).unwrap(), "retry: 2000\n\nid: 1\nevent: state\ndata: {}\n\n");
    }

    #[tokio::test]
    async fn non_200_stream_open_returns_the_response() {
        let srv = FakeServer::start(TOKEN).await;
        srv.reply("GET", "/api/v1/events", 429, r#"{"error":"busy","code":"busy"}"#);
        let c = Client::new(srv.addr, TOKEN).unwrap();
        match c.open_stream("/api/v1/events").await {
            Err(ClientError::Status(r)) => assert_eq!(r.status, 429),
            other => panic!("unexpected {other:?}"),
        }
    }
}
