package desync

import (
	"errors"
	"net/netip"
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
	if p.Engine != NFQWS || len(p.Setup) == 0 || len(p.Teardown) != 1 {
		t.Fatalf("plan = %+v", p)
	}
	if !strings.Contains(strings.Join(p.Setup, "\n"), `oifname "eth0"`) ||
		!strings.Contains(strings.Join(p.Setup, "\n"), "50000-50099") {
		t.Fatalf("setup = %q", p.Setup)
	}
	if string(p.Files["hostlist.txt"]) != "yt.example\n" || p.Args[0] != "--qnum=200" {
		t.Fatalf("plan = %+v", p)
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
