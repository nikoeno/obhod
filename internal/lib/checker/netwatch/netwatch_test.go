package netwatch

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nikoejento/obhod/internal/lib/analyzer"
	"github.com/nikoejento/obhod/internal/lib/clock"
	"github.com/nikoejento/obhod/internal/lib/netif"
)

var t0 = time.Date(2026, 6, 1, 9, 0, 0, 0, time.UTC)

// cymru answers the two TXT queries LookupASN makes.
type cymru struct {
	mu   sync.Mutex
	asns map[netip.Addr]uint32
	down bool
}

func (c *cymru) LookupIP(context.Context, string) ([]netip.Addr, error) {
	return nil, errors.New("unused")
}

func (c *cymru) LookupTXT(_ context.Context, name string) ([]string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.down {
		return nil, errors.New("resolver down")
	}
	if strings.HasSuffix(name, ".origin.asn.cymru.com") {
		for ip, n := range c.asns {
			b := ip.As4()
			if name == fmt.Sprintf("%d.%d.%d.%d.origin.asn.cymru.com", b[3], b[2], b[1], b[0]) {
				return []string{fmt.Sprintf("%d | %s/24 | RU | ripencc | 2000-01-01", n, ip)}, nil
			}
		}
	}
	var n uint32
	if _, err := fmt.Sscanf(name, "AS%d.asn.cymru.com", &n); err == nil {
		return []string{fmt.Sprintf("%d | RU | ripencc | 2000-01-01 | ISP-%d", n, n)}, nil
	}
	return nil, errors.New("nxdomain")
}

type env struct {
	mu     sync.Mutex
	ifc    netif.Interface
	upErr  error
	ip     netip.Addr
	ipErr  error
	events chan netif.Event
	dns    *cymru
	calls  chan struct{} // one entry per Uplink call, i.e. per identification
}

func newEnv() *env {
	ip := netip.MustParseAddr("95.24.1.7")
	return &env{
		ifc:    netif.Interface{Name: "eth0", Gateway: netip.MustParseAddr("192.168.1.1"), GatewayMAC: net.HardwareAddr{2, 0, 0, 0, 0, 1}},
		ip:     ip,
		events: make(chan netif.Event),
		dns:    &cymru{asns: map[netip.Addr]uint32{ip: 12389}},
		calls:  make(chan struct{}, 100),
	}
}

func (e *env) deps(clk clock.Clock) Deps {
	return Deps{
		Clock:    clk,
		Resolver: e.dns,
		PublicIP: func(context.Context) (netip.Addr, error) {
			e.mu.Lock()
			defer e.mu.Unlock()
			return e.ip, e.ipErr
		},
		Uplink: func(context.Context) (netif.Interface, error) {
			e.mu.Lock()
			defer e.mu.Unlock()
			e.calls <- struct{}{}
			return e.ifc, e.upErr
		},
		Events: func(context.Context) (<-chan netif.Event, error) { return e.events, nil },
	}
}

func (e *env) set(f func(e *env)) {
	e.mu.Lock()
	defer e.mu.Unlock()
	f(e)
}

type run struct {
	w   *Watcher
	clk *clock.Fake
	e   *env
}

func start(t *testing.T, cfg Config, e *env) *run {
	t.Helper()
	clk := clock.NewFake(t0)
	w := New(cfg, e.deps(clk))
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { w.Run(ctx); close(done) }()
	t.Cleanup(func() {
		cancel()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Error("Run did not return")
		}
	})
	return &run{w: w, clk: clk, e: e}
}

func (r *run) change(t *testing.T) Network {
	t.Helper()
	select {
	case n := <-r.w.Changes():
		return n
	case <-time.After(5 * time.Second):
		t.Fatal("no change event")
	}
	return Network{}
}

func (r *run) noChange(t *testing.T) {
	t.Helper()
	select {
	case n := <-r.w.Changes():
		t.Fatalf("unexpected change %+v", n)
	case <-time.After(50 * time.Millisecond):
	}
}

func (r *run) call(t *testing.T) {
	t.Helper()
	select {
	case <-r.e.calls:
	case <-time.After(5 * time.Second):
		t.Fatal("no identification")
	}
}

func (r *run) waiters(t *testing.T, n int) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := r.clk.BlockUntil(ctx, n); err != nil {
		t.Fatal(err)
	}
}

func TestASNPath(t *testing.T) {
	r := start(t, Config{Salt: "s"}, newEnv())
	n := r.change(t)
	if !n.Known || n.ID != "asn:12389" || n.ASN != 12389 || n.ASName != "ISP-12389" || n.Uplink != "eth0" {
		t.Errorf("network = %+v", n)
	}
	if n.PublicIP != netip.MustParseAddr("95.24.1.7") {
		t.Errorf("PublicIP = %v", n.PublicIP)
	}
	if got := r.w.Current(); got.ID != n.ID {
		t.Errorf("Current = %+v", got)
	}
}

func TestGatewayFallback(t *testing.T) {
	e := newEnv()
	e.set(func(e *env) { e.ipErr = errors.New("no internet") })
	r := start(t, Config{Salt: "s"}, e)
	n := r.change(t)
	if !n.Known || !strings.HasPrefix(string(n.ID), "gw:") || len(n.ID) != len("gw:")+16 || n.ASN != 0 {
		t.Errorf("network = %+v", n)
	}
}

func TestIncludeGateway(t *testing.T) {
	r := start(t, Config{Salt: "s", IncludeGateway: true}, newEnv())
	n := r.change(t)
	if !strings.HasPrefix(string(n.ID), "asn:12389/gw:") || !n.ID.Valid() {
		t.Errorf("ID = %q", n.ID)
	}
}

func TestNothingAvailableIsUnknown(t *testing.T) {
	e := newEnv()
	e.set(func(e *env) { e.ipErr = errors.New("x"); e.ifc.GatewayMAC = nil; e.ifc.Gateway = netip.Addr{} })
	r := start(t, Config{Salt: "s"}, e)
	r.call(t)
	r.noChange(t) // never known: nothing to announce
	if n := r.w.Current(); n.Known || n.ID != "" {
		t.Errorf("Current = %+v", n)
	}
}

func TestSaltStability(t *testing.T) {
	id := func(salt string) string {
		e := newEnv()
		e.set(func(e *env) { e.ipErr = errors.New("x") })
		r := start(t, Config{Salt: salt}, e)
		return string(r.change(t).ID)
	}
	a1, a2, b := id("salt-a"), id("salt-a"), id("salt-b")
	if a1 != a2 {
		t.Errorf("same salt gave %q and %q", a1, a2)
	}
	if a1 == b {
		t.Errorf("different salts gave the same ID %q", a1)
	}
}

func TestDebounceAndNoChangeNoEvent(t *testing.T) {
	e := newEnv()
	r := start(t, Config{Salt: "s", Debounce: 3 * time.Second}, e)
	r.change(t)
	r.call(t) // the initial identification

	for i := 0; i < 5; i++ {
		e.events <- netif.Event{At: t0, Kind: netif.AddressChanged}
	}
	r.waiters(t, 2) // refresh + debounce
	r.clk.Advance(3 * time.Second)
	r.call(t)
	r.noChange(t) // same ID → no event
	select {
	case <-e.calls:
		t.Error("more than one identification for a burst of events")
	case <-time.After(50 * time.Millisecond):
	}

	// A real change goes through after the debounce.
	e.set(func(e *env) {
		ip := netip.MustParseAddr("178.1.2.3")
		e.ip = ip
		e.dns.mu.Lock()
		e.dns.asns[ip] = 8402
		e.dns.mu.Unlock()
	})
	e.events <- netif.Event{At: t0, Kind: netif.UplinkChanged}
	r.waiters(t, 2)
	r.clk.Advance(3 * time.Second)
	if n := r.change(t); n.ID != "asn:8402" {
		t.Errorf("after switch: %+v", n)
	}
}

func TestRefreshIdentifiesAgain(t *testing.T) {
	r := start(t, Config{Salt: "s", Refresh: 10 * time.Minute}, newEnv())
	r.change(t)
	r.call(t)
	r.waiters(t, 1)
	r.clk.Advance(10 * time.Minute)
	r.call(t)
}

func TestFailureKeepsLastIDOnSameUplink(t *testing.T) {
	e := newEnv()
	r := start(t, Config{Salt: "s", Debounce: time.Second}, e)
	first := r.change(t)
	r.call(t)

	// The echo service fails and the resolver is down, but the uplink is the same: keep the ID.
	e.set(func(e *env) { e.ipErr = errors.New("echo down") })
	e.events <- netif.Event{Kind: netif.AddressChanged}
	r.waiters(t, 2)
	r.clk.Advance(time.Second)
	r.call(t)
	r.noChange(t)
	if got := r.w.Current(); got.ID != first.ID || !got.Known {
		t.Errorf("ID not kept: %+v", got)
	}

	// A different gateway means a different network: the old ID must not stick. gw hash is used.
	e.set(func(e *env) { e.ifc.GatewayMAC = net.HardwareAddr{2, 0, 0, 0, 0, 9} })
	e.events <- netif.Event{Kind: netif.UplinkChanged}
	r.waiters(t, 2)
	r.clk.Advance(time.Second)
	if n := r.change(t); !strings.HasPrefix(string(n.ID), "gw:") {
		t.Errorf("after uplink change: %+v", n)
	}

	// No uplink at all → unknown.
	e.set(func(e *env) { e.upErr = netif.ErrNoUplink })
	e.events <- netif.Event{Kind: netif.LinkDown}
	r.waiters(t, 2)
	r.clk.Advance(time.Second)
	if n := r.change(t); n.Known {
		t.Errorf("still known without an uplink: %+v", n)
	}
}

func TestChangesLatestWins(t *testing.T) {
	w := New(Config{}, Deps{Uplink: func(context.Context) (netif.Interface, error) { return netif.Interface{}, nil }})
	w.publish(Network{ID: "asn:1", Known: true}, "k")
	w.publish(Network{ID: "asn:2", Known: true}, "k")
	if n := <-w.Changes(); n.ID != "asn:2" {
		t.Errorf("got %+v", n)
	}
}

func TestFetchIP(t *testing.T) {
	answer, status := "203.0.113.9\n", http.StatusOK
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(status)
		fmt.Fprint(w, answer)
	}))
	defer srv.Close()
	ctx := context.Background()
	ip, err := fetchIP(ctx, srv.Client(), srv.URL)
	if err != nil || ip != netip.MustParseAddr("203.0.113.9") {
		t.Errorf("got %v, %v", ip, err)
	}
	answer = "<html>nope</html>"
	if _, err := fetchIP(ctx, srv.Client(), srv.URL); err == nil {
		t.Error("garbage accepted")
	}
	answer, status = "1.2.3.4", http.StatusInternalServerError
	if _, err := fetchIP(ctx, srv.Client(), srv.URL); err == nil {
		t.Error("HTTP 500 accepted")
	}
	// Errors must not leak the endpoint URL.
	if _, err := fetchIP(ctx, srv.Client(), "http://127.0.0.1:1/x"); err == nil || strings.Contains(err.Error(), "127.0.0.1") {
		t.Errorf("err = %v", err)
	}
}

func TestEchoLookupWithoutEndpoints(t *testing.T) {
	f := echoLookup(nil, func(context.Context) (netif.Interface, error) { return netif.Interface{}, nil })
	if _, err := f(context.Background()); err == nil {
		t.Error("want error without endpoints")
	}
}

// winEnv mimics Windows: an uplink with a name and a gateway IP, but no gateway MAC and no SSID, and no
// public-IP lookup.
func winEnv() *env {
	e := newEnv()
	e.set(func(e *env) {
		e.ipErr = errors.New("no echo endpoints")
		e.ifc = netif.Interface{Name: "Ethernet", Index: 7, Gateway: netip.MustParseAddr("192.168.1.1")}
	})
	return e
}

func TestLocalFallback(t *testing.T) {
	r := start(t, Config{Salt: "s"}, winEnv())
	n := r.change(t)
	if !n.Known || !strings.HasPrefix(string(n.ID), "local:") || len(n.ID) != len("local:")+16 || n.ASN != 0 || !n.ID.Valid() {
		t.Fatalf("network = %+v", n)
	}
	for _, leak := range []string{"192.168", "192", "Ethernet"} {
		if strings.Contains(string(n.ID), leak) {
			t.Errorf("ID %q contains %q", n.ID, leak)
		}
	}
}

func TestLocalFallbackIsLastResort(t *testing.T) {
	// A gateway MAC present: gw: wins over local:.
	e := winEnv()
	e.set(func(e *env) { e.ifc.GatewayMAC = net.HardwareAddr{2, 0, 0, 0, 0, 1} })
	if n := start(t, Config{Salt: "s"}, e).change(t); !strings.HasPrefix(string(n.ID), "gw:") {
		t.Errorf("with a MAC: %q", n.ID)
	}
	// An SSID present: gw: too.
	e = winEnv()
	e.set(func(e *env) { e.ifc.SSID = "home" })
	if n := start(t, Config{Salt: "s"}, e).change(t); !strings.HasPrefix(string(n.ID), "gw:") {
		t.Errorf("with an SSID: %q", n.ID)
	}
	// The ASN path wins over both.
	e = winEnv()
	e.set(func(e *env) { e.ipErr = nil })
	if n := start(t, Config{Salt: "s"}, e).change(t); n.ID != "asn:12389" {
		t.Errorf("with an ASN: %q", n.ID)
	}
}

func TestLocalFallbackNeedsInterfaceAndGateway(t *testing.T) {
	for name, f := range map[string]func(*env){
		"no gateway": func(e *env) { e.ifc.Gateway = netip.Addr{} },
		"no name":    func(e *env) { e.ifc.Name = "" },
	} {
		e := winEnv()
		e.set(f)
		r := start(t, Config{Salt: "s"}, e)
		r.call(t)
		r.noChange(t)
		if n := r.w.Current(); n.Known || n.ID != "" {
			t.Errorf("%s: Current = %+v", name, n)
		}
	}
}

func TestLocalIDSaltAndGatewayChange(t *testing.T) {
	id := func(salt, gw string) string {
		e := winEnv()
		e.set(func(e *env) { e.ifc.Gateway = netip.MustParseAddr(gw) })
		return string(start(t, Config{Salt: salt}, e).change(t).ID)
	}
	a1, a2 := id("salt-a", "192.168.1.1"), id("salt-a", "192.168.1.1")
	if a1 != a2 {
		t.Errorf("same salt and facts gave %q and %q", a1, a2)
	}
	if b := id("salt-b", "192.168.1.1"); a1 == b {
		t.Errorf("different salts gave the same ID %q", a1)
	}
	if c := id("salt-a", "10.0.0.1"); a1 == c {
		t.Errorf("different gateways gave the same ID %q", a1)
	}
}

func TestLocalIDFollowsUplinkAndKeepsStable(t *testing.T) {
	e := winEnv()
	r := start(t, Config{Salt: "s", Debounce: time.Second, Refresh: time.Hour}, e)
	first := r.change(t)
	r.call(t)

	// Re-identification on the same uplink: same ID, no event.
	e.events <- netif.Event{Kind: netif.AddressChanged}
	r.waiters(t, 2)
	r.clk.Advance(time.Second)
	r.call(t)
	r.noChange(t)
	if got := r.w.Current(); got.ID != first.ID {
		t.Errorf("ID moved: %q → %q", first.ID, got.ID)
	}

	// A new gateway is a new network.
	e.set(func(e *env) { e.ifc.Gateway = netip.MustParseAddr("10.0.0.1") })
	e.events <- netif.Event{Kind: netif.UplinkChanged}
	r.waiters(t, 2)
	r.clk.Advance(time.Second)
	second := r.change(t)
	if !strings.HasPrefix(string(second.ID), "local:") || second.ID == first.ID {
		t.Errorf("after gateway change: %q", second.ID)
	}

	// A different adapter on the same gateway is a new network too.
	e.set(func(e *env) { e.ifc.Name = "Wi-Fi"; e.ifc.Index = 9 })
	e.events <- netif.Event{Kind: netif.UplinkChanged}
	r.waiters(t, 2)
	r.clk.Advance(time.Second)
	if third := r.change(t); third.ID == second.ID || third.ID == first.ID {
		t.Errorf("after adapter change: %q", third.ID)
	}
}

var _ analyzer.Resolver = (*cymru)(nil)
