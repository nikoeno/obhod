package pc

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nikoejento/obhod/internal/lib/netif"
	"github.com/nikoejento/obhod/internal/lib/route"
	"github.com/nikoejento/obhod/internal/pc/desync"
)

// syncBuffer is a log sink that tests can read while the app writes.
type syncBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

func withLog(buf *syncBuffer) Option {
	return WithLogger(slog.New(slog.NewTextHandler(buf, &slog.HandlerOptions{Level: slog.LevelDebug})))
}

func TestBusyStatusPortFailsBeforeTheTunnel(t *testing.T) {
	h := newHarness(t, baseTable(t))
	h.status.listenErr = errors.New("listen tcp 127.0.0.1:9091: bind: address already in use")
	err := h.app.Run(context.Background())
	if exitCode(err) != exitConfig || !strings.Contains(err.Error(), "status.listen") {
		t.Fatalf("err = %v, code %d", err, exitCode(err))
	}
	if h.log.index("start sing-box") >= 0 || h.log.index("start winws2") >= 0 {
		t.Fatalf("tunnel started although the status port was busy: %s", h.ops())
	}
	if h.log.index("checker.stop") < 0 {
		t.Fatalf("not a clean shutdown: %s", h.ops())
	}
}

func TestSingBoxRestartFailureRollsBackConfig(t *testing.T) {
	buf := &syncBuffer{}
	h := newHarness(t, baseTable(t), withLog(buf))
	h.start()
	h.waitOp("status.start")
	h.fc.Advance(time.Minute)
	before := h.singBoxFile()
	h.log.clear()
	h.procs.mu.Lock()
	h.procs.restartErr[nameSingBox] = []error{errors.New("exit status 1"), nil}
	h.procs.logs[nameSingBox] = []string{"FATAL[0000] start service: bad thing"}
	h.procs.mu.Unlock()

	h.publishAndFire(tbl(t, ent("yt.example", route.Desync), ent("blocked.example", route.Direct), ent("plain.example", route.Direct)))
	h.waitCount("restart sing-box", 1)
	if h.log.index("restart-failed sing-box") < 0 {
		t.Fatalf("ops: %s", h.ops())
	}
	waitUntil(t, "rollback logged", func() bool { return strings.Contains(buf.String(), "previous config") })
	if !bytes.Equal(before, h.singBoxFile()) {
		t.Fatal("the failed config was left in place")
	}
	if _, err := os.Stat(filepath.Join(h.p.Run, singBoxFileName+".old")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("backup left behind: %v", err)
	}
	if h.app.State() != StateConnected {
		t.Fatalf("state = %s", h.app.State())
	}
	if !strings.Contains(buf.String(), "bad thing") {
		t.Fatal("the output of the failed sing-box was not logged")
	}
}

func TestSingBoxLostEndsTerminalRun(t *testing.T) {
	h := newHarness(t, baseTable(t))
	done := make(chan error, 1)
	go func() { done <- h.app.Run(context.Background()) }()
	h.waitOp("status.start")
	h.fc.Advance(time.Minute)
	h.procs.mu.Lock()
	h.procs.restartErr[nameSingBox] = []error{errors.New("bad config"), errors.New("old one fails too")}
	h.procs.mu.Unlock()

	h.publishAndFire(tbl(t, ent("yt.example", route.Desync), ent("blocked.example", route.Direct)))
	select {
	case err := <-done:
		if exitCode(err) != exitRuntime || !strings.Contains(err.Error(), "sing-box failed") {
			t.Fatalf("err = %v, code %d", err, exitCode(err))
		}
	case <-time.After(10 * time.Second):
		t.Fatalf("Run did not end after sing-box was lost: %s", h.ops())
	}
	if h.log.index("stop winws2") < 0 {
		t.Fatalf("engine left running: %s", h.ops())
	}
}

func TestSingBoxGiveUpDisconnectsInUIMode(t *testing.T) {
	h := newHarness(t, baseTable(t), withUI(true))
	h.start()
	h.waitOp("status.start")
	if err := h.app.Connect(context.Background()); err != nil {
		t.Fatal(err)
	}
	h.procs.crash(nameSingBox)
	waitUntil(t, "disconnected", func() bool { return h.app.State() == StateDisconnected })
	if h.log.index("stop winws2") < 0 {
		t.Fatalf("engine left running: %s", h.ops())
	}
	snap := (&statusSource{a: h.app}).Snapshot()
	if snap.State != string(StateDisconnected) {
		t.Fatalf("status page state = %q", snap.State)
	}
}

func TestEngineGiveUpIsRetriedOnNextReload(t *testing.T) {
	h := newHarness(t, baseTable(t))
	h.start()
	h.waitOp("status.start")
	h.fc.Advance(time.Minute)
	h.procs.crash("winws2")
	waitUntil(t, "engine forgotten", func() bool {
		h.app.mu.Lock()
		defer h.app.mu.Unlock()
		return !h.app.engine.running
	})
	h.log.clear()
	h.publishAndFire(tbl(t, ent("yt.example", route.Desync), ent("blocked.example", route.Direct)))
	h.waitOp("start winws2")
}

func TestTUNAsUplinkNeverDrivesTheEngine(t *testing.T) {
	// At connect: refused.
	h := newHarness(t, baseTable(t))
	h.uplink = netif.Interface{Name: "splitter", Index: 40}
	err := h.app.Run(context.Background())
	if err == nil || !strings.Contains(err.Error(), "virtual") {
		t.Fatalf("err = %v", err)
	}
	if h.log.index("start winws2") >= 0 {
		t.Fatalf("engine started on the TUN: %s", h.ops())
	}

	// On reload: the engine keeps its interface, sing-box is still updated.
	h = newHarness(t, baseTable(t))
	h.start()
	h.waitOp("status.start")
	h.fc.Advance(time.Minute)
	h.log.clear()
	h.mu.Lock()
	h.uplink = netif.Interface{Name: "splitter", Index: 40}
	h.mu.Unlock()
	h.publishAndFire(tbl(t, ent("yt.example", route.Desync), ent("vid.example", route.Desync), ent("blocked.example", route.Direct)))
	h.waitOp("restart sing-box")
	if h.log.index("restart winws2") >= 0 {
		t.Fatalf("engine restarted on the TUN: %s", h.ops())
	}
	eng, _ := h.procs.spec("winws2")
	if strings.Contains(strings.Join(eng.Args, " "), "ifIdx == 40") {
		t.Fatalf("engine bound to the TUN: %q", eng.Args)
	}
}

func TestNFQWSUplinkChangeNeedsNewRules(t *testing.T) {
	buf := &syncBuffer{}
	h := newHarness(t, baseTable(t), withLog(buf))
	h.app.cfg.Desync.Engine = desync.NFQWS
	h.nftList = "table inet splitter {\n ip daddr 203.0.113.7 return\n oifname \"eth0\" tcp dport { 80, 443 } queue\n}"
	h.start()
	h.waitOp("start nfqws2")
	h.waitOp("status.start")
	h.fc.Advance(time.Minute)
	h.log.clear()

	h.mu.Lock()
	h.uplink = netif.Interface{Name: "wlan0", Index: 8}
	h.mu.Unlock()
	h.netEv <- netif.Event{Kind: netif.UplinkChanged}
	waitUntil(t, "debounce timer", func() bool { return h.fc.Waiters() >= 1 })
	h.fc.Advance(reloadDebounce)
	waitUntil(t, "warning", func() bool { return strings.Contains(buf.String(), `oifname \"wlan0\"`) })
	if h.log.index("restart nfqws2") >= 0 {
		t.Fatalf("engine restarted although the nft rules still name eth0: %s", h.ops())
	}

	// The human re-applies the rules; the next reload restarts the engine.
	h.mu.Lock()
	h.nftList = strings.ReplaceAll(h.nftList, "eth0", "wlan0")
	h.mu.Unlock()
	h.netEv <- netif.Event{Kind: netif.AddressChanged}
	waitUntil(t, "debounce timer", func() bool { return h.fc.Waiters() >= 1 })
	h.fc.Advance(reloadDebounce)
	h.waitOp("restart nfqws2")
}

func TestStaleNFTTableRefusesConnect(t *testing.T) {
	h := newHarness(t, baseTable(t))
	h.app.cfg.Desync.Engine = desync.NFQWS
	h.nftList = "table inet splitter {\n ip daddr 198.51.100.1 return\n oifname \"eth9\" queue\n}"
	err := h.app.Run(context.Background())
	if exitCode(err) != exitManual || !strings.Contains(err.Error(), "another plan") || !strings.Contains(err.Error(), "nft flush table inet splitter") {
		t.Fatalf("err = %v, code %d", err, exitCode(err))
	}
}

func TestHostNameTunnelIsResolvedAndExcluded(t *testing.T) {
	h := newHarness(t, baseTable(t))
	h.app.outbounds.Tunnel = json.RawMessage(`{"type":"vless","server":"vpn.example.org","server_port":443}`)
	h.lookup = func(host string) ([]netip.Addr, error) {
		if host != "vpn.example.org" {
			return nil, errors.New("unexpected host")
		}
		return []netip.Addr{netip.MustParseAddr("198.51.100.77"), netip.MustParseAddr("2001:db8::77")}, nil
	}
	h.start()
	h.waitOp("start winws2")
	eng, _ := h.procs.spec("winws2")
	args := strings.Join(eng.Args, " ")
	if !strings.Contains(args, "ip.DstAddr == 198.51.100.77") || !strings.Contains(args, "ipv6.DstAddr == 2001:db8::77") {
		t.Fatalf("resolved tunnel addresses not excluded: %q", eng.Args)
	}
}

func TestHostNameTunnelWithoutDesyncNeedsNoExclude(t *testing.T) {
	h := newHarness(t, tbl(t, ent("a.example", route.Tunnel), ent("b.example", route.Direct)))
	h.app.outbounds.Tunnel = json.RawMessage(`{"type":"vless","server":"vpn.example.org","server_port":443}`)
	h.start()
	h.waitOp("start sing-box")
}

func TestDisconnectGivesUpOnBusyLock(t *testing.T) {
	h := newHarness(t, baseTable(t))
	if err := h.app.op.Lock(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer h.app.op.Unlock()
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	if err := h.app.Disconnect(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err = %v", err)
	}
}

func TestUIConnectEndsWithRun(t *testing.T) {
	h := newHarness(t, baseTable(t), withUI(true))
	block := make(chan struct{})
	h.d.uplink = func(ctx context.Context) (netif.Interface, error) {
		select {
		case <-block:
		case <-ctx.Done():
			return netif.Interface{}, ctx.Err()
		}
		return h.uplink, nil
	}
	stop := h.start()
	h.waitOp("status.start")
	connected := make(chan error, 1)
	go func() { connected <- h.app.Connect(context.Background()) }() // the UI's context never ends
	waitUntil(t, "connecting", func() bool { return h.app.State() == StateConnecting })
	stopped := make(chan error, 1)
	go func() { stopped <- stop() }()
	select {
	case <-connected:
	case <-time.After(5 * time.Second):
		close(block)
		t.Fatal("Connect outlived Run")
	}
	<-stopped
}

func TestLogNeverHoldsSecrets(t *testing.T) {
	buf := &syncBuffer{}
	h := newHarness(t, baseTable(t), withLog(buf))
	h.app.outbounds.Tunnel = json.RawMessage(`{"type":"vless","server":"203.0.113.7","server_port":443,"uuid":"11111111-2222-3333-4444-555555555555"}`)
	h.start()
	h.waitOp("status.start")
	h.fc.Advance(time.Minute)
	h.publishAndFire(tbl(t, ent("yt.example", route.Desync), ent("blocked.example", route.Direct)))
	h.waitOp("restart sing-box")
	log := buf.String()
	for _, secret := range []string{"test-secret", "11111111-2222-3333-4444-555555555555"} {
		if strings.Contains(log, secret) {
			t.Fatalf("log contains %q:\n%s", secret, log)
		}
	}
}

func TestNFTMissing(t *testing.T) {
	setup := []string{
		"nft add table inet splitter",
		"nft 'add rule inet splitter postrouting ip daddr { 203.0.113.7/32, 192.0.2.0/24 } return'",
		"nft 'add rule inet splitter postrouting ip6 daddr { 2001:db8::7/128 } return'",
		"nft 'add rule inet splitter postrouting tcp sport { 50000-50099 } return'",
		`nft 'add rule inet splitter postrouting oifname "eth0" tcp dport { 80, 443 } queue num 200 bypass'`,
	}
	full := "ip daddr { 192.0.2.0/24, 203.0.113.7 } return\nip6 daddr 2001:db8::7 return\ntcp sport 50000-50099 return\noifname \"eth0\" tcp dport { 80, 443 }"
	if m := nftMissing(full, setup); len(m) != 0 {
		t.Fatalf("missing = %v", m)
	}
	// A longer address that contains the wanted one is not a match.
	if m := nftMissing(strings.Replace(full, "203.0.113.7 ", "203.0.113.70 ", 1), setup); len(m) != 1 || m[0] != "203.0.113.7" {
		t.Fatalf("prefix match: missing = %v", m)
	}
	stale := strings.NewReplacer("eth0", "wlan0", "203.0.113.7", "203.0.113.8").Replace(full)
	if m := strings.Join(nftMissing(stale, setup), " "); !strings.Contains(m, `oifname "eth0"`) || !strings.Contains(m, "203.0.113.7") {
		t.Fatalf("missing = %v", m)
	}
}

func TestRotatorRecoversFromFailedReopen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "app.log")
	r, err := newRotator(path, 1<<20, 2)
	if err != nil {
		t.Fatal(err)
	}
	_ = r.f.Close()
	r.f = nil // as after a rotation whose reopen failed
	if _, err := r.Write([]byte("back\n")); err != nil {
		t.Fatalf("write after a failed reopen: %v", err)
	}
	if err := r.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Write([]byte("x")); !errors.Is(err, os.ErrClosed) {
		t.Fatalf("write after Close: %v", err)
	}
	if b, _ := os.ReadFile(path); string(b) != "back\n" {
		t.Fatalf("file = %q", b)
	}
}

func TestValidateCatchesChildFieldsAtLoad(t *testing.T) {
	for name, mut := range map[string]func(*Config){
		"clash api not loopback": func(c *Config) { c.SingBox.ClashAPI = "0.0.0.0:9090" },
		"clash api bad port":     func(c *Config) { c.SingBox.ClashAPI = "127.0.0.1:x" },
		"fakeip ipv6":            func(c *Config) { c.SingBox.DNS.FakeIPRange = "fc00::/18" },
		"tun address":            func(c *Config) { c.SingBox.TUN.Address = "nope" },
		"doh host name":          func(c *Config) { c.SingBox.DNS.DirectDoH = "https://dns.example/dns-query" },
		"strategy override":      func(c *Config) { c.Desync.Strategy = []string{"--wf-raw-filter=true"} },
		"port 0":                 func(c *Config) { c.Desync.Ports = []uint16{0} },
		"exclude range":          func(c *Config) { c.Desync.ExcludePorts = []desync.PortRange{{9, 1}} },
		"checker order":          func(c *Config) { c.Checker.Decide.Order = []route.Route{route.Block} },
	} {
		c := DefaultConfig()
		mut(&c)
		if err := c.Validate(); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	if err := DefaultConfig().Validate(); err != nil {
		t.Fatalf("default config: %v", err)
	}
}
