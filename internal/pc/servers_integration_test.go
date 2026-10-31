//go:build integration

package pc

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/nikoejento/obhod/internal/lib/route"
	"github.com/nikoejento/obhod/internal/pc/servers"
	"github.com/nikoejento/obhod/internal/pc/singbox"
)

// TestServerFixturesPassPinnedSingBoxCheck renders every parse fixture as the
// tunnel outbound and runs the pinned `sing-box check` on the result.
func TestServerFixturesPassPinnedSingBoxCheck(t *testing.T) {
	name := "sing-box"
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	bin, err := filepath.Abs(filepath.Join("..", "..", "third_party", "bin", runtime.GOOS+"-"+runtime.GOARCH, name))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(bin); err != nil {
		t.Skipf("pinned sing-box not installed: %v", err)
	}
	// The check does bite: a Reality key of the wrong length passes Parse but not sing-box.
	t.Run("bad_reality_key_is_rejected", func(t *testing.T) {
		p, err := servers.Parse("vless://00000000-0000-4000-8000-000000000000@198.51.100.7:443?security=reality&sni=www.example.com&pbk=short&sid=0123abcd")
		if err != nil {
			t.Fatal(err)
		}
		cfg := DefaultConfig().SingBox
		cfg.ClashSecret = "integration"
		table, _ := route.NewTable("n", time.Unix(0, 0), []route.Entry{{Domain: "t.example", Route: route.Tunnel, Source: route.SourceStatic}})
		out, _ := singbox.Generate(cfg, singbox.Outbounds{Tunnel: p.Outbound}, table)
		file := filepath.Join(t.TempDir(), "sing-box.json")
		_ = os.WriteFile(file, out, 0o600)
		if err := singbox.Check(context.Background(), bin, file); err == nil {
			t.Fatal("sing-box accepted a Reality key of the wrong length")
		}
	})
	for _, in := range fixtureFiles(t) {
		id := strings.TrimSuffix(filepath.Base(in), ".in")
		t.Run(id, func(t *testing.T) {
			b, err := os.ReadFile(in)
			if err != nil {
				t.Fatal(err)
			}
			p, err := servers.Parse(string(b))
			if err != nil {
				t.Fatal(err)
			}
			cfg := DefaultConfig().SingBox
			cfg.ClashSecret = "integration"
			cfg.Binary = bin
			table, _ := route.NewTable("n", time.Unix(0, 0), []route.Entry{{Domain: "t.example", Route: route.Tunnel, Source: route.SourceStatic}})
			out, err := singbox.Generate(cfg, singbox.Outbounds{Tunnel: p.Outbound}, table)
			if err != nil {
				t.Fatal(err)
			}
			file := filepath.Join(t.TempDir(), "sing-box.json")
			if err := os.WriteFile(file, out, 0o600); err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
			defer cancel()
			if err := singbox.Check(ctx, bin, file); err != nil {
				t.Fatalf("sing-box check rejected %s: %v", id, err)
			}
		})
	}
}
