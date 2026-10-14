package desync

import (
	"errors"
	"net/netip"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/nikoejento/obhod/internal/lib/netif"
	"github.com/nikoejento/obhod/internal/lib/route"
)

func tableOf(t *testing.T, es ...route.Entry) route.Table {
	t.Helper()
	tb, err := route.NewTable("net", time.Unix(0, 0), es)
	if err != nil {
		t.Fatal(err)
	}
	return tb
}

func desyncTable(t *testing.T) route.Table {
	return tableOf(t,
		route.Entry{Domain: "yt.example", Route: route.Desync, Source: route.SourceChecked},
		route.Entry{Domain: "a.example", Route: route.Direct, Source: route.SourceChecked},
		route.Entry{Domain: "b.example", Route: route.Tunnel, Source: route.SourceChecked},
	)
}

func uplink() netif.Interface { return netif.Interface{Name: "eth0", Index: 9} }

func cfg(e Engine) Config {
	return Config{
		Engine:     e,
		Binary:     "/opt/engine",
		Strategy:   []string{"--filter-l7=tls", "--lua-desync=fake"},
		ExcludeIPs: []netip.Prefix{netip.MustParsePrefix("203.0.113.7/32")},
	}
}

func TestBuildWinWS(t *testing.T) {
	p, err := Build(cfg(WinWS), uplink(), desyncTable(t))
	if err != nil {
		t.Fatal(err)
	}
	if p.Engine != WinWS || p.Path != "/opt/engine" || len(p.Setup) != 0 || len(p.Teardown) != 0 {
		t.Fatalf("plan = %+v", p)
	}
	if string(p.Files["hostlist.txt"]) != "yt.example\n" {
		t.Fatalf("files = %v", p.Files)
	}
	all := strings.Join(p.Args, " ")
	if !strings.Contains(all, "ifIdx == 9") || !strings.Contains(all, "203.0.113.7") || !strings.Contains(all, "--hostlist=hostlist.txt") {
		t.Fatalf("args = %q", p.Args)
	}
}

func TestBuildNFQWS(t *testing.T) {
	c := cfg(NFQWS)
	c.ExcludePorts = []PortRange{{50000, 50099}}
	p, err := Build(c, uplink(), desyncTable(t))
	if err != nil {
		t.Fatal(err)
	}
	if p.Engine != NFQWS || len(p.Setup) == 0 || len(p.Teardown) != 2 {
		t.Fatalf("plan = %+v", p)
	}
	if !strings.Contains(strings.Join(p.Setup, "\n"), `oifname "eth0"`) ||
		!strings.Contains(strings.Join(p.Setup, "\n"), "50000-50099") {
		t.Fatalf("setup = %q", p.Setup)
	}
	if string(p.Files["hostlist.txt"]) != "yt.example\n" || p.Args[0] != "--qnum=200" {
		t.Fatalf("plan = %+v", p)
	}
	if !strings.Contains(strings.Join(p.Args, " "), "--hostlist=hostlist.txt") || !slices.Contains(p.Args, "--uid=0:0") {
		t.Fatalf("args = %q", p.Args)
	}
}

func TestOnlyDesyncDomainsInHostlist(t *testing.T) {
	tb := tableOf(t,
		route.Entry{Domain: "yt.example", Route: route.Desync, Source: route.SourceChecked},
		route.Entry{Domain: "also.example", Route: route.Desync, Source: route.SourceChecked},
		route.Entry{Domain: "no.example", Route: route.Tunnel, Source: route.SourceChecked},
		route.Entry{Domain: "plain.example", Route: route.Direct, Source: route.SourceChecked},
	)
	for _, e := range []Engine{WinWS, NFQWS} {
		p, err := Build(cfg(e), uplink(), tb)
		if err != nil {
			t.Fatal(err)
		}
		if got := string(p.Files["hostlist.txt"]); got != "also.example\nyt.example\n" {
			t.Fatalf("%s hostlist = %q", e, got)
		}
	}
}

func TestEmptyDesyncList(t *testing.T) {
	tb := tableOf(t, route.Entry{Domain: "a.example", Route: route.Tunnel, Source: route.SourceChecked})
	for _, e := range []Engine{WinWS, NFQWS} {
		p, err := Build(cfg(e), uplink(), tb)
		if err != nil {
			t.Fatal(err)
		}
		if p.Engine != e || p.Path != "" || p.Args != nil || p.Files != nil || p.Setup != nil || p.Teardown != nil {
			t.Fatalf("%s: plan = %+v, want an empty plan", e, p)
		}
	}
}

func TestNoExcludeIP(t *testing.T) {
	c := cfg(WinWS)
	c.ExcludeIPs = nil
	// Checked first, even when there is nothing to desync and the engine is unknown.
	for _, tb := range []route.Table{desyncTable(t), tableOf(t)} {
		if _, err := Build(c, uplink(), tb); !errors.Is(err, ErrNoExcludeIP) {
			t.Fatalf("err = %v", err)
		}
	}
	c.Engine = "bogus"
	if _, err := Build(c, uplink(), tableOf(t)); !errors.Is(err, ErrNoExcludeIP) {
		t.Fatalf("err = %v", err)
	}
}

func TestVirtualUplinkRejected(t *testing.T) {
	up := uplink()
	up.Virtual = true
	for _, e := range []Engine{WinWS, NFQWS} {
		if _, err := Build(cfg(e), up, desyncTable(t)); !errors.Is(err, ErrVirtualUplink) {
			t.Fatalf("%s: err = %v", e, err)
		}
	}
}

func TestUnsupportedEngine(t *testing.T) {
	if _, err := Build(cfg("bogus"), uplink(), desyncTable(t)); !errors.Is(err, ErrUnsupported) {
		t.Fatalf("err = %v", err)
	}
	// The engine is checked before the empty list.
	if _, err := Build(cfg("bogus"), uplink(), tableOf(t)); !errors.Is(err, ErrUnsupported) {
		t.Fatalf("empty list: err = %v", err)
	}
}

func TestVirtualUplinkWithEmptyListIsEmptyPlan(t *testing.T) {
	up := uplink()
	up.Virtual = true
	p, err := Build(cfg(WinWS), up, tableOf(t,
		route.Entry{Domain: "a.example", Route: route.Direct, Source: route.SourceChecked}))
	if err != nil || p.Path != "" || p.Engine != WinWS {
		t.Fatalf("plan = %+v, err = %v", p, err)
	}
}

func TestStrategyCannotOverrideAppOptions(t *testing.T) {
	for _, arg := range []string{
		"--wf-raw-filter=true", "--wf-iface=1", "--wf-save=x.txt", "--wf-tcp-out=1-65535",
		"--new", "--skip", "--hostlist=/etc/other.txt", "--hostlist-domains=example.com",
		"--hostlist-auto=auto.txt", "--uid=1000", "--user=nobody", "--ipset=ips.txt", "--qnum=1", "--fwmark=0x1",
		"--lua-init=@evil.lua", "@args.txt",
	} {
		for _, e := range []Engine{WinWS, NFQWS} {
			c := cfg(e)
			c.Strategy = append(c.Strategy, arg)
			if _, err := Build(c, uplink(), desyncTable(t)); !errors.Is(err, ErrInvalidConfig) {
				t.Errorf("%s %q: err = %v", e, arg, err)
			}
			// Also with no desync domains: a bad config is an error either way.
			if _, err := Build(c, uplink(), tableOf(t)); !errors.Is(err, ErrInvalidConfig) {
				t.Errorf("%s %q, empty list: err = %v", e, arg, err)
			}
		}
	}
	c := cfg(WinWS)
	c.Strategy = []string{"--filter-tcp=443", "--filter-l7=tls", "--payload=tls_client_hello", "--lua-desync=fake:blob=fake_default_tls", "--newline"}
	if _, err := Build(c, uplink(), desyncTable(t)); err != nil {
		t.Fatalf("ordinary strategy rejected: %v", err)
	}
}

func TestConfigCheck(t *testing.T) {
	for name, mut := range map[string]func(*Config){
		"port 0":         func(c *Config) { c.Ports = []uint16{443, 0} },
		"reversed range": func(c *Config) { c.ExcludePorts = []PortRange{{2000, 1000}} },
		"zero range":     func(c *Config) { c.ExcludePorts = []PortRange{{0, 10}} },
		"invalid prefix": func(c *Config) { c.ExcludeIPs = append(c.ExcludeIPs, netip.Prefix{}) },
		"lua path":       func(c *Config) { c.LuaInit = []string{"../x.lua"} },
	} {
		c := cfg(WinWS)
		mut(&c)
		if err := c.Check(); !errors.Is(err, ErrInvalidConfig) {
			t.Errorf("%s: err = %v", name, err)
		}
	}
	if err := cfg(NFQWS).Check(); err != nil {
		t.Fatal(err)
	}
}

func TestIPv4MappedExcludeIsUnmapped(t *testing.T) {
	for _, e := range []Engine{WinWS, NFQWS} {
		c := cfg(e)
		c.ExcludeIPs = []netip.Prefix{netip.MustParsePrefix("::ffff:198.51.100.9/128"), netip.MustParsePrefix("::ffff:192.0.2.0/120")}
		p, err := Build(c, uplink(), desyncTable(t))
		if err != nil {
			t.Fatal(err)
		}
		all := strings.Join(append(p.Args, p.Setup...), " ")
		if strings.Contains(all, "::ffff") || strings.Contains(all, "ipv6.DstAddr") || strings.Contains(all, "ip6 daddr") {
			t.Fatalf("%s: mapped address rendered as IPv6: %s", e, all)
		}
		if !strings.Contains(all, "198.51.100.9") || !strings.Contains(all, "192.0.2.") {
			t.Fatalf("%s: unmapped address missing: %s", e, all)
		}
	}
}

func TestDefaultEngine(t *testing.T) {
	c := cfg("")
	p, err := Build(c, uplink(), desyncTable(t))
	switch e := DefaultEngine(); e {
	case "":
		if !errors.Is(err, ErrUnsupported) {
			t.Fatalf("err = %v", err)
		}
	default:
		if err != nil || p.Engine != e {
			t.Fatalf("plan = %+v, err = %v", p, err)
		}
	}
}

func TestChildErrorsPropagate(t *testing.T) {
	c := cfg(WinWS)
	c.Binary = ""
	if _, err := Build(c, uplink(), desyncTable(t)); err == nil {
		t.Fatal("missing binary accepted")
	}
	up := uplink()
	up.Index = 0
	if _, err := Build(cfg(WinWS), up, desyncTable(t)); err == nil {
		t.Fatal("zero interface index accepted")
	}
	up = uplink()
	up.Name = ""
	if _, err := Build(cfg(NFQWS), up, desyncTable(t)); err == nil {
		t.Fatal("empty uplink name accepted")
	}
}
