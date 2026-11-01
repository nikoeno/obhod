//! Start-failure details for the UI's "Details" box.
//!
//! A Windows `runas` launch cannot capture the engine's stderr, so `splitter run --ui` writes its
//! start error to `--error-file` (`api/desktop/API.md`). This module reads that file (bounded),
//! removes everything that could be private (the token, IP addresses, `host:port`, host names,
//! directories of paths, long token-like strings) and renders the text shown to the user.
//! Nothing here is sent anywhere: the text goes to the webview and the file stays in the app's
//! per-user data folder.

use std::io::Read;
use std::net::{Ipv4Addr, Ipv6Addr};
use std::path::Path;

/// The most bytes read from the engine's error file.
pub const MAX_FILE_BYTES: usize = 4096;
/// The most characters in the details string handed to the webview.
pub const MAX_DETAILS_CHARS: usize = 4096;

/// Reads at most [`MAX_FILE_BYTES`] bytes of `path` (lossy UTF-8). `None` when the file is missing,
/// unreadable or blank. The bool says the file was longer than the limit.
pub fn read_bounded(path: &Path) -> Option<(String, bool)> {
    let f = std::fs::File::open(path).ok()?;
    let mut buf = Vec::new();
    f.take(MAX_FILE_BYTES as u64 + 1).read_to_end(&mut buf).ok()?;
    let cut = buf.len() > MAX_FILE_BYTES;
    buf.truncate(MAX_FILE_BYTES);
    let text = String::from_utf8_lossy(&buf).into_owned();
    if text.trim().is_empty() {
        return None;
    }
    Some((text, cut))
}

/// What is known about a failed start.
pub struct Facts<'a> {
    /// A short phrase: where the start failed.
    pub stage: &'a str,
    /// The engine's exit code, when it had exited.
    pub exit_code: Option<i32>,
    /// The engine program (only its file name is shown).
    pub program: &'a Path,
    /// The error file's text and whether it was cut at the size limit; `None` when there was none.
    pub engine_text: Option<(String, bool)>,
}

/// The details text. `token` is the access token of this start (masked wherever it appears).
pub fn render(f: &Facts<'_>, token: &str) -> String {
    let mut out = String::new();
    out.push_str(&format!("stage: {}\n", f.stage));
    if let Some(c) = f.exit_code {
        out.push_str(&format!("exit code: {c}\n"));
    }
    let name = f.program.file_name().map(|n| n.to_string_lossy().into_owned()).unwrap_or_default();
    let found = if f.program.exists() { "found" } else { "missing" };
    out.push_str(&format!("engine: {} ({found})\n", sanitize(&name, token)));
    match &f.engine_text {
        Some((text, cut)) => {
            out.push_str("engine output:\n");
            out.push_str(sanitize(text.trim_end(), token).as_str());
            out.push('\n');
            if *cut {
                out.push_str("(shortened)\n");
            }
        }
        None => out.push_str("engine output: none (the engine wrote no error file)\n"),
    }
    truncate_chars(out, MAX_DETAILS_CHARS)
}

fn truncate_chars(mut s: String, max: usize) -> String {
    if s.chars().count() > max {
        let end = s.char_indices().nth(max).map_or(s.len(), |(i, _)| i);
        s.truncate(end);
        s.push_str("\n(shortened)");
    }
    s
}

/// Removes the token, directories, addresses and token-like strings from `text`; control
/// characters other than newline and tab are dropped.
pub fn sanitize(text: &str, token: &str) -> String {
    let text = if token.is_empty() { text.to_string() } else { text.replace(token, "[hidden]") };
    let text: String = text.chars().filter(|c| *c == '\n' || *c == '\t' || !c.is_control()).collect();
    text.lines().map(|l| redact_long_tokens(&redact_words(&redact_paths(l)))).collect::<Vec<_>>().join("\n")
}

/// Replaces each absolute path by `…/<last component>`.
fn redact_paths(line: &str) -> String {
    let c: Vec<char> = line.chars().collect();
    let mut out = String::new();
    let mut i = 0;
    while i < c.len() {
        let boundary = i == 0 || c[i - 1].is_whitespace() || "\"'(=[<".contains(c[i - 1]);
        if boundary && starts_path(&c[i..]) {
            let mut end = i;
            while end < c.len() && !path_ends(&c, end) {
                end += 1;
            }
            let cand = &c[i..end];
            if is_path(cand) {
                let from = cand.iter().rposition(|x| *x == '/' || *x == '\\').map_or(0, |p| p + 1);
                out.push('…');
                out.push('/');
                out.extend(&cand[from..]);
                i = end;
                continue;
            }
        }
        out.push(c[i]);
        i += 1;
    }
    out
}

fn starts_path(c: &[char]) -> bool {
    match c {
        [d, ':', s, ..] if d.is_ascii_alphabetic() && (*s == '\\' || *s == '/') => true,
        ['\\', '\\', ..] => true,
        ['/', n, ..] => n.is_alphanumeric() || *n == '.' || *n == '_' || *n == '~',
        _ => false,
    }
}

/// A path ends at a quote, a character Windows forbids in names, or a colon / comma followed by a space.
fn path_ends(c: &[char], i: usize) -> bool {
    if "\"'<>|*?".contains(c[i]) {
        return true;
    }
    (c[i] == ':' || c[i] == ',') && (i + 1 == c.len() || c[i + 1] == ' ')
}

fn is_path(cand: &[char]) -> bool {
    match cand {
        ['/', ..] => cand.iter().filter(|x| **x == '/').count() >= 2,
        _ => true,
    }
}

const FILE_EXTS: &[&str] = &[
    "yaml", "yml", "json", "txt", "log", "go", "rs", "exe", "dll", "sh", "md", "conf", "cfg", "toml", "lock", "token", "addr", "tmp",
    "bak", "old", "manifest", "pid", "sock", "so",
];

fn is_word_char(c: char) -> bool {
    c.is_ascii_alphanumeric() || matches!(c, '.' | ':' | '-' | '_' | '[' | ']' | '%')
}

/// Replaces IP addresses, `host:port` and dotted host names.
fn redact_words(line: &str) -> String {
    let mut out = String::new();
    let mut word = String::new();
    let flush = |word: &mut String, out: &mut String| {
        if !word.is_empty() {
            out.push_str(&redact_word(word));
            word.clear();
        }
    };
    for ch in line.chars() {
        if is_word_char(ch) {
            word.push(ch);
        } else {
            flush(&mut word, &mut out);
            out.push(ch);
        }
    }
    flush(&mut word, &mut out);
    out
}

fn redact_word(w: &str) -> String {
    let core_end = w.trim_end_matches(['.', ':']).len();
    let (core, tail) = w.split_at(core_end);
    if core.is_empty() || !is_address_like(core) {
        return w.to_string();
    }
    format!("[address]{tail}")
}

fn is_address_like(core: &str) -> bool {
    let bare = core.trim_start_matches('[');
    // [v6]:port, [v6]
    if core.starts_with('[') {
        let inner = bare.split(']').next().unwrap_or("");
        return inner.parse::<Ipv6Addr>().is_ok();
    }
    if core.parse::<Ipv4Addr>().is_ok() || core.parse::<Ipv6Addr>().is_ok() {
        return true;
    }
    let (host, port) = match core.rsplit_once(':') {
        Some((h, p)) => (h, Some(p)),
        None => (core, None),
    };
    if let Some(p) = port {
        let digits = !p.is_empty() && p.len() <= 5 && p.bytes().all(|b| b.is_ascii_digit());
        if !digits || host.len() < 2 {
            return false;
        }
        if host.parse::<Ipv4Addr>().is_ok() {
            return true;
        }
        return host_like(host);
    }
    // A bare name with at least two dots and a non-file ending looks like a server name.
    host.matches('.').count() >= 2 && host_like(host)
}

/// Letters somewhere, labels of host characters, and not a file name.
fn host_like(h: &str) -> bool {
    if h.contains(':') || !h.bytes().any(|b| b.is_ascii_alphabetic()) {
        return false;
    }
    match h.rsplit_once('.') {
        Some((_, ext)) => !FILE_EXTS.contains(&ext.to_ascii_lowercase().as_str()) && ext.bytes().all(|b| b.is_ascii_alphabetic()),
        None => true, // localhost:8080, myserver:443
    }
}

/// Runs of 32 or more base64url characters look like a token.
fn redact_long_tokens(line: &str) -> String {
    let mut out = String::new();
    let mut run = String::new();
    let flush = |run: &mut String, out: &mut String| {
        if run.len() >= 32 {
            out.push_str("[hidden]");
        } else {
            out.push_str(run);
        }
        run.clear();
    };
    for ch in line.chars() {
        if ch.is_ascii_alphanumeric() || ch == '-' || ch == '_' {
            run.push(ch);
        } else {
            flush(&mut run, &mut out);
            out.push(ch);
        }
    }
    flush(&mut run, &mut out);
    out
}

#[cfg(test)]
mod tests {
    use super::*;
    use std::path::PathBuf;

    const TOKEN: &str = "k3Jd9sLq2VxT7mPz0AaBbCcDdEeFfGgHhIiJjKkLlMm";

    #[test]
    fn paths_keep_only_the_file_name() {
        assert_eq!(
            sanitize(r"splitter: config: open C:\ProgramData\splitter\app.yaml: Access is denied.", ""),
            "splitter: config: open …/app.yaml: Access is denied."
        );
        assert_eq!(sanitize(r#"read "C:\Users\Jo Doe\AppData\Local\run\api.token" failed"#, ""), r#"read "…/api.token" failed"#);
        assert_eq!(sanitize("open /home/jo/.config/splitter/app.yaml: no such file", ""), "open …/app.yaml: no such file");
        assert_eq!(sanitize(r"\\server\share\x\f.txt: bad", ""), "…/f.txt: bad");
        // A lone slash word or a relative path is not a path.
        assert_eq!(sanitize("tcp/udp and a/b", ""), "tcp/udp and a/b");
        assert_eq!(sanitize("directories: C:\\Users\\jo\\data\\", ""), "directories: …/");
    }

    #[test]
    fn addresses_and_hosts_are_removed_but_file_positions_stay() {
        assert_eq!(sanitize("cannot listen on 127.0.0.1:54321: busy", ""), "cannot listen on [address]: busy");
        assert_eq!(sanitize("dial 203.0.113.7 failed", ""), "dial [address] failed");
        assert_eq!(sanitize("dial [2001:db8::1]:443 failed", ""), "dial [address] failed");
        assert_eq!(sanitize("dial 2001:db8::1 failed", ""), "dial [address] failed");
        assert_eq!(sanitize("lookup vpn.example.com: no such host", ""), "lookup [address]: no such host");
        assert_eq!(sanitize("connect vpn.example.com:8443", ""), "connect [address]");
        assert_eq!(sanitize("localhost:8080 refused", ""), "[address] refused");
        assert_eq!(sanitize("app.yaml:12:3: bad key", ""), "app.yaml:12:3: bad key");
        assert_eq!(sanitize("at 12:30:45 it broke", ""), "at 12:30:45 it broke");
        assert_eq!(sanitize("version 1.2.3 and sing-box.exe", ""), "version 1.2.3 and sing-box.exe");
    }

    #[test]
    fn the_token_and_token_like_strings_are_hidden() {
        let s = sanitize(&format!("bad token {TOKEN} in file"), TOKEN);
        assert!(!s.contains(TOKEN) && s.contains("[hidden]"), "{s}");
        // Even without being told the token, a long base64url run goes.
        let s = sanitize(&format!("key={TOKEN}"), "");
        assert_eq!(s, "key=[hidden]");
        // Part of the token inside a longer word.
        assert!(!sanitize(&format!("x{TOKEN}y"), TOKEN).contains(&TOKEN[..20]));
        assert_eq!(sanitize("short abc123 word", ""), "short abc123 word");
    }

    #[test]
    fn control_characters_are_dropped() {
        assert_eq!(sanitize("a\u{1b}[31mb\u{7}\r\nc\td", ""), "a[31mb\nc\td");
    }

    #[test]
    fn reading_is_bounded_lossy_and_forgiving() {
        let dir = std::env::temp_dir().join(format!("obhod-diag-{}", std::process::id()));
        let _ = std::fs::remove_dir_all(&dir);
        std::fs::create_dir_all(&dir).unwrap();
        assert!(read_bounded(&dir.join("absent.txt")).is_none());
        let blank = dir.join("blank.txt");
        std::fs::write(&blank, " \r\n").unwrap();
        assert!(read_bounded(&blank).is_none());
        let bad = dir.join("bad.txt");
        std::fs::write(&bad, b"splitter: \xff\xfe broken\nexit code: 1\n").unwrap();
        let (t, cut) = read_bounded(&bad).unwrap();
        assert!(t.contains('\u{fffd}') && t.contains("exit code: 1") && !cut);
        let big = dir.join("big.txt");
        std::fs::write(&big, "x".repeat(MAX_FILE_BYTES * 3)).unwrap();
        let (t, cut) = read_bounded(&big).unwrap();
        assert_eq!(t.len(), MAX_FILE_BYTES);
        assert!(cut);
        let _ = std::fs::remove_dir_all(&dir);
    }

    #[test]
    fn render_lists_the_facts_and_never_the_token_or_folders() {
        let program = PathBuf::from("splitter-not-there.exe");
        let f = Facts {
            stage: "the engine exited before it was ready",
            exit_code: Some(1),
            program: &program,
            engine_text: Some((
                format!("splitter: config: open C:\\ProgramData\\splitter\\app.yaml: denied {TOKEN}\nexit code: 1\n"),
                false,
            )),
        };
        let d = render(&f, TOKEN);
        assert!(d.contains("stage: the engine exited before it was ready"), "{d}");
        assert!(d.contains("exit code: 1\n"), "{d}");
        assert!(d.contains("engine: splitter-not-there.exe (missing)"), "{d}");
        assert!(d.contains("open …/app.yaml: denied [hidden]"), "{d}");
        assert!(!d.contains(TOKEN) && !d.contains("ProgramData"), "{d}");
    }

    #[test]
    fn render_without_an_error_file_says_so_and_a_huge_text_is_cut() {
        let program = PathBuf::from("splitter");
        let f = Facts { stage: "timeout", exit_code: None, program: &program, engine_text: None };
        let d = render(&f, TOKEN);
        assert!(d.contains("none (the engine wrote no error file)") && !d.contains("exit code"), "{d}");
        let long = "word ".repeat(2000);
        let f = Facts { stage: "x", exit_code: None, program: &program, engine_text: Some((long, true)) };
        let d = render(&f, TOKEN);
        assert!(d.chars().count() <= MAX_DETAILS_CHARS + 20, "{}", d.chars().count());
        assert!(d.ends_with("(shortened)"), "{d}");
    }
}
