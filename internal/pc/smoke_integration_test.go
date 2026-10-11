//go:build integration

package pc

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/nikoejento/obhod/internal/lib/paths"
)

// TestCheckConfigWithPinnedSingBox runs `splitter check-config` end to end with
// the pinned sing-box from third_party/bin. It skips when the binary is missing.
func TestCheckConfigWithPinnedSingBox(t *testing.T) {
	bin := "sing-box"
	if runtime.GOOS == "windows" {
		bin += ".exe"
	}
	pinned, err := filepath.Abs(filepath.Join("..", "..", "third_party", "bin", runtime.GOOS+"-"+runtime.GOARCH, bin))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(pinned); err != nil {
		t.Skipf("pinned sing-box not installed: %v", err)
	}

	p := paths.FromRoot(t.TempDir())
	if err := os.MkdirAll(p.Config, 0o700); err != nil {
		t.Fatal(err)
	}
	writeSecret(t, p, "tunnel.json", `{"type":"vless","server":"203.0.113.7","server_port":443,"uuid":"00000000-0000-0000-0000-000000000000","flow":"xtls-rprx-vision","tls":{"enabled":true,"server_name":"example.org","reality":{"enabled":true,"public_key":"0123456789abcdef0123456789abcdef0123456789a","short_id":"0123456789abcdef"},"utls":{"enabled":true,"fingerprint":"chrome"}}}`)
	if err := os.WriteFile(filepath.Join(p.Config, "app.yaml"), []byte("desync:\n  strategy: [\"--filter-l7=tls\"]\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	var out, errOut bytes.Buffer
	c := newCLI(&out, &errOut)
	c.defaultPaths = func() (paths.Paths, error) { return p, nil }
	c.signalCtx = func() (context.Context, context.CancelFunc) { return context.WithCancel(context.Background()) }
	realFind := c.check.findBinary
	c.check.findBinary = func(name, configured string) (string, error) {
		if name == "sing-box" {
			return pinned, nil
		}
		return realFind(name, configured)
	}
	if code := c.run([]string{"check-config"}); code != 0 {
		t.Fatalf("exit %d\nstdout: %s\nstderr: %s", code, out.String(), errOut.String())
	}
	if !strings.Contains(out.String(), "sing-box check: ok") {
		t.Fatalf("sing-box was not run: %s", out.String())
	}
}
