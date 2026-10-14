//go:build integration

package singbox

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/nikoejento/obhod/internal/lib/route"
)

// pinnedBinary returns the sing-box from third_party/bin, or skips the test.
func pinnedBinary(t *testing.T) string {
	t.Helper()
	name := "sing-box"
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	p, err := filepath.Abs(filepath.Join("..", "..", "..", "third_party", "bin", runtime.GOOS+"-"+runtime.GOARCH, name))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(p); err != nil {
		t.Skipf("pinned sing-box not installed: %v", err)
	}
	return p
}

func TestPinnedCheck(t *testing.T) {
	bin := pinnedBinary(t)
	tunnel := json.RawMessage(`{"type":"vless","server":"203.0.113.7","server_port":443,"uuid":"00000000-0000-0000-0000-000000000000","flow":"xtls-rprx-vision","tls":{"enabled":true,"server_name":"example.org","reality":{"enabled":true,"public_key":"` + "0123456789abcdef0123456789abcdef0123456789a" + `","short_id":"0123456789abcdef"},"utls":{"enabled":true,"fingerprint":"chrome"}}}`)
	proxy := json.RawMessage(`{"type":"socks","server":"127.0.0.1","server_port":1080}`)

	for _, quic := range []bool{true, false} {
		for _, withProxy := range []bool{true, false} {
			es := []route.Entry{
				{Domain: "a.example", Route: route.Direct, Source: route.SourceChecked}, {Domain: "b.example", Route: route.Desync, Source: route.SourceChecked},
				{Domain: "c.example", Route: route.Tunnel, Source: route.SourceChecked}, {Domain: "d.example", Route: route.Block, Source: route.SourceStatic},
				// Nested entries: per-level route rules and the FakeIP DNS rules with domain_suffix + query_type.
				{Domain: "x.a.example", Route: route.Tunnel, Source: route.SourceChecked}, {Domain: "y.x.a.example", Route: route.Desync, Source: route.SourceChecked},
				{Domain: "ok.d.example", Route: route.Direct, Source: route.SourceChecked},
			}
			ob := Outbounds{Tunnel: tunnel}
			if withProxy {
				ob.Proxy = proxy
				es = append(es, route.Entry{Domain: "e.example", Route: route.Proxy, Source: route.SourceStatic})
			}
			tb, err := route.NewTable("n", time.Unix(0, 0), es)
			if err != nil {
				t.Fatal(err)
			}
			cfg := baseConfig()
			cfg.BlockQUIC = quic
			cfg.Binary = bin
			if withProxy {
				cfg.DNS.DirectDoH = "2606:4700::1111" // bare IPv6 form
			}
			out, err := Generate(cfg, ob, tb)
			if err != nil {
				t.Fatal(err)
			}
			f := filepath.Join(t.TempDir(), "sing-box.json")
			if err := os.WriteFile(f, out, 0o600); err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			err = Check(ctx, bin, f)
			cancel()
			if err != nil {
				t.Fatalf("quic=%v proxy=%v: %v\n%s", quic, withProxy, err, out)
			}
		}
	}
}

func TestCheckReportsOutput(t *testing.T) {
	bin := pinnedBinary(t)
	f := filepath.Join(t.TempDir(), "bad.json")
	if err := os.WriteFile(f, []byte(`{"route":{"rules":[{"action":"bogus"}]}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	err := Check(context.Background(), bin, f)
	if err == nil {
		t.Fatal("expected check failure")
	}
	t.Log(err)
}
