package desktop

import (
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeTokenFile(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "token")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestReadTokenFile(t *testing.T) {
	good := strings.Repeat("aB3-_", 8) // 40 characters
	for name, content := range map[string]string{
		"plain":        good,
		"lf":           good + "\n",
		"crlf":         good + "\r\n",
		"min length":   strings.Repeat("x", minTokenLen),
		"max length":   strings.Repeat("x", maxTokenLen),
		"all symbols":  "!\"#$%&'()*+,-./0123456789:;<=>?@ABCDEFGHIJKLMNOPQRSTUVWXYZ[\\]^_`abcdefghijklmnopqrstuvwxyz{|}~",
		"base64 token": "dGhpcyBpcyBhIHRva2VuIGZvciB0aGUgdGVzdHMgb25seQ",
	} {
		got, err := readTokenFile(writeTokenFile(t, content))
		if err != nil {
			t.Errorf("%s: %v", name, err)
		} else if got != strings.TrimRight(content, "\r\n") {
			t.Errorf("%s: wrong token", name)
		}
	}
}

func TestReadTokenFileErrorsNameThePathNotTheContent(t *testing.T) {
	const marker = "SECRETCONTENT"
	cases := map[string]string{
		"empty":         "",
		"only newline":  "\r\n",
		"short":         marker,
		"too long":      marker + strings.Repeat("x", maxTokenLen),
		"huge":          marker + strings.Repeat("x", maxTokenRaw+10),
		"space inside":  marker + " " + strings.Repeat("x", 40),
		"two lines":     marker + strings.Repeat("x", 40) + "\n" + marker,
		"tab":           marker + "\t" + strings.Repeat("x", 40),
		"non-ascii":     marker + "é" + strings.Repeat("x", 40),
		"nul":           marker + "\x00" + strings.Repeat("x", 40),
		"leading blank": " " + marker + strings.Repeat("x", 40),
	}
	for name, content := range cases {
		path := writeTokenFile(t, content)
		_, err := readTokenFile(path)
		if err == nil {
			t.Errorf("%s: accepted", name)
			continue
		}
		if strings.Contains(err.Error(), marker) {
			t.Errorf("%s: the error contains the content: %v", name, err)
		}
		if !strings.Contains(err.Error(), path) || !strings.Contains(err.Error(), "--token-file") {
			t.Errorf("%s: the error does not name the flag and the path: %v", name, err)
		}
	}
	missing := filepath.Join(t.TempDir(), "absent")
	if _, err := readTokenFile(missing); err == nil || !strings.Contains(err.Error(), missing) {
		t.Errorf("missing file: %v", err)
	}
	if _, err := readTokenFile(t.TempDir()); err == nil {
		t.Error("a directory was accepted")
	}
}

func TestModeTooOpen(t *testing.T) {
	for mode, want := range map[fs.FileMode]bool{0o600: false, 0o400: false, 0o700: false, 0o640: true, 0o604: true, 0o644: true, 0o660: true, 0o777: true} {
		if got := modeTooOpen(mode); got != want {
			t.Errorf("%04o: %v, want %v", mode, got, want)
		}
	}
}

func TestCheckListen(t *testing.T) {
	for _, ok := range []string{"127.0.0.1:0", "127.0.0.1:8080", "127.1.2.3:9", "[::1]:0", "[::1]:65535"} {
		if err := checkListen(ok); err != nil {
			t.Errorf("%s: %v", ok, err)
		}
	}
	for _, bad := range []string{"0.0.0.0:0", "[::]:0", ":0", "localhost:0", "example.org:80", "127.0.0.1", "127.0.0.1:", "127.0.0.1:x", "127.0.0.1:70000",
		"192.0.2.1:80", "[::1%lo]:0", "[::ffff:127.0.0.1]:0", "", "::1:80", "127.0.0.1:-1"} {
		if err := checkListen(bad); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
}

func TestWriteAddrFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "addr")
	for _, addr := range []string{"127.0.0.1:1111", "[::1]:2222"} {
		if err := writeAddrFile(path, addr); err != nil {
			t.Fatal(err)
		}
		if b, err := os.ReadFile(path); err != nil || string(b) != addr {
			t.Fatalf("content = %q, %v", b, err)
		}
	}
	if entries, _ := os.ReadDir(dir); len(entries) != 1 {
		t.Fatalf("temporary files left: %v", entries)
	}
	if err := writeAddrFile(filepath.Join(dir, "no", "such", "dir", "addr"), "x"); err == nil {
		t.Fatal("a missing directory was accepted")
	}
}
