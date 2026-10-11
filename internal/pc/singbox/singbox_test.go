package singbox

import (
	"bytes"
	"encoding/json"
	"errors"
	"flag"
	"math/rand"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/nikoejento/obhod/internal/lib/route"
)

var update = flag.Bool("update", false, "rewrite golden files")

var (
	tunnelJSON = json.RawMessage(`{"type":"vless","tag":"whatever","server":"203.0.113.7","server_port":443,"uuid":"00000000-0000-0000-0000-000000000000","flow":"xtls-rprx-vision","tls":{"enabled":true,"server_name":"example.org","reality":{"enabled":true,"public_key":"AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA","short_id":"0123456789abcdef"}}}`)
	proxyJSON  = json.RawMessage(`{"type":"socks","server":"127.0.0.1","server_port":1080}`)
)

func baseConfig() Config {
	return Config{
		Binary:       "sing-box",
		DNS:          DNSConfig{DirectDoH: "https://1.1.1.1/dns-query"},
		ClashSecret:  "test-secret",
		OwnProcesses: []string{"winws2.exe", "splitter.exe", "sing-box.exe"},
		BlockQUIC:    true,
	}
}

func entries() []route.Entry {
	return []route.Entry{
		{Domain: "lan.example", Route: route.Direct, Source: route.SourceChecked},
		{Domain: "yt.example", Route: route.Desync, Source: route.SourceChecked},
		{Domain: "blocked.example", Route: route.Tunnel, Source: route.SourceChecked},
		{Domain: "corp.example", Route: route.Proxy, Source: route.SourceStatic},
		{Domain: "ads.example", Route: route.Block, Source: route.SourceStatic},
		{Domain: "a.yt.example", Route: route.Direct, Source: route.SourceChecked},
	}
}

func withoutProxy(es []route.Entry) []route.Entry {
	var out []route.Entry
	for _, e := range es {
		if e.Route != route.Proxy {
			out = append(out, e)
		}
	}
	return out
}

func table(t *testing.T, es []route.Entry) route.Table {
	t.Helper()
	tb, err := route.NewTable("net", time.Unix(0, 0).UTC(), es)
	if err != nil {
		t.Fatal(err)
	}
	return tb
}

func golden(t *testing.T, name string, got []byte) {
	t.Helper()
	path := filepath.Join("testdata", name+".golden.json")
	if *update {
		if err := os.MkdirAll("testdata", 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, got, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(want, got) {
		t.Fatalf("%s differs from golden (run with -update)\n%s", name, got)
	}
}

func TestGolden(t *testing.T) {
	noQUIC := baseConfig()
	noQUIC.BlockQUIC = false
	cases := []struct {
		name string
		cfg  Config
		ob   Outbounds
		es   []route.Entry
	}{
		{"empty", baseConfig(), Outbounds{Tunnel: tunnelJSON}, nil},
		{"mixed", baseConfig(), Outbounds{Tunnel: tunnelJSON, Proxy: proxyJSON}, entries()},
		{"proxy_unset", baseConfig(), Outbounds{Tunnel: tunnelJSON}, withoutProxy(entries())},
		{"quic_off", noQUIC, Outbounds{Tunnel: tunnelJSON, Proxy: proxyJSON}, entries()},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := Generate(c.cfg, c.ob, table(t, c.es))
			if err != nil {
				t.Fatal(err)
			}
			if !json.Valid(got) {
				t.Fatal("invalid JSON")
			}
			golden(t, c.name, got)
		})
	}
}

func TestRuleOrderAndTags(t *testing.T) {
	got, err := Generate(baseConfig(), Outbounds{Tunnel: tunnelJSON, Proxy: proxyJSON}, table(t, entries()))
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Route struct {
			Rules []map[string]any `json:"rules"`
			Final string           `json:"final"`
		} `json:"route"`
		Outbounds []map[string]any `json:"outbounds"`
	}
	if err := json.Unmarshal(got, &doc); err != nil {
		t.Fatal(err)
	}
	var seq []string
	for _, r := range doc.Route.Rules {
		switch {
		case r["action"] == "sniff":
			seq = append(seq, "sniff")
		case r["action"] == "hijack-dns":
			seq = append(seq, "dns")
		case r["process_name"] != nil:
			seq = append(seq, "own")
		case r["network"] == "udp":
			seq = append(seq, "quic")
		case r["action"] == "reject":
			seq = append(seq, "block")
		default:
			seq = append(seq, r["outbound"].(string))
		}
	}
	want := []string{"sniff", "dns", "own", "quic", "block", "proxy", "tunnel", "desync", "direct"}
	if len(seq) != len(want) {
		t.Fatalf("rule order = %v, want %v", seq, want)
	}
	for i := range want {
		if seq[i] != want[i] {
			t.Fatalf("rule order = %v, want %v", seq, want)
		}
	}
	if doc.Route.Final != "tunnel" {
		t.Fatalf("final = %q", doc.Route.Final)
	}
	tags := map[string]bool{}
	for _, o := range doc.Outbounds {
		tags[o["tag"].(string)] = true
	}
	for _, tag := range []string{"direct", "desync", "tunnel", "proxy"} {
		if !tags[tag] {
			t.Errorf("missing outbound %q", tag)
		}
	}
}

func TestDeterministic(t *testing.T) {
	cfg := baseConfig()
	ob := Outbounds{Tunnel: tunnelJSON, Proxy: proxyJSON}
	want, err := Generate(cfg, ob, table(t, entries()))
	if err != nil {
		t.Fatal(err)
	}
	rng := rand.New(rand.NewSource(1))
	for range 20 {
		es := entries()
		rng.Shuffle(len(es), func(i, j int) { es[i], es[j] = es[j], es[i] })
		c := cfg
		c.OwnProcesses = []string{"sing-box.exe", "winws2.exe", "splitter.exe", "winws2.exe"}
		// Entries go through NewTable, so shuffled input must produce the same bytes.
		got, err := Generate(c, ob, table(t, es))
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(want, got) {
			t.Fatal("output depends on input order")
		}
	}
}

func TestErrors(t *testing.T) {
	tb := table(t, entries())
	cases := []struct {
		name string
		mut  func(*Config, *Outbounds, *route.Table)
		want error
	}{
		{"no tunnel", func(_ *Config, o *Outbounds, _ *route.Table) { o.Tunnel = nil }, ErrNoTunnel},
		{"proxy route without outbound", func(_ *Config, o *Outbounds, _ *route.Table) { o.Proxy = nil }, ErrNoProxy},
		{"non-loopback api", func(c *Config, _ *Outbounds, _ *route.Table) { c.ClashAPI = "0.0.0.0:9090" }, ErrInvalidCfg},
		{"lan api", func(c *Config, _ *Outbounds, _ *route.Table) { c.ClashAPI = "192.168.1.5:9090" }, ErrInvalidCfg},
		{"hostname api", func(c *Config, _ *Outbounds, _ *route.Table) { c.ClashAPI = "example.com:9090" }, ErrInvalidCfg},
		{"api without port", func(c *Config, _ *Outbounds, _ *route.Table) { c.ClashAPI = "127.0.0.1" }, ErrInvalidCfg},
		{"no doh", func(c *Config, _ *Outbounds, _ *route.Table) { c.DNS.DirectDoH = "" }, ErrInvalidCfg},
		{"doh hostname", func(c *Config, _ *Outbounds, _ *route.Table) { c.DNS.DirectDoH = "https://dns.example/dns-query" }, ErrInvalidCfg},
		{"bad fakeip", func(c *Config, _ *Outbounds, _ *route.Table) { c.DNS.FakeIPRange = "fc00::/18" }, ErrInvalidCfg},
		{"bad tun address", func(c *Config, _ *Outbounds, _ *route.Table) { c.TUN.Address = "nope" }, ErrInvalidCfg},
		{"invalid table", func(_ *Config, _ *Outbounds, t *route.Table) {
			t.Entries = []route.Entry{{Domain: "a.example", Route: "bogus"}}
		}, route.ErrInvalidRoute},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			cfg, ob, tab := baseConfig(), Outbounds{Tunnel: tunnelJSON, Proxy: proxyJSON}, tb
			c.mut(&cfg, &ob, &tab)
			if _, err := Generate(cfg, ob, tab); !errors.Is(err, c.want) {
				t.Fatalf("err = %v, want %v", err, c.want)
			}
		})
	}
	if _, err := Generate(baseConfig(), Outbounds{Tunnel: json.RawMessage(`[1]`)}, table(t, nil)); err == nil {
		t.Fatal("non-object tunnel accepted")
	}
	if _, err := Generate(baseConfig(), Outbounds{Tunnel: json.RawMessage(`{"server":"x"}`)}, table(t, nil)); err == nil {
		t.Fatal("tunnel without type accepted")
	}
}

func TestDoHForms(t *testing.T) {
	for in, want := range map[string][3]any{
		"1.1.1.1":                       {"1.1.1.1", 443, "/dns-query"},
		"https://9.9.9.9:8443/custom":   {"9.9.9.9", 8443, "/custom"},
		"https://[2606:4700::1111]/dns": {"2606:4700::1111", 443, "/dns"},
	} {
		h, p, path, err := parseDoH(in)
		if err != nil || h != want[0] || p != want[1] || path != want[2] {
			t.Errorf("%q → %v %v %v %v", in, h, p, path, err)
		}
	}
}

func TestCommand(t *testing.T) {
	path, args := Command(Config{Binary: "/x/sing-box"}, "/run/sing-box.json")
	if path != "/x/sing-box" || len(args) != 3 || args[0] != "run" || args[2] != "/run/sing-box.json" {
		t.Fatalf("Command = %q %q", path, args)
	}
}
