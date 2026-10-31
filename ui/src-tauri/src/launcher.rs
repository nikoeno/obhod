//! Starting the `splitter` engine elevated (ADR 0003, decision 3), behind a trait so tests never
//! start a process.
//!
//! * Windows: `ShellExecuteExW` with the `runas` verb (UAC prompt), hidden window. The launch
//!   cannot capture stdout, so the address comes from `--addr-file`.
//! * Linux: `pkexec splitter ...`, stdout piped so the ready line can be read too.
//!
//! The real launchers are compiled everywhere they can be but never run by tests or by agents.
//! The UI itself never runs elevated; only the engine does.

use std::ffi::OsString;
use std::path::{Path, PathBuf};

/// Everything needed to start the engine.
#[derive(Debug, Clone, PartialEq, Eq)]
pub struct LaunchSpec {
    pub program: PathBuf,
    pub args: Vec<String>,
}

/// Why a launch failed before the engine could run.
#[derive(Debug, Clone, PartialEq, Eq)]
pub enum LaunchError {
    /// The user cancelled the UAC / pkexec prompt, or the authorization was refused.
    Cancelled,
    /// The engine program does not exist at the expected place.
    NotFound,
    /// Anything else (no such verb, no polkit agent, ...). No details: they may contain paths.
    Failed,
}

/// A started engine process.
pub trait RunningEngine: Send {
    /// `Some(exit code)` once the process has ended, `None` while it runs.
    fn poll_exit(&mut self) -> Option<i32>;
    /// Complete stdout lines read since the last call (empty when stdout is not captured).
    fn take_stdout_lines(&mut self) -> Vec<String>;
    /// Best effort. An elevated process may refuse this to a normal user; `/shutdown` and
    /// `--parent-pid` are the real ways to stop it.
    fn kill(&mut self);
}

/// Starts the engine. The real implementations elevate.
pub trait Launcher: Send + Sync {
    fn launch(&self, spec: &LaunchSpec) -> Result<Box<dyn RunningEngine>, LaunchError>;
}

/// `splitter run --ui --token-file P --addr-file P2 [--parent-pid PID]` (api/desktop/API.md).
pub fn build_args(token_file: &Path, addr_file: &Path, parent_pid: Option<u32>) -> Vec<String> {
    let mut a = vec![
        "run".to_string(),
        "--ui".to_string(),
        "--token-file".to_string(),
        token_file.to_string_lossy().into_owned(),
        "--addr-file".to_string(),
        addr_file.to_string_lossy().into_owned(),
    ];
    if let Some(pid) = parent_pid {
        a.push("--parent-pid".to_string());
        a.push(pid.to_string());
    }
    a
}

/// Quotes one argument for a Windows command line (the `CommandLineToArgvW` rules).
pub fn quote_windows_arg(arg: &str) -> String {
    if !arg.is_empty() && !arg.contains([' ', '\t', '\n', '\u{b}', '"']) {
        return arg.to_string();
    }
    let mut out = String::from("\"");
    let mut backslashes = 0usize;
    for c in arg.chars() {
        match c {
            '\\' => backslashes += 1,
            '"' => {
                out.push_str(&"\\".repeat(backslashes * 2 + 1));
                out.push('"');
                backslashes = 0;
            }
            _ => {
                out.push_str(&"\\".repeat(backslashes));
                backslashes = 0;
                out.push(c);
            }
        }
    }
    out.push_str(&"\\".repeat(backslashes * 2));
    out.push('"');
    out
}

/// The command line `ShellExecuteExW` receives as `lpParameters`.
pub fn windows_parameters(args: &[String]) -> String {
    args.iter().map(|a| quote_windows_arg(a)).collect::<Vec<_>>().join(" ")
}

/// pkexec exit codes: 126 = the user dismissed the dialog, 127 = authorization failed.
pub fn pkexec_cancelled(code: i32) -> bool {
    code == 126 || code == 127
}

/// The launcher for this platform.
pub fn default_launcher() -> Box<dyn Launcher> {
    #[cfg(windows)]
    {
        Box::new(windows::ShellExecuteLauncher)
    }
    #[cfg(not(windows))]
    {
        Box::new(PkexecLauncher)
    }
}

/// Where the engine binary is expected: next to the UI executable (the Tauri install folder).
/// Debug builds also honour `OBHOD_SPLITTER` so a developer can point at `bin/splitter`; release
/// builds never read the environment, so nothing can redirect what gets elevated.
pub fn engine_program() -> Option<PathBuf> {
    #[cfg(debug_assertions)]
    if let Some(p) = std::env::var_os("OBHOD_SPLITTER") {
        return Some(PathBuf::from(p));
    }
    let exe = std::env::current_exe().ok()?;
    let name: OsString = if cfg!(windows) { "splitter.exe".into() } else { "splitter".into() };
    Some(exe.parent()?.join(name))
}

/// Linux (and other Unix): `pkexec <program> <args>` with stdout piped.
#[allow(dead_code)] // only selected on non-Windows targets
pub struct PkexecLauncher;

impl Launcher for PkexecLauncher {
    fn launch(&self, spec: &LaunchSpec) -> Result<Box<dyn RunningEngine>, LaunchError> {
        use std::io::{BufRead, BufReader};
        use std::process::{Command, Stdio};
        use std::sync::mpsc;

        if !spec.program.exists() {
            return Err(LaunchError::NotFound);
        }
        let mut child = Command::new("pkexec")
            .arg(&spec.program)
            .args(&spec.args)
            .stdin(Stdio::null())
            .stdout(Stdio::piped())
            .stderr(Stdio::null())
            .spawn()
            .map_err(|_| LaunchError::Failed)?;
        let (tx, rx) = mpsc::channel();
        if let Some(out) = child.stdout.take() {
            std::thread::spawn(move || {
                for line in BufReader::new(out).lines().map_while(Result::ok) {
                    if tx.send(line).is_err() {
                        break;
                    }
                }
            });
        }
        Ok(Box::new(PkexecProcess { child, lines: rx }))
    }
}

struct PkexecProcess {
    child: std::process::Child,
    lines: std::sync::mpsc::Receiver<String>,
}

impl RunningEngine for PkexecProcess {
    fn poll_exit(&mut self) -> Option<i32> {
        match self.child.try_wait() {
            Ok(Some(status)) => Some(status.code().unwrap_or(-1)),
            Ok(None) => None,
            Err(_) => Some(-1),
        }
    }

    fn take_stdout_lines(&mut self) -> Vec<String> {
        self.lines.try_iter().collect()
    }

    fn kill(&mut self) {
        let _ = self.child.kill();
    }
}

#[cfg(windows)]
mod windows {
    //! `ShellExecuteExW` with `runas`. All `unsafe` of the crate is in this module.

    use super::{windows_parameters, LaunchError, LaunchSpec, Launcher, RunningEngine};
    use std::os::windows::ffi::OsStrExt;
    use windows_sys::Win32::Foundation::{
        CloseHandle, GetLastError, ERROR_CANCELLED, ERROR_FILE_NOT_FOUND, ERROR_PATH_NOT_FOUND, WAIT_OBJECT_0,
    };
    use windows_sys::Win32::System::Threading::{GetExitCodeProcess, TerminateProcess, WaitForSingleObject};
    use windows_sys::Win32::UI::Shell::{ShellExecuteExW, SEE_MASK_NOCLOSEPROCESS, SHELLEXECUTEINFOW};
    use windows_sys::Win32::UI::WindowsAndMessaging::SW_HIDE;

    fn wide(s: &std::ffi::OsStr) -> Vec<u16> {
        s.encode_wide().chain(std::iter::once(0)).collect()
    }

    pub struct ShellExecuteLauncher;

    impl Launcher for ShellExecuteLauncher {
        fn launch(&self, spec: &LaunchSpec) -> Result<Box<dyn RunningEngine>, LaunchError> {
            if !spec.program.exists() {
                return Err(LaunchError::NotFound);
            }
            let verb = wide("runas".as_ref());
            let file = wide(spec.program.as_os_str());
            let params = wide(windows_parameters(&spec.args).as_ref());
            // SAFETY: SHELLEXECUTEINFOW is plain data; every pointer field points at a
            // NUL-terminated UTF-16 buffer that outlives the call.
            let handle = unsafe {
                let mut info: SHELLEXECUTEINFOW = std::mem::zeroed();
                info.cbSize = std::mem::size_of::<SHELLEXECUTEINFOW>() as u32;
                info.fMask = SEE_MASK_NOCLOSEPROCESS;
                info.lpVerb = verb.as_ptr();
                info.lpFile = file.as_ptr();
                info.lpParameters = params.as_ptr();
                info.nShow = SW_HIDE;
                if ShellExecuteExW(&mut info) == 0 {
                    return Err(match GetLastError() {
                        ERROR_CANCELLED => LaunchError::Cancelled,
                        ERROR_FILE_NOT_FOUND | ERROR_PATH_NOT_FOUND => LaunchError::NotFound,
                        _ => LaunchError::Failed,
                    });
                }
                info.hProcess
            };
            if handle.is_null() {
                return Err(LaunchError::Failed);
            }
            Ok(Box::new(WinProcess { handle: handle as isize }))
        }
    }

    /// The process handle is kept as an integer so the type is `Send`.
    struct WinProcess {
        handle: isize,
    }

    impl RunningEngine for WinProcess {
        fn poll_exit(&mut self) -> Option<i32> {
            // SAFETY: the handle is valid until Drop closes it.
            unsafe {
                if WaitForSingleObject(self.handle as _, 0) != WAIT_OBJECT_0 {
                    return None;
                }
                let mut code = 0u32;
                if GetExitCodeProcess(self.handle as _, &mut code) == 0 {
                    return Some(-1);
                }
                Some(code as i32)
            }
        }

        fn take_stdout_lines(&mut self) -> Vec<String> {
            Vec::new() // ShellExecute cannot redirect stdout; the address file is used.
        }

        fn kill(&mut self) {
            // SAFETY: valid handle. A normal-integrity process cannot terminate an elevated one;
            // the call then fails and is ignored (the engine stops through /shutdown or
            // --parent-pid).
            unsafe {
                TerminateProcess(self.handle as _, 1);
            }
        }
    }

    impl Drop for WinProcess {
        fn drop(&mut self) {
            // SAFETY: closing the handle we own, once.
            unsafe {
                CloseHandle(self.handle as _);
            }
        }
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn args_follow_the_cli_contract() {
        let a = build_args(Path::new("/run/api.token"), Path::new("/run/api.addr"), Some(4242));
        assert_eq!(a, ["run", "--ui", "--token-file", "/run/api.token", "--addr-file", "/run/api.addr", "--parent-pid", "4242"]);
        let a = build_args(Path::new("t"), Path::new("a"), None);
        assert!(!a.contains(&"--parent-pid".to_string()));
        // The token itself is never an argument.
        assert!(!a.iter().any(|x| x == "--token"));
    }

    #[test]
    fn windows_quoting_follows_commandlinetoargv_rules() {
        assert_eq!(quote_windows_arg("run"), "run");
        assert_eq!(quote_windows_arg(""), "\"\"");
        assert_eq!(quote_windows_arg(r"C:\Users\Jo Doe\api.token"), r#""C:\Users\Jo Doe\api.token""#);
        assert_eq!(quote_windows_arg(r"a b\"), r#""a b\\""#);
        assert_eq!(quote_windows_arg(r#"say "hi""#), r#""say \"hi\"""#);
        let p = windows_parameters(&["run".into(), r"C:\a b\c".into()]);
        assert_eq!(p, r#"run "C:\a b\c""#);
    }

    #[test]
    fn pkexec_dismissal_codes() {
        assert!(pkexec_cancelled(126) && pkexec_cancelled(127));
        assert!(!pkexec_cancelled(0) && !pkexec_cancelled(2));
    }
}
