//! Owning the `splitter` child: token and address files, launch, readiness, event forwarding and
//! a clean stop.
//!
//! Start (`start_engine`): create a random token, write it to a user-only file, delete a stale
//! address file, launch `splitter run --ui --token-file ... --addr-file ... --parent-pid <ui pid>`
//! through a [`Launcher`], then wait for the ready line (`OBHOD-API v1 host:port`) or the address
//! file, and confirm with an authenticated `GET /version`. Only loopback addresses are accepted.
//! The token file is deleted once the engine has answered (the engine keeps the token in memory).

use std::net::SocketAddr;
use std::path::PathBuf;
use std::sync::Arc;
use std::time::{Duration, Instant};

use tokio::sync::{watch, Mutex};
use tokio::task::JoinHandle;
use tokio::time::sleep;

use crate::client::Api;
use crate::error::ApiError;
use crate::http::{parse_loopback_addr, Client};
use crate::launcher::{build_args, pkexec_cancelled, LaunchError, LaunchSpec, Launcher, RunningEngine};
use crate::sse::{run_event_loop, EventSink, LoopConfig, LoopEnd};
use crate::token::{generate_token, write_token_file};

const TOKEN_FILE: &str = "api.token";
const ADDR_FILE: &str = "api.addr";

/// Where and how to start the engine.
#[derive(Debug, Clone)]
pub struct EngineConfig {
    /// The `splitter` executable.
    pub program: PathBuf,
    /// Per-user directory for the token and address files.
    pub run_dir: PathBuf,
    /// The UI's process id, so the engine ends when the UI does.
    pub parent_pid: Option<u32>,
    /// How long to wait for the engine (a UAC prompt may sit there a while).
    pub ready_timeout: Duration,
    pub poll_interval: Duration,
}

impl EngineConfig {
    pub fn new(program: PathBuf, run_dir: PathBuf) -> Self {
        Self {
            program,
            run_dir,
            parent_pid: Some(std::process::id()),
            ready_timeout: Duration::from_secs(120),
            poll_interval: Duration::from_millis(100),
        }
    }

    pub fn token_path(&self) -> PathBuf {
        self.run_dir.join(TOKEN_FILE)
    }

    pub fn addr_path(&self) -> PathBuf {
        self.run_dir.join(ADDR_FILE)
    }
}

/// `OBHOD-API v1 host:port` (the engine's only stdout line in `--ui` mode).
pub fn parse_ready_line(line: &str) -> Option<SocketAddr> {
    let rest = line.trim_end().strip_prefix("OBHOD-API v1 ")?;
    if rest.contains(char::is_whitespace) {
        return None;
    }
    parse_loopback_addr(rest)
}

fn map_launch(e: LaunchError) -> ApiError {
    match e {
        LaunchError::Cancelled => ApiError::permission_denied(),
        LaunchError::NotFound => ApiError::engine_unreachable("The engine program was not found next to the app."),
        LaunchError::Failed => ApiError::engine_unreachable("The engine could not be started."),
    }
}

/// Exit before the engine was ready. Code 2 is "needs administrator rights" (api/desktop/API.md);
/// 126/127 are pkexec's "dismissed" and "not authorized".
fn map_early_exit(code: i32) -> ApiError {
    if code == 2 || pkexec_cancelled(code) {
        ApiError::permission_denied()
    } else {
        ApiError::engine_unreachable("The engine stopped before it was ready.")
    }
}

/// A running engine, as returned by [`start_engine`].
pub struct Started {
    pub client: Client,
    pub api: Api,
    pub process: Box<dyn RunningEngine>,
}

fn cleanup_files(cfg: &EngineConfig) {
    let _ = std::fs::remove_file(cfg.token_path());
}

/// Launches the engine and waits until it answers. See the module docs.
pub async fn start_engine(launcher: &dyn Launcher, cfg: &EngineConfig) -> Result<Started, ApiError> {
    std::fs::create_dir_all(&cfg.run_dir).map_err(|_| ApiError::engine_unreachable("The app's data folder is not writable."))?;
    let token = generate_token().map_err(|_| ApiError::internal())?;
    write_token_file(&cfg.token_path(), &token).map_err(|_| ApiError::engine_unreachable("The access file could not be written."))?;

    // A dead address from a crashed run must not be mistaken for the new one.
    match std::fs::remove_file(cfg.addr_path()) {
        Ok(()) => {}
        Err(e) if e.kind() == std::io::ErrorKind::NotFound => {}
        Err(_) => {
            cleanup_files(cfg);
            return Err(ApiError::engine_unreachable("The old address file could not be removed."));
        }
    }

    let spec = LaunchSpec { program: cfg.program.clone(), args: build_args(&cfg.token_path(), &cfg.addr_path(), cfg.parent_pid) };
    let mut process = launcher.launch(&spec).map_err(|e| {
        cleanup_files(cfg);
        map_launch(e)
    })?;

    let deadline = Instant::now() + cfg.ready_timeout;
    let addr = loop {
        if let Some(code) = process.poll_exit() {
            cleanup_files(cfg);
            return Err(map_early_exit(code));
        }
        let mut found = None;
        for line in process.take_stdout_lines() {
            if let Some(a) = parse_ready_line(&line) {
                found = Some(a);
            }
        }
        if found.is_none() {
            if let Ok(text) = std::fs::read_to_string(cfg.addr_path()) {
                if !text.trim().is_empty() {
                    match parse_loopback_addr(&text) {
                        Some(a) => found = Some(a),
                        None => {
                            // Not a loopback address: never send the token there.
                            process.kill();
                            cleanup_files(cfg);
                            return Err(ApiError::engine_unreachable("The engine reported an address the app does not accept."));
                        }
                    }
                }
            }
        }
        if let Some(a) = found {
            break a;
        }
        if Instant::now() >= deadline {
            process.kill();
            cleanup_files(cfg);
            return Err(ApiError::engine_unreachable("The engine did not start in time."));
        }
        sleep(cfg.poll_interval).await;
    };

    let client = Client::new(addr, token).ok_or_else(ApiError::internal)?;
    let api = Api::new(client.clone());
    // Confirm that this is our engine: it must accept the token.
    let confirm_deadline = Instant::now() + Duration::from_secs(5).min(cfg.ready_timeout);
    loop {
        match api.version().await {
            Ok(_) => break,
            Err(e) => {
                if let Some(code) = process.poll_exit() {
                    cleanup_files(cfg);
                    return Err(map_early_exit(code));
                }
                if Instant::now() >= confirm_deadline {
                    process.kill();
                    cleanup_files(cfg);
                    return Err(e);
                }
                sleep(cfg.poll_interval).await;
            }
        }
    }
    cleanup_files(cfg);
    Ok(Started { client, api, process })
}

struct Running {
    api: Api,
    process: Box<dyn RunningEngine>,
    stop: watch::Sender<bool>,
    events: JoinHandle<LoopEnd>,
}

impl Running {
    fn stop_events(&self) {
        let _ = self.stop.send(true);
    }
}

impl Drop for Running {
    fn drop(&mut self) {
        self.stop_events();
        self.events.abort();
    }
}

/// Holds the one running engine of this app session.
pub struct Manager {
    launcher: Arc<dyn Launcher>,
    cfg: EngineConfig,
    sink: Arc<dyn EventSink>,
    loop_cfg: LoopConfig,
    inner: Mutex<Option<Running>>,
}

impl Manager {
    pub fn new(launcher: Arc<dyn Launcher>, cfg: EngineConfig, sink: Arc<dyn EventSink>) -> Self {
        Self { launcher, cfg, sink, loop_cfg: LoopConfig::default(), inner: Mutex::new(None) }
    }

    #[cfg(test)]
    pub fn with_loop_config(mut self, c: LoopConfig) -> Self {
        self.loop_cfg = c;
        self
    }

    /// Starts the engine unless it already runs. Returns when it answers.
    pub async fn ensure_started(&self) -> Result<(), ApiError> {
        let mut g = self.inner.lock().await;
        if let Some(r) = g.as_mut() {
            if r.process.poll_exit().is_none() {
                return Ok(());
            }
            *g = None; // the old process is gone
        }
        let s = start_engine(self.launcher.as_ref(), &self.cfg).await?;
        let (stop, rx) = watch::channel(false);
        let events = tokio::spawn(run_event_loop(s.client.clone(), self.sink.clone(), rx, self.loop_cfg));
        *g = Some(Running { api: s.api, process: s.process, stop, events });
        Ok(())
    }

    /// Stops the engine (if any) and starts a new one with a new token.
    pub async fn restart(&self) -> Result<(), ApiError> {
        self.stop().await;
        self.ensure_started().await
    }

    /// The API handle, or `engine_unreachable` when there is no live engine.
    pub async fn api(&self) -> Result<Api, ApiError> {
        let mut g = self.inner.lock().await;
        match g.as_mut() {
            None => Err(ApiError::engine_unreachable("The engine is not running.")),
            Some(r) => {
                if r.process.poll_exit().is_some() {
                    *g = None;
                    return Err(ApiError::engine_unreachable("The engine is not running."));
                }
                Ok(r.api.clone())
            }
        }
    }

    /// Call after a failed engine call: forgets the engine when its process has ended.
    pub async fn observe(&self, err: &ApiError) {
        if !err.is_engine_lost() {
            return;
        }
        let mut g = self.inner.lock().await;
        if g.as_mut().is_some_and(|r| r.process.poll_exit().is_some()) {
            *g = None;
        }
    }

    pub async fn is_running(&self) -> bool {
        self.api().await.is_ok()
    }

    /// Asks the engine to shut down, waits briefly, then forgets it.
    pub async fn stop(&self) {
        let running = self.inner.lock().await.take();
        let Some(mut r) = running else { return };
        r.stop_events();
        let _ = tokio::time::timeout(Duration::from_secs(3), r.api.shutdown()).await;
        let deadline = Instant::now() + Duration::from_secs(5);
        while r.process.poll_exit().is_none() {
            if Instant::now() >= deadline {
                r.process.kill();
                break;
            }
            sleep(Duration::from_millis(50)).await;
        }
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use crate::sse::EngineEvent;
    use crate::testutil::{After, FakeServer};
    use std::sync::atomic::{AtomicBool, AtomicI32, Ordering};
    use std::sync::Mutex as StdMutex;

    /// Fake engine: on launch it reads the token file (like the real engine) and tells the fake
    /// HTTP server to accept it, then reports its address as the real engine would.
    struct FakeLauncher {
        server: Arc<FakeServer>,
        ready: Ready,
        launches: StdMutex<Vec<LaunchSpec>>,
        stale_addr_existed: AtomicBool,
        token_seen: StdMutex<Option<String>>,
        fail: StdMutex<Option<LaunchError>>,
        exit_code: Arc<AtomicI32>,
    }

    #[derive(Clone, Copy, PartialEq)]
    enum Ready {
        AddrFile,
        StdoutLine,
        BadAddrFile,
        Never,
        ExitsEarly(i32),
    }

    const NOT_EXITED: i32 = i32::MIN;

    struct FakeProc {
        lines: Vec<String>,
        exit_code: Arc<AtomicI32>,
        killed: Arc<AtomicBool>,
    }

    impl RunningEngine for FakeProc {
        fn poll_exit(&mut self) -> Option<i32> {
            let c = self.exit_code.load(Ordering::SeqCst);
            (c != NOT_EXITED).then_some(c)
        }
        fn take_stdout_lines(&mut self) -> Vec<String> {
            std::mem::take(&mut self.lines)
        }
        fn kill(&mut self) {
            self.killed.store(true, Ordering::SeqCst);
        }
    }

    impl FakeLauncher {
        fn new(server: Arc<FakeServer>, ready: Ready) -> Self {
            Self {
                server,
                ready,
                launches: StdMutex::new(Vec::new()),
                stale_addr_existed: AtomicBool::new(false),
                token_seen: StdMutex::new(None),
                fail: StdMutex::new(None),
                exit_code: Arc::new(AtomicI32::new(NOT_EXITED)),
            }
        }
    }

    fn arg_after<'a>(args: &'a [String], flag: &str) -> &'a str {
        let i = args.iter().position(|a| a == flag).unwrap();
        &args[i + 1]
    }

    impl Launcher for FakeLauncher {
        fn launch(&self, spec: &LaunchSpec) -> Result<Box<dyn RunningEngine>, LaunchError> {
            self.launches.lock().unwrap().push(spec.clone());
            if let Some(e) = self.fail.lock().unwrap().clone() {
                return Err(e);
            }
            let token_file = arg_after(&spec.args, "--token-file");
            let addr_file = arg_after(&spec.args, "--addr-file");
            self.stale_addr_existed.store(std::path::Path::new(addr_file).exists(), Ordering::SeqCst);
            let token = std::fs::read_to_string(token_file).unwrap();
            *self.token_seen.lock().unwrap() = Some(token.clone());
            self.server.set_token(&token);
            let mut lines = Vec::new();
            match self.ready {
                Ready::AddrFile => std::fs::write(addr_file, self.server.addr.to_string()).unwrap(),
                Ready::StdoutLine => lines.push(format!("OBHOD-API v1 {}", self.server.addr)),
                Ready::BadAddrFile => std::fs::write(addr_file, "192.0.2.9:4444").unwrap(),
                Ready::Never => {}
                Ready::ExitsEarly(c) => self.exit_code.store(c, Ordering::SeqCst),
            }
            Ok(Box::new(FakeProc { lines, exit_code: self.exit_code.clone(), killed: Arc::new(AtomicBool::new(false)) }))
        }
    }

    #[derive(Default)]
    struct Collect(StdMutex<Vec<EngineEvent>>);
    impl EventSink for Collect {
        fn emit(&self, e: EngineEvent) {
            self.0.lock().unwrap().push(e);
        }
    }

    fn temp_dir(tag: &str) -> PathBuf {
        let d = std::env::temp_dir().join(format!("obhod-engine-test-{}-{tag}", std::process::id()));
        let _ = std::fs::remove_dir_all(&d);
        d
    }

    fn config(tag: &str) -> EngineConfig {
        let mut c = EngineConfig::new(PathBuf::from("splitter-fake"), temp_dir(tag));
        c.ready_timeout = Duration::from_millis(600);
        c.poll_interval = Duration::from_millis(10);
        c.parent_pid = Some(4242);
        c
    }

    async fn server_with_version() -> Arc<FakeServer> {
        let s = Arc::new(FakeServer::start("not-the-token-yet-not-the-token-yet").await);
        s.reply("GET", "/api/v1/version", 200, &crate::testutil::testdata("version.golden.json"));
        s.reply("POST", "/api/v1/shutdown", 200, r#"{"ok":true}"#);
        s
    }

    #[test]
    fn ready_line_must_match_the_contract_and_be_loopback() {
        assert_eq!(parse_ready_line("OBHOD-API v1 127.0.0.1:54321\n").unwrap().port(), 54321);
        assert!(parse_ready_line("OBHOD-API v1 [::1]:54321").is_some());
        for bad in [
            "OBHOD-API v2 127.0.0.1:1",
            "OBHOD-API v1  127.0.0.1:1",
            "OBHOD-API v1 10.0.0.1:1",
            "OBHOD-API v1 localhost:1",
            "hello",
            "OBHOD-API v1 127.0.0.1:1 extra",
            "",
        ] {
            assert!(parse_ready_line(bad).is_none(), "{bad}");
        }
    }

    #[tokio::test]
    async fn starts_via_addr_file_with_the_documented_command_line() {
        let srv = server_with_version().await;
        let l = FakeLauncher::new(srv.clone(), Ready::AddrFile);
        let cfg = config("addr");
        // A stale address from a crashed run must be deleted before the launch.
        std::fs::create_dir_all(&cfg.run_dir).unwrap();
        std::fs::write(cfg.addr_path(), "127.0.0.1:1").unwrap();

        let started = start_engine(&l, &cfg).await.unwrap();
        assert_eq!(started.client.addr(), srv.addr);
        assert!(!l.stale_addr_existed.load(Ordering::SeqCst), "stale addr-file was still there at launch");

        let spec = l.launches.lock().unwrap()[0].clone();
        assert_eq!(spec.program, PathBuf::from("splitter-fake"));
        assert_eq!(&spec.args[..2], ["run", "--ui"]);
        assert_eq!(arg_after(&spec.args, "--token-file"), cfg.token_path().to_string_lossy());
        assert_eq!(arg_after(&spec.args, "--addr-file"), cfg.addr_path().to_string_lossy());
        assert_eq!(arg_after(&spec.args, "--parent-pid"), "4242");

        // The token is passed by file only, never on the command line, and the file is gone once the engine answered.
        let token = l.token_seen.lock().unwrap().clone().unwrap();
        assert!(crate::token::is_valid_token(&token));
        assert!(!spec.args.iter().any(|a| a.contains(&token)));
        assert!(!cfg.token_path().exists());
        let auth = srv.requests().iter().find_map(|r| r.header("authorization").map(String::from)).unwrap();
        assert_eq!(auth, format!("Bearer {token}"));
        let _ = std::fs::remove_dir_all(&cfg.run_dir);
    }

    #[tokio::test]
    async fn starts_via_ready_line_on_stdout() {
        let srv = server_with_version().await;
        let l = FakeLauncher::new(srv.clone(), Ready::StdoutLine);
        let cfg = config("line");
        let started = start_engine(&l, &cfg).await.unwrap();
        assert_eq!(started.client.addr(), srv.addr);
        let _ = std::fs::remove_dir_all(&cfg.run_dir);
    }

    #[tokio::test]
    async fn refuses_a_non_loopback_address_and_never_contacts_it() {
        let srv = server_with_version().await;
        let l = FakeLauncher::new(srv.clone(), Ready::BadAddrFile);
        let cfg = config("bad");
        let e = start_engine(&l, &cfg).await.err().unwrap();
        assert_eq!(e.code, "engine_unreachable");
        assert!(srv.requests().is_empty());
        assert!(!cfg.token_path().exists(), "token file must not be left behind");
        let _ = std::fs::remove_dir_all(&cfg.run_dir);
    }

    #[tokio::test]
    async fn launch_errors_and_early_exits_map_to_user_codes() {
        let srv = server_with_version().await;
        for (err, code) in [
            (LaunchError::Cancelled, "permission_denied"),
            (LaunchError::NotFound, "engine_unreachable"),
            (LaunchError::Failed, "engine_unreachable"),
        ] {
            let l = FakeLauncher::new(srv.clone(), Ready::Never);
            *l.fail.lock().unwrap() = Some(err);
            let cfg = config("launch-err");
            let e = start_engine(&l, &cfg).await.err().unwrap();
            assert_eq!(e.code, code);
            assert!(!cfg.token_path().exists());
            let _ = std::fs::remove_dir_all(&cfg.run_dir);
        }
        for (exit, code) in [
            (2, "permission_denied"),
            (126, "permission_denied"),
            (127, "permission_denied"),
            (1, "engine_unreachable"),
            (3, "engine_unreachable"),
        ] {
            let l = FakeLauncher::new(srv.clone(), Ready::ExitsEarly(exit));
            let cfg = config("exit");
            let e = start_engine(&l, &cfg).await.err().unwrap();
            assert_eq!(e.code, code, "exit {exit}");
            let _ = std::fs::remove_dir_all(&cfg.run_dir);
        }
    }

    #[tokio::test]
    async fn times_out_when_the_engine_never_reports_an_address() {
        let srv = server_with_version().await;
        let l = FakeLauncher::new(srv, Ready::Never);
        let mut cfg = config("timeout");
        cfg.ready_timeout = Duration::from_millis(100);
        let e = start_engine(&l, &cfg).await.err().unwrap();
        assert_eq!(e.code, "engine_unreachable");
        let _ = std::fs::remove_dir_all(&cfg.run_dir);
    }

    #[tokio::test]
    async fn errors_never_contain_the_token_or_paths() {
        let srv = server_with_version().await;
        let mut seen = Vec::new();
        for ready in [Ready::Never, Ready::BadAddrFile, Ready::ExitsEarly(1)] {
            let l = FakeLauncher::new(srv.clone(), ready);
            let mut cfg = config("secret");
            cfg.ready_timeout = Duration::from_millis(100);
            let e = start_engine(&l, &cfg).await.err().unwrap();
            let token = l.token_seen.lock().unwrap().clone().unwrap();
            let shown = format!("{e} {e:?} {}", serde_json::to_string(&e).unwrap());
            assert!(!shown.contains(&token));
            assert!(!shown.contains(&cfg.run_dir.to_string_lossy().to_string()));
            seen.push(shown);
            let _ = std::fs::remove_dir_all(&cfg.run_dir);
        }
        assert_eq!(seen.len(), 3);
    }

    #[tokio::test]
    async fn manager_starts_once_forwards_events_and_stops_with_shutdown() {
        let srv = server_with_version().await;
        srv.script_events(vec!["event: state\ndata: {\"state\":\"disconnected\"}\n\n"], After::Hold);
        let l = Arc::new(FakeLauncher::new(srv.clone(), Ready::AddrFile));
        let sink = Arc::new(Collect::default());
        let cfg = config("manager");
        let m = Manager::new(l.clone(), cfg.clone(), sink.clone());

        assert_eq!(m.api().await.unwrap_err().code, "engine_unreachable");
        m.ensure_started().await.unwrap();
        m.ensure_started().await.unwrap(); // idempotent
        assert_eq!(l.launches.lock().unwrap().len(), 1);
        assert!(m.is_running().await);

        for _ in 0..200 {
            if sink.0.lock().unwrap().iter().any(|e| e.kind == "state") {
                break;
            }
            sleep(Duration::from_millis(10)).await;
        }
        let kinds: Vec<String> = sink.0.lock().unwrap().iter().map(|e| e.kind.clone()).collect();
        assert_eq!(kinds, ["stream-open", "state"]);

        // stop(): POST /shutdown, then the (fake) process exits.
        let exit = l.exit_code.clone();
        let stopper = tokio::spawn(async move {
            sleep(Duration::from_millis(150)).await;
            exit.store(0, Ordering::SeqCst);
        });
        m.stop().await;
        stopper.await.unwrap();
        assert_eq!(srv.count("POST", "/api/v1/shutdown"), 1);
        assert!(!m.is_running().await);
        let _ = std::fs::remove_dir_all(&cfg.run_dir);
    }

    #[tokio::test]
    async fn manager_notices_a_dead_process_and_can_restart_with_a_new_token() {
        let srv = server_with_version().await;
        let l = Arc::new(FakeLauncher::new(srv.clone(), Ready::AddrFile));
        let cfg = config("restart");
        let m = Manager::new(l.clone(), cfg.clone(), Arc::new(Collect::default())).with_loop_config(LoopConfig {
            idle_timeout: Duration::from_millis(200),
            default_retry: Duration::from_millis(20),
            max_backoff: Duration::from_millis(40),
        });
        m.ensure_started().await.unwrap();
        let first = l.token_seen.lock().unwrap().clone().unwrap();

        l.exit_code.store(0, Ordering::SeqCst); // the process ends behind our back
        let e = m.api().await.unwrap_err();
        assert_eq!(e.code, "engine_unreachable");

        l.exit_code.store(NOT_EXITED, Ordering::SeqCst);
        m.ensure_started().await.unwrap();
        let second = l.token_seen.lock().unwrap().clone().unwrap();
        assert_ne!(first, second, "every start uses a fresh token");
        assert_eq!(l.launches.lock().unwrap().len(), 2);
        m.stop().await;
        let _ = std::fs::remove_dir_all(&cfg.run_dir);
    }

    #[tokio::test]
    async fn observe_keeps_an_engine_that_is_alive_but_slow() {
        let srv = server_with_version().await;
        let l = Arc::new(FakeLauncher::new(srv, Ready::AddrFile));
        let cfg = config("observe");
        let m = Manager::new(l.clone(), cfg.clone(), Arc::new(Collect::default()));
        m.ensure_started().await.unwrap();
        m.observe(&ApiError::engine_unreachable("slow")).await;
        assert!(m.is_running().await);
        l.exit_code.store(1, Ordering::SeqCst);
        m.observe(&ApiError::engine_unreachable("gone")).await;
        assert!(!m.is_running().await);
        let _ = std::fs::remove_dir_all(&cfg.run_dir);
    }
}
