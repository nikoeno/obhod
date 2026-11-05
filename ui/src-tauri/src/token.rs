//! The control-API token: generation and the user-only file handed to `splitter --token-file`.
//!
//! Contract (`api/desktop/API.md`, Auth): printable ASCII without spaces, 32 to 256 characters, at
//! least 32 bytes of entropy. 32 random bytes in base64url are 43 characters. The file is written
//! without a trailing newline; on Unix it is created with mode 0600. On Windows it lives in the
//! per-user app data directory, whose ACL is inherited from the user profile.

use std::fs::{self, OpenOptions};
use std::io::{self, Write};
use std::path::Path;

const B64URL: &[u8; 64] = b"ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789-_";

/// Number of random bytes in a token.
pub const TOKEN_BYTES: usize = 32;

/// Base64url without padding (RFC 4648, section 5).
pub fn base64url(bytes: &[u8]) -> String {
    let mut out = String::with_capacity((bytes.len() * 4).div_ceil(3));
    for chunk in bytes.chunks(3) {
        let b = [chunk[0], *chunk.get(1).unwrap_or(&0), *chunk.get(2).unwrap_or(&0)];
        let n = (u32::from(b[0]) << 16) | (u32::from(b[1]) << 8) | u32::from(b[2]);
        out.push(B64URL[(n >> 18) as usize & 63] as char);
        out.push(B64URL[(n >> 12) as usize & 63] as char);
        if chunk.len() > 1 {
            out.push(B64URL[(n >> 6) as usize & 63] as char);
        }
        if chunk.len() > 2 {
            out.push(B64URL[n as usize & 63] as char);
        }
    }
    out
}

/// A fresh random token from the operating system's generator.
pub fn generate_token() -> io::Result<String> {
    let mut buf = [0u8; TOKEN_BYTES];
    getrandom::fill(&mut buf).map_err(|_| io::Error::other("no random source"))?;
    Ok(base64url(&buf))
}

/// True if `token` satisfies the engine's rule for token files.
pub fn is_valid_token(token: &str) -> bool {
    (32..=256).contains(&token.len()) && token.bytes().all(|b| b.is_ascii_graphic())
}

/// Writes `token` to `path` (parent directories are created), readable only by the current user
/// where the platform allows it. An existing file is replaced.
pub fn write_token_file(path: &Path, token: &str) -> io::Result<()> {
    if let Some(dir) = path.parent() {
        fs::create_dir_all(dir)?;
    }
    let mut opts = OpenOptions::new();
    opts.write(true).create(true).truncate(true);
    #[cfg(unix)]
    {
        use std::os::unix::fs::OpenOptionsExt;
        opts.mode(0o600);
    }
    let mut f = opts.open(path)?;
    #[cfg(unix)]
    {
        use std::os::unix::fs::PermissionsExt;
        f.set_permissions(fs::Permissions::from_mode(0o600))?;
    }
    f.write_all(token.as_bytes())?;
    f.sync_all()
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn base64url_matches_rfc_vectors() {
        assert_eq!(base64url(b""), "");
        assert_eq!(base64url(b"f"), "Zg");
        assert_eq!(base64url(b"fo"), "Zm8");
        assert_eq!(base64url(b"foo"), "Zm9v");
        assert_eq!(base64url(b"foob"), "Zm9vYg");
        assert_eq!(base64url(&[0xfb, 0xff, 0xfe]), "-__-");
    }

    #[test]
    fn tokens_are_valid_unique_and_long_enough() {
        let a = generate_token().unwrap();
        let b = generate_token().unwrap();
        assert_ne!(a, b);
        assert_eq!(a.len(), 43);
        assert!(is_valid_token(&a));
        assert!(!is_valid_token("short"));
        assert!(!is_valid_token(&"a b".repeat(20)));
        assert!(!is_valid_token(&"a".repeat(257)));
    }

    #[test]
    fn token_file_has_exact_content_and_replaces_old_one() {
        let dir = std::env::temp_dir().join(format!("obhod-token-test-{}", std::process::id()));
        let path = dir.join("nested").join("api.token");
        write_token_file(&path, "first-first-first-first-first-first-1").unwrap();
        write_token_file(&path, "second-second-second-second-second-2").unwrap();
        assert_eq!(fs::read_to_string(&path).unwrap(), "second-second-second-second-second-2");
        #[cfg(unix)]
        {
            use std::os::unix::fs::PermissionsExt;
            assert_eq!(fs::metadata(&path).unwrap().permissions().mode() & 0o777, 0o600);
        }
        let _ = fs::remove_dir_all(&dir);
    }
}
