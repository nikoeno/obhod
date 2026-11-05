package desktop

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
)

// Token rules (api/desktop/API.md, "Auth"): 32 to 256 printable ASCII
// characters without spaces, as a random base64url string has.
const (
	minTokenLen = 32
	maxTokenLen = 256
	maxTokenRaw = 4 << 10 // a file larger than this is not a token file
)

// readTokenFile reads the token once. Errors name the path and the rule that
// failed, never the content.
func readTokenFile(path string) (string, error) {
	if err := checkTokenPerm(path); err != nil {
		return "", err
	}
	f, err := os.Open(path)
	if err != nil {
		var pe *fs.PathError
		if errors.As(err, &pe) {
			err = pe.Err
		}
		return "", fmt.Errorf("--token-file %s: %w", path, err)
	}
	defer f.Close()
	raw, err := io.ReadAll(io.LimitReader(f, maxTokenRaw+1))
	if err != nil {
		return "", fmt.Errorf("--token-file %s: cannot read the file", path)
	}
	if len(raw) > maxTokenRaw {
		return "", fmt.Errorf("--token-file %s: the file is too large for a token (at most %d characters)", path, maxTokenLen)
	}
	tok := trimLineEnd(string(raw))
	if err := checkToken(tok); err != nil {
		return "", fmt.Errorf("--token-file %s: %w", path, err)
	}
	return tok, nil
}

// trimLineEnd removes trailing \r and \n characters.
func trimLineEnd(s string) string {
	for len(s) > 0 && (s[len(s)-1] == '\n' || s[len(s)-1] == '\r') {
		s = s[:len(s)-1]
	}
	return s
}

// checkToken applies the token rules. The error never contains the token.
func checkToken(tok string) error {
	switch {
	case len(tok) == 0:
		return errors.New("the token is empty")
	case len(tok) < minTokenLen:
		return fmt.Errorf("the token is too short (at least %d characters)", minTokenLen)
	case len(tok) > maxTokenLen:
		return fmt.Errorf("the token is too long (at most %d characters)", maxTokenLen)
	}
	for i := 0; i < len(tok); i++ {
		if c := tok[i]; c < 0x21 || c > 0x7e {
			return errors.New("the token must be printable ASCII without spaces")
		}
	}
	return nil
}

// modeTooOpen reports whether a file mode lets group or others read the file.
func modeTooOpen(m fs.FileMode) bool { return m.Perm()&0o077 != 0 }
