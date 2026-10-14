//go:build linux && lab

package lab

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/netip"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/miekg/dns"

	"github.com/nikoejento/obhod/internal/lib/faultserver"
	"github.com/nikoejento/obhod/internal/lib/route"
	"github.com/nikoejento/obhod/lab/fakedns"
	"github.com/nikoejento/obhod/lab/fakedpi"
	"github.com/nikoejento/obhod/lab/netns"
)

// routingAppYAML is merged over the lab defaults for TestRouting.
//
//   - The checker never changes the seeded table during the test: a 1 h schedule and a confirmation
//     count no run can reach (the probes of a desync domain are mangled by the engine too, so they
//     would otherwise pull it to direct, see PLAN.md L-D5). The seeded decisions agree with the
//     fakedpi rules anyway.
//   - The desync strategy splits the ClientHello in the middle of the second-level label of the
//     host name (zapret2 multisplit, position marker midsld, which needs the TLS payload type that
//     --payload=tls_client_hello selects). fakedpi's NaiveSNI reads only a hello that is complete in
//     one segment, so the split hello passes. The app owns --qnum, --hostlist, --lua-init and the
//     nft rules; desync.Config.Check rejects those in a strategy.
func routingAppYAML(debugLog string) string {
	return fmt.Sprintf(`
checker:
  schedule: {interval: 1h, jitter: 1m, mingap: 30m}
  decide: {confirmations: 50}
desync:
  strategy:
    - "--debug=@%s"
    - "--filter-l7=tls"
    - "--payload=tls_client_hello"
    - "--lua-desync=multisplit:pos=midsld"
`, debugLog)
}

// Finding F4: nfqws2 resolves --hostlist to an absolute path and, after its default privilege drop
// (uid 2147483647), needs search permission on every ancestor directory, so the app keeps nfqws2 as
// root with --uid=0:0 (set by the nfqws renderer; a strategy cannot override it). The F4 check below
// asserts that identity and the owner-only hostlist.

// routeCase is one row of the routing proof (PLAN.md §0).
type routeCase struct {
	host string
	tag  string // the route name = the outbound tag the Clash API shows
	from string // who the site sees: "client", "vps"; "" when nothing may arrive
	dpi  string // "proxy": no flow to the site, the SNI only on the flow to the VPS SOCKS port; "plain": the TSPU saw the SNI; "split": and the hello was split; "none": it saw neither the SNI nor the site address
	note string
}

// TestRouting is L8: `splitter run` connected (sing-box TUN, nfqws2) in the client namespace routes
// every domain the way its table says. The proof is what the fake endpoints saw: the site's
// connection log (who connected), the fake TSPU's flow log, the Clash API of the real sing-box, and
// the DoH and resolver query logs. The root namespace must be unchanged afterwards.
func TestRouting(t *testing.T) {
	if err := Preflight(); err != nil {
		t.Skip(err)
	}
	nfq, err := thirdParty("nfqws2")
	if err != nil {
		t.Skipf("the pinned nfqws2 is missing (make fetch): %v", err)
	}
	for _, f := range []string{"zapret-lib.lua", "zapret-antidpi.lua"} {
		if _, err := os.Stat(filepath.Join(filepath.Dir(nfq), "lua", f)); err != nil {
			t.Skipf("the zapret2 Lua library is missing next to nfqws2 (make fetch): %v", err)
		}
	}
	before := rootSnapshot(t)
	t.Cleanup(func() { before.compare(t, rootSnapshot(t)) }) // registered first: runs after the lab's teardown

	l := Up(t)
	const isp = "ispa"
	ctx := context.Background()

	// --- sites, rules, seed -----------------------------------------------------------------

	ok := faultserver.Spec{Behavior: faultserver.OK, Bytes: 1024}
	// A body big enough that a rate-limited curl keeps the connection open for ~2.5 s: the Clash API
	// only lists live connections.
	big := faultserver.Spec{Behavior: faultserver.OK, Bytes: 1 << 20}
	for _, n := range []string{"control-a.test", "control-b.test", "canary.test", "neutral.test"} {
		l.AddSite(n, ok)
	}
	cases := []routeCase{
		{host: "direct-a.test", tag: "direct", from: "client", dpi: "plain"},
		{host: "desync-a.test", tag: "desync", from: "client", dpi: "split", note: "NaiveSNI on ispa resets it without the engine"},
		{host: "tunnel-a.test", tag: "tunnel", from: "vps", dpi: "none", note: "ispa drops SYNs to its address (IPBlock)"},
		{host: "proxy-a.test", tag: "proxy", from: "vps", dpi: "proxy", note: "proxy route is unencrypted SOCKS: the SNI is visible to the ISP on the way to the proxy (design)"},
		{host: "block-a.test", tag: "block", from: "", dpi: "none"},
		{host: "tunnel-p.test", tag: "tunnel", from: "vps", dpi: "none", note: "parent"},
		{host: "direct.tunnel-p.test", tag: "direct", from: "client", dpi: "plain", note: "direct child of a tunnel parent"},
		{host: "desync-p.test", tag: "desync", from: "client", dpi: "split", note: "parent"},
		{host: "direct.desync-p.test", tag: "direct", from: "client", dpi: "accepted", note: "direct child of a desync parent: sing-box says direct, the engine's hostlist still matches the subdomain (singbox/API.md)"},
		{host: "block-p.test", tag: "block", from: "", dpi: "none", note: "parent"},
		{host: "direct.block-p.test", tag: "direct", from: "client", dpi: "plain", note: "direct child of a block parent"},
	}
	for _, c := range cases {
		l.AddSite(c.host, big)
	}
	siteIP := func(host string) netip.Addr { return l.Site(host).Addr.Addr() }

	decisions := map[string]route.Route{
		"direct-a.test": route.Direct, "desync-a.test": route.Desync, "tunnel-a.test": route.Tunnel,
		"tunnel-p.test": route.Tunnel, "direct.tunnel-p.test": route.Direct,
		"desync-p.test": route.Desync, "direct.desync-p.test": route.Direct,
		"direct.block-p.test": route.Direct,
	}
	static := map[string]route.Route{"proxy-a.test": route.Proxy, "block-a.test": route.Block, "block-p.test": route.Block}
	wantRoutes := map[route.Route]int{route.Direct: 4, route.Desync: 2, route.Tunnel: 2, route.Proxy: 1, route.Block: 2}

	// ispa's TSPU: the canary (a realistic TSPU, calibration reports dpi_present), the naive-SNI
	// rule on the desync domains (and, by suffix, their children), and the two tunnel domains by
	// address, so every probe agrees with the seeded route.
	l.SetRules(isp,
		fakedpi.Rule{Kind: fakedpi.NaiveSNI, Domains: []string{"canary.test"}},
		fakedpi.Rule{Kind: fakedpi.NaiveSNI, Domains: []string{"desync-a.test", "desync-p.test"}},
		fakedpi.Rule{Kind: fakedpi.IPBlock, Prefix: netip.PrefixFrom(siteIP("tunnel-a.test"), 32)},
		fakedpi.Rule{Kind: fakedpi.IPBlock, Prefix: netip.PrefixFrom(siteIP("tunnel-p.test"), 32)},
	)

	// --- helpers ---------------------------------------------------------------------------

	check := func(name string, fn func() error) bool {
		t.Helper()
		err := fn()
		note := "ok"
		if err != nil {
			note = err.Error()
			t.Errorf("%s: %v", name, err)
		}
		l.Report("routing: "+name, err == nil, note)
		return err == nil
	}
	mustPass := func(name string, fn func() error) {
		t.Helper()
		if !check(name, fn) {
			t.FailNow()
		}
	}
	curlTLS := func(args ...string) ([]byte, error) {
		// X25519 only: a post-quantum key share spans two segments and NaiveSNI by design never reads
		// such a hello, so the "desync is required" precondition would not hold.
		return l.Curl(ctx, append([]string{"--curves", "X25519", "-o", "/dev/null"}, args...)...)
	}
	isReset := func(err error) bool { return err != nil && strings.Contains(strings.ToLower(err.Error()), "reset") }
	newConns := func(host string, n int) []faultserver.Conn { return l.Site(host).Server.Conns()[n:] }

	// QUIC: a UDP listener on a site address in inet, and a datagram sender in the client namespace.
	quicAddr := net.JoinHostPort(siteIP("direct-a.test").String(), "443")
	var (
		qmu  sync.Mutex
		qgot []netip.AddrPort
	)
	var pc net.PacketConn
	if err := l.inet.Do(func() error {
		var err error
		pc, err = net.ListenPacket("udp", quicAddr)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { pc.Close() })
	go func() {
		buf := make([]byte, 2048)
		for {
			_, from, err := pc.ReadFrom(buf)
			if err != nil {
				return
			}
			if ap, err := netip.ParseAddrPort(from.String()); err == nil {
				qmu.Lock()
				qgot = append(qgot, ap)
				qmu.Unlock()
			}
		}
	}()
	quicSeen := func() int { qmu.Lock(); defer qmu.Unlock(); return len(qgot) }
	sendUDP := func(dst string) error {
		var c net.Conn
		if err := l.cli.Do(func() error {
			var err error
			c, err = net.DialTimeout("udp", dst, 2*time.Second)
			return err
		}); err != nil {
			return err
		}
		defer c.Close()
		_, err := c.Write([]byte("lab-quic-probe"))
		return err
	}
	udpFlows := func(from int) []fakedpi.Flow {
		var out []fakedpi.Flow
		for _, f := range l.DPI(isp).Seen()[from:] {
			if f.Proto == "udp" && f.Dst.Port() == 443 {
				out = append(out, f)
			}
		}
		return out
	}

	// --- 0. preconditions, before the app runs -----------------------------------------------

	mustPass("precondition: direct works through ispa", func() error {
		n := len(l.Site("direct-a.test").Server.Conns())
		if _, err := curlTLS("https://direct-a.test/"); err != nil {
			return err
		}
		if cs := newConns("direct-a.test", n); len(cs) != 1 || cs[0].Remote.Addr() != l.ClientAddr(isp) || cs[0].SNI != "direct-a.test" {
			return fmt.Errorf("site saw %+v, want one connection from %v", cs, l.ClientAddr(isp))
		}
		return nil
	})
	mustPass("precondition: without the app the naive-SNI rule resets the desync domains", func() error {
		for _, h := range []string{"desync-a.test", "desync-p.test", "direct.desync-p.test"} {
			if _, err := curlTLS("https://" + h + "/"); !isReset(err) {
				return fmt.Errorf("%s: want a connection reset, got %v", h, err)
			}
		}
		return nil
	})
	mustPass("precondition: without the app the tunnel domains are unreachable directly", func() error {
		for _, h := range []string{"tunnel-a.test", "tunnel-p.test"} {
			if _, err := curlTLS("--connect-timeout", "3", "https://"+h+"/"); err == nil {
				return fmt.Errorf("%s: reachable directly, the IPBlock rule does not hold", h)
			}
		}
		return nil
	})
	udpBase := len(l.DPI(isp).Seen())
	mustPass("precondition: a UDP/443 datagram reaches inet without the app", func() error {
		if err := sendUDP(quicAddr); err != nil {
			return err
		}
		ctx2, cancel := context.WithTimeout(ctx, 5*time.Second)
		defer cancel()
		if err := poll(ctx2, func() error {
			if quicSeen() == 0 {
				return fmt.Errorf("no datagram yet")
			}
			return nil
		}); err != nil {
			return err
		}
		qmu.Lock()
		from := qgot[0].Addr()
		qmu.Unlock()
		if from != l.ClientAddr(isp) {
			return fmt.Errorf("datagram came from %v, want %v", from, l.ClientAddr(isp))
		}
		if len(udpFlows(udpBase)) == 0 {
			return fmt.Errorf("fakedpi did not see the UDP/443 flow, so it cannot prove its absence later")
		}
		return nil
	})

	// --- 1. one splitter, connected -----------------------------------------------------------
	//
	// Finding F3 (app bug, reported separately): internal/pc/app.go Run takes the checker's DEFAULT
	// table (network not identified yet, so no desync domains) and connects; only afterwards does
	// reloadLoop call checker.Subscribe, so the table change published when the network is identified
	// during Connect is lost. The contract's exit 2 at start (internal/pc/API.md startup step 3) is
	// therefore unreachable, and the seeded table is not applied until something else triggers a
	// reload. The test records F3 and forces the reload with a link bounce (the uplink watcher arms
	// the debounce).

	sb, _ := thirdParty("sing-box")
	// nfqws2 writes its own debug log here (--debug=@file in the strategy); it is copied to the run dir.
	// nfqws2 drops root before it opens the file, so the log lives in a directory every uid can reach
	// (explicit chmod, umask-proof; its parent is the world-writable temp dir) with a pre-created
	// 0666 file. A root-only directory such as t.TempDir made nfqws2 exit 1.
	nfqDir, err := os.MkdirTemp("", "obl-nfqws2-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(nfqDir) }) // registered first: runs after the copy below
	if err := os.Chmod(nfqDir, 0o777); err != nil {
		t.Fatal(err)
	}
	nfqDebug := filepath.Join(nfqDir, "nfqws2-debug.log")
	if err := os.WriteFile(nfqDebug, nil, 0o666); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(nfqDebug, 0o666); err != nil {
		t.Fatal(err)
	}
	opts := SplitterOptions{Args: []string{"run"}, AppYAML: routingAppYAML(nfqDebug), Decisions: decisions, Static: static}
	if out, _, err := l.Exec(ctx, "nft", "list", "table", "inet", "splitter"); err == nil {
		t.Fatalf("the table inet splitter exists in the client namespace before the app ran: %s", out)
	}
	s := l.RunSplitter(ctx, opts)
	nfqDebugTail := func(n int) string {
		b, err := os.ReadFile(nfqDebug)
		if err != nil {
			return "(no nfqws2 debug log: " + err.Error() + ")"
		}
		return string(tail(b, n))
	}
	t.Cleanup(func() {
		if b, err := os.ReadFile(nfqDebug); err == nil {
			_ = os.WriteFile(filepath.Join(l.logDir, "nfqws2-debug.log"), b, 0o644)
		}
	})
	statusNow := func() (labStatus, error) {
		var st labStatus
		b, err := s.Status(ctx)
		if err != nil {
			return st, err
		}
		return st, json.Unmarshal(b, &st)
	}
	var initial labStatus
	var baseRecent []labChange // route changes of the first seeded table, before the end-of-run comparison
	mustPass("connected: splitter run reaches connected (no nft table yet)", func() error {
		wctx, cancel := context.WithTimeout(ctx, 150*time.Second)
		defer cancel()
		err := poll(wctx, func() error {
			if s.exited() {
				return &stopError{fmt.Errorf("splitter exited (code %d); output:\n%s", s.code, tail([]byte(s.output()), 4096))}
			}
			st, err := statusNow()
			if err != nil {
				return err
			}
			if st.State != "connected" {
				return fmt.Errorf("state %q", st.State)
			}
			initial = st
			return nil
		})
		if err != nil {
			return fmt.Errorf("%v\n%s", err, s.Diagnostics(ctx, "not connected"))
		}
		return nil
	})
	check("table as seeded (both network IDs): route counts in the status", func() error {
		// The status may still show the defaults for a moment after the network is identified.
		wctx, cancel := context.WithTimeout(ctx, 30*time.Second)
		defer cancel()
		err := poll(wctx, func() error {
			st, err := statusNow()
			if err != nil {
				return err
			}
			initial = st
			baseRecent = st.Recent
			if !mapsEqual(st.Routes, wantRoutes) {
				return fmt.Errorf("status routes %v, want %v", st.Routes, wantRoutes)
			}
			return nil
		})
		if err != nil {
			return fmt.Errorf("%v (a mismatch means the seeded decisions were not found under the network ID the app uses)", err)
		}
		return nil
	})
	logCount := func(sub string) int { return strings.Count(s.Log(), sub) }
	const (
		reloadMarkEngine  = "reload: desync engine not (re)started"
		reloadMarkSingBox = "sing-box restarted with a new config"
	)
	engineProcs := func() []procInfo { return procsOf(nfq, s.PID()) }
	check("F3: the table published during Connect is applied (a reload runs without outside help)", func() error {
		time.Sleep(8 * time.Second) // a negative (debounce 3 s + margin): nothing to poll for
		for _, m := range []string{reloadMarkSingBox, reloadMarkEngine, "desync engine"} {
			if logCount(m) > 0 {
				return nil
			}
		}
		return fmt.Errorf("F3: the status shows the seeded routes (%v) but the app log has no reload 8 s later, so sing-box still runs the default table. "+
			"internal/pc/app.go Run takes checker.Table() and connects before reloadLoop calls checker.Subscribe (reload.go), so the change published when the network was identified is lost; "+
			"the contract's exit 2 at start (internal/pc/API.md startup step 3) cannot happen. The test forces a reload with a link bounce", initial.Routes)
	})

	// bounce takes the client's active link down and up (the pc uplink watcher polls every 2 s and
	// arms the 3 s reload debounce); UseISP restores the link, the default route and the resolver.
	bounce := func() {
		l.must(l.topo.SetLink(l.cli, "eth0", false))
		time.Sleep(3500 * time.Millisecond) // longer than the watcher's poll, so the down event is seen
		l.UseISP(isp)
	}
	// waitLog polls the app log for more than n occurrences of sub.
	waitLog := func(sub string, n int, within time.Duration) error {
		wctx, cancel := context.WithTimeout(ctx, within)
		defer cancel()
		return poll(wctx, func() error {
			if s.exited() {
				return &stopError{fmt.Errorf("splitter exited (code %d)", s.code)}
			}
			if logCount(sub) <= n {
				return fmt.Errorf("no new %q in the app log", sub)
			}
			return nil
		})
	}

	// --- 2. forced reload: the app logs the nft commands, the test applies them --------------

	var setup [][]string
	var setupMsg string // the app's logged message with the Setup commands
	mustPass("reload without the nft table: the engine is not started and the app logs the Setup commands", func() error {
		n := logCount(reloadMarkEngine)
		bounce()
		if err := waitLog(reloadMarkEngine, n, 60*time.Second); err != nil {
			return fmt.Errorf("%v\n%s", err, s.Diagnostics(ctx, "no reload after the link bounce"))
		}
		msg, err := logErrValue(s.Log(), reloadMarkEngine)
		if err != nil {
			return err
		}
		cmds, err := parseNFTSetup(msg)
		if err != nil {
			return fmt.Errorf("%v; message:\n%s", err, msg)
		}
		setup, setupMsg = cmds, msg
		for _, want := range []string{`"inet splitter"`, `oifname "eth0"`, "queue num 200"} {
			if !strings.Contains(msg, want) {
				return fmt.Errorf("the message lacks %s:\n%s", want, msg)
			}
		}
		if len(engineProcs()) != 0 {
			return fmt.Errorf("nfqws2 runs although the nft table does not exist")
		}
		// The tunnel server is an IP literal in tunnel.json, so the app excludes it by itself
		// (desync.exclude_ips stays empty).
		raw, err := os.ReadFile(filepath.Join(s.root, "secrets", "tunnel.json"))
		if err != nil {
			return err
		}
		var tj struct{ Server string }
		if err := json.Unmarshal(raw, &tj); err != nil {
			return err
		}
		if a, err := netip.ParseAddr(tj.Server); err != nil || a != l.VPSAddr() {
			return fmt.Errorf("tunnel.json server %q is not the VPS address literal %v", tj.Server, l.VPSAddr())
		}
		if !strings.Contains(strings.Join(flatten(cmds), " "), l.VPSAddr().String()+"/32") {
			return fmt.Errorf("the printed rules do not exclude the tunnel server %v: %q", l.VPSAddr(), cmds)
		}
		return nil
	})
	l.Report("routing: exit 2 at start (internal/pc/API.md startup step 3)", true,
		"UNREACHABLE: the first table the app takes has no desync domains (F3), so the exit-2 path never runs at start; the missing table only shows up as a logged reload error")
	mustPass("the logged commands apply in the client namespace", func() error {
		for _, argv := range setup {
			actx, cancel := context.WithTimeout(ctx, 30*time.Second)
			_, _, err := l.Exec(actx, argv...) // only text parseNFTSetup accepted: nft add/flush on inet splitter, or the exact ip rule exemption
			cancel()
			if err != nil {
				return fmt.Errorf("%q: %w", argv, err)
			}
		}
		out, _, err := l.Exec(ctx, "nft", "list", "table", "inet", "splitter")
		if err != nil {
			return err
		}
		if !nftQueue200.MatchString(string(out)) {
			return fmt.Errorf("the kernel listing has no queue 200 rule: %s", out)
		}
		for _, want := range []string{`oifname "eth0"`, "203.0.113.250"} {
			if !strings.Contains(string(out), want) {
				return fmt.Errorf("the kernel listing lacks %s:\n%s", want, out)
			}
		}
		return nil
	})
	check("F5: the printed Setup exempts the engine's fwmark from the TUN policy routing", func() error {
		// sing-box auto_route installs "9003: not from all iif lo lookup 2022" (table 2022 routes via
		// the TUN), so every locally generated, device-unbound packet enters the TUN, including the
		// raw packets nfqws2 sends with fwmark 0x40000000. The printed Setup carries the exemption
		// (parseNFTSetup accepted it in its exact form); it must have a priority below 9000, and
		// after the commands were applied above the kernel must list it.
		found := false
		for _, argv := range setup {
			if isEngineMarkRule(argv) {
				if p, _ := strconv.Atoi(argv[8]); p >= 9000 {
					return fmt.Errorf("F5: the printed exemption has priority %d, not below sing-box's 9000: %q", p, argv)
				}
				found = true
			}
		}
		if !found {
			return fmt.Errorf("F5: the printed Setup has no `ip rule add fwmark 0x40000000/0x40000000 lookup main priority <9000` exemption, so nfqws2's raw packets follow sing-box's rule 9003 into the TUN and never leave eth0. printed message:%s%s", "\n", setupMsg)
		}
		out, _, err := l.Exec(ctx, "ip", "rule", "show")
		if err != nil {
			return err
		}
		if !regexp.MustCompile(`(?m)^[0-9]+:.*fwmark 0x40000000/0x40000000 lookup main`).Match(out) {
			return fmt.Errorf("F5: the exemption was applied but `ip rule show` lacks it:%s%s", "\n", out)
		}
		return nil
	})
	mustPass("second reload: the app accepts the kernel listing and starts nfqws2; sing-box runs the table config", func() error {
		bounce()
		// sing-box restarts at most every 30 s and only when its config changed, so allow a long wait.
		wctx, cancel := context.WithTimeout(ctx, 90*time.Second)
		defer cancel()
		err := poll(wctx, func() error {
			if s.exited() {
				return &stopError{fmt.Errorf("splitter exited (code %d)", s.code)}
			}
			if len(engineProcs()) == 0 {
				return fmt.Errorf("nfqws2 not running yet")
			}
			if logCount(reloadMarkSingBox) == 0 {
				return fmt.Errorf("sing-box has not been restarted with the table config yet")
			}
			if _, err := s.Clash(wctx); err != nil {
				return err
			}
			st, err := statusNow()
			if err != nil {
				return err
			}
			if st.State != "connected" {
				return fmt.Errorf("state %q", st.State)
			}
			baseRecent = st.Recent // F3: the app records the first changes at its first reload, so the baseline is taken here
			return nil
		})
		if err != nil {
			return fmt.Errorf("%v\n%s", err, s.Diagnostics(ctx, "engine/sing-box not up after the second reload"))
		}
		return nil
	})

	var nfqArgs []string
	check("processes: sing-box and nfqws2 run in the client namespace", func() error {
		var errs []string
		// nfqws2 must stay up: the same pid and no new start for 5 s (a crash loop is caught between
		// restarts otherwise).
		starts := func() int { return strings.Count(s.Log(), "name=nfqws2 state=starting") }
		first := engineProcs()
		n0 := starts()
		for i := 0; i < 10 && len(first) > 0; i++ {
			time.Sleep(500 * time.Millisecond) // a negative: nothing to poll for
			if cur := engineProcs(); len(cur) == 0 || cur[0].pid != first[0].pid {
				errs = append(errs, "nfqws2 restarted or exited within 5 s")
				break
			}
		}
		if starts() != n0 {
			errs = append(errs, fmt.Sprintf("the app log shows %d new nfqws2 start(s) within 5 s", starts()-n0))
		}
		if len(errs) > 0 || len(first) == 0 {
			errs = append(errs, "app log lines about nfqws2:\n"+grepLines(s.Log(), "nfqws2")+"process output:\n"+grepLines(s.Log()+"\n"+s.output(), "process output"))
		}
		if len(procsOf(sb, s.PID())) == 0 {
			errs = append(errs, "sing-box is not running")
		}
		if p := engineProcs(); len(p) == 0 {
			errs = append(errs, "nfqws2 is not running")
		} else {
			nfqArgs = p[0].args
		}
		if len(errs) > 0 {
			return fmt.Errorf("%s", strings.Join(errs, "; "))
		}
		return nil
	})

	// --- 3. the routing proof, with the Clash API --------------------------------------------

	// Read-only diagnostics in the client namespace.
	linkCounters := func() string {
		var b strings.Builder
		for _, dev := range []string{"eth0", "splitter"} {
			out, _, err := l.Exec(ctx, "ip", "-s", "link", "show", dev)
			fmt.Fprintf(&b, "%s (%v)\n%s\n", dev, err, out)
		}
		return b.String()
	}
	// routeDiag answers which device the kernel picks for an unbound packet, for a packet marked like
	// the engine's raw packets (fwmark 0x40000000) and for a device-bound one.
	routeDiag := func(site netip.Addr) string {
		src := l.isp(isp).cliAddr.String()
		var b strings.Builder
		b.WriteString("--- routing diagnostics\n")
		for _, argv := range [][]string{
			{"ip", "rule", "show"}, {"ip", "-6", "rule", "show"}, {"ip", "route", "show", "table", "all"},
			{"ip", "route", "get", site.String(), "from", src},
			{"ip", "route", "get", site.String(), "from", src, "mark", "0x40000000"},
			{"ip", "route", "get", site.String(), "from", src, "oif", "eth0"},
		} {
			out, _, err := l.Exec(ctx, argv...)
			fmt.Fprintf(&b, "$ %s (%v)\n%s\n", strings.Join(argv, " "), err, out)
		}
		return b.String()
	}

	// ispDiag is the read-only state of the fake TSPU and of the ISP namespace after a request: the
	// engine's counters and every flow it logged since nFlows to the site, the ISP's nft ruleset (the
	// lab's own queue rules carry counters), its link counters and its conntrack entries for the site.
	ispDiag := func(site netip.Addr, nFlows int) string {
		var b strings.Builder
		b.WriteString("--- fakedpi (" + isp + ")\n")
		fmt.Fprintf(&b, "Stats: %v\n", l.DPI(isp).Stats())
		for _, f := range l.DPI(isp).Seen()[nFlows:] {
			if f.Dst.Addr() == site {
				fmt.Fprintf(&b, "flow: %+v\n", f)
			}
		}
		ns := l.isp(isp).ns
		for _, argv := range [][]string{
			{"nft", "list", "ruleset"}, {"ip", "-s", "link"},
			{"sh", "-c", "grep -F " + site.String() + " /proc/net/nf_conntrack || echo '(no entry or no /proc/net/nf_conntrack)'"},
		} {
			out, err := l.topo.Exec(ctx, ns, argv...)
			fmt.Fprintf(&b, "--- %s ns: %s (%v)\n%s\n", isp, strings.Join(argv[:min(len(argv), 3)], " "), err, out)
		}
		return b.String()
	}

	// runCase fetches https://host/ (rate limited, so the connection stays open) and, while it runs,
	// polls the Clash API for the connection. extra are more curl arguments (e.g. --resolve).
	runCase := func(c routeCase, extra ...string) error {
		site := l.Site(c.host)
		ip := site.Addr.Addr()
		nConns := len(site.Server.Conns())
		nFlows := len(l.DPI(isp).Seen())
		// The desync cases and the plain direct control are captured on eth0 and on the TUN device
		// (H1: the engine's raw packets, marked and not bound to a device, follow the TUN policy
		// route instead of leaving through eth0), with the interface counters before and after.
		var sniffer, tunSniffer *Sniffer
		var extraSniff []namedSniffer
		var linksBefore string
		if c.tag == "desync" || c.host == "direct-a.test" || c.host == "direct.desync-p.test" {
			linksBefore = linkCounters()
			if sn, err := l.SniffClient("eth0", c.host); err != nil {
				t.Logf("sniffer eth0: %v", err)
			} else {
				sniffer = sn
			}
			if sn, err := l.SniffClient("splitter", c.host); err != nil {
				t.Logf("sniffer splitter: %v", err)
			} else {
				tunSniffer = sn
			}
			// Who sends the RST? Capture the same flow in the ISP (client side and inet side) and in inet.
			for _, p := range []struct {
				name  string
				ns    *netns.NS
				iface string
			}{{"isp-lan", l.isp(isp).ns, "lan"}, {"isp-wan", l.isp(isp).ns, "wan"}, {"inet-isp", l.inet, "isp" + strconv.Itoa(l.isp(isp).idx)}} {
				if c.tag != "desync" {
					break // the ISP and inet captures are for the desync cases only
				}
				if sn, err := l.SniffNS(p.ns, p.iface, c.host); err != nil {
					t.Logf("sniffer %s: %v", p.name, err)
				} else {
					extraSniff = append(extraSniff, namedSniffer{p.name, sn})
				}
			}
		}
		done := make(chan error, 1)
		go func() {
			_, err := l.Curl(ctx, append([]string{"--curves", "X25519", "--limit-rate", "400k", "-o", "/dev/null"},
				append(extra, "https://"+c.host+"/")...)...)
			done <- err
		}()
		var seen *ClashConn
		var curlErr error
	wait:
		for {
			select {
			case curlErr = <-done:
				break wait
			case <-time.After(150 * time.Millisecond):
			}
			if seen != nil {
				continue
			}
			conns, err := s.Clash(ctx)
			if err != nil {
				continue
			}
			for _, cc := range conns {
				if cc.DstIP == ip || cc.Host == c.host {
					cc := cc
					seen = &cc
					break
				}
			}
		}

		var errs []string
		fail := func(f string, a ...any) { errs = append(errs, fmt.Sprintf(f, a...)) }

		var sniffed, sniffedHead, tunHead, h1, extraTxt, dpiTxt string
		var onFail []func() // files only written when the case fails
		if sniffer != nil {
			toSite := func(frames []Frame) []Frame {
				var out []Frame
				for _, f := range frames {
					if f.Src == netip.AddrPortFrom(ip, 443) || f.Dst == netip.AddrPortFrom(ip, 443) {
						out = append(out, f)
					}
				}
				return out
			}
			ethFrames := toSite(sniffer.Stop())
			var tunFrames []Frame
			if tunSniffer != nil {
				tunFrames = toSite(tunSniffer.Stop())
			}
			sniffed = FormatFrames(ethFrames, 0)
			sniffedHead = FormatFrames(ethFrames, 40)
			tunHead = FormatFrames(tunFrames, 40)
			// H1 verdict from the captures alone: payload segments of the client's hello that appear
			// on the TUN device with the engine's TTL but never on eth0.
			payloadOut := func(fs []Frame) (n int) {
				for _, f := range fs {
					if f.Dst.Addr() == ip && f.Len > 0 {
						n++
					}
				}
				return
			}
			h1 = fmt.Sprintf("client payload segments to the site: %d left through eth0, %d went into the TUN device splitter (a nonzero splitter count with 0 on eth0 means the packets were routed into the TUN, F5)", payloadOut(ethFrames), payloadOut(tunFrames))
			counters := "--- ip -s link before\n" + linksBefore + "--- ip -s link after\n" + linkCounters()
			rd := routeDiag(ip)
			all := c.host + "\n" + h1 + "\n--- eth0 (flow to the site)\n" + sniffed + "--- splitter (TUN, flow to the site)\n" + FormatFrames(tunFrames, 0) + counters + rd
			_ = os.WriteFile(filepath.Join(l.logDir, "sniff-"+c.host+".txt"), []byte(all), 0o644)
			if c.tag == "desync" {
				_ = os.WriteFile(filepath.Join(l.logDir, "desync-sniff.txt"), []byte(all), 0o644)
			}
			for _, ex := range extraSniff {
				fs := toSite(ex.sn.Stop())
				name, txt := ex.name, FormatFrames(fs, 0)
				onFail = append(onFail, func() {
					_ = os.WriteFile(filepath.Join(l.logDir, "sniff-"+c.host+"-"+name+".txt"), []byte(txt), 0o644)
				})
				extraTxt += "--- " + ex.name + " (first 40)\n" + FormatFrames(fs, 40)
			}
			_ = os.WriteFile(filepath.Join(l.logDir, "sniff-"+c.host+"-eth0.txt"), []byte(sniffed), 0o644)
			_ = os.WriteFile(filepath.Join(l.logDir, "sniff-"+c.host+"-splitter.txt"), []byte(FormatFrames(tunFrames, 0)), 0o644)
			if len(extraSniff) > 0 {
				dpiTxt = ispDiag(ip, nFlows)
				onFail = append(onFail, func() {
					_ = os.WriteFile(filepath.Join(l.logDir, "isp-diag-"+c.host+".txt"), []byte(dpiTxt), 0o644)
				})
			}
			tunHead += counters + rd
		}
		// The site's log.
		got := newConns(c.host, nConns)
		switch c.from {
		case "":
			if curlErr == nil {
				fail("curl succeeded, want a failure")
			}
			if len(got) != 0 {
				fail("the site saw %+v, want nothing", got)
			}
		default:
			if curlErr != nil {
				fail("curl: %v", curlErr)
			}
			want := l.ClientAddr(isp)
			if c.from == "vps" {
				want = l.VPSAddr()
			}
			if len(got) == 0 {
				fail("the site saw no connection")
			}
			for _, g := range got {
				if g.Remote.Addr() != want || g.SNI != c.host {
					fail("the site saw %v with SNI %q, want %v with SNI %q", g.Remote.Addr(), g.SNI, want, c.host)
				}
			}
		}

		// The fake TSPU's log.
		var toSite []fakedpi.Flow
		for _, f := range l.DPI(isp).Seen()[nFlows:] {
			if f.Dst.Addr() == ip || f.SNI == c.host {
				toSite = append(toSite, f)
			}
		}
		switch c.dpi {
		case "proxy":
			// Plain SOCKS5: the ClientHello crosses the ISP unencrypted on its way to the VPS, so
			// the TSPU legitimately sees the SNI there. It must never see a flow to the site.
			for _, f := range toSite {
				if f.Dst.Addr() == ip {
					fail("the TSPU saw a flow to the site address, want only the flow to the proxy: %+v", f)
				} else if f.Dst != netip.AddrPortFrom(l.VPSAddr(), socksPort) {
					fail("the SNI %s went to %v, want only the VPS SOCKS port: %+v", c.host, f.Dst, f)
				}
			}
		case "none":
			if len(toSite) != 0 {
				fail("the TSPU saw %d flow(s) to the site or with its SNI, want none: %+v", len(toSite), toSite)
			}
		default:
			i := slices.IndexFunc(toSite, func(f fakedpi.Flow) bool { return f.Dst.Addr() == ip && f.SNI == c.host })
			switch {
			case i < 0:
				fail("the TSPU saw no flow to %v with SNI %s: %+v", ip, c.host, toSite)
			case toSite[i].Verdict != fakedpi.Accept || toSite[i].Rule != "":
				fail("the TSPU took %s/%s on the flow, want accept: %+v", toSite[i].Verdict, toSite[i].Rule, toSite[i])
			case c.dpi == "split" && toSite[i].Hellos < 1:
				fail("the TSPU saw no ClientHello start in the split flow: %+v", toSite[i])
			}
		}

		// The Clash API.
		if c.from == "" {
			if seen != nil {
				t.Logf("%s: the Clash API listed a blocked connection: %+v", c.host, *seen)
			}
		} else if seen == nil {
			fail("the Clash API never listed the connection (polled while the transfer was open)")
		} else if seen.Host != c.host || seen.Outbound() != c.tag {
			fail("the Clash API shows host %q chain %v, want host %q last chain element %q", seen.Host, seen.Chains, c.host, c.tag)
		}
		if len(errs) > 0 {
			for _, f := range onFail {
				f()
			}
			if sniffer != nil {
				var d strings.Builder
				d.WriteString("\n--- " + h1 + "\n--- frames of the flow to the site (eth0, first 40) ---\n" + sniffedHead +
					"--- frames of the flow to the site (splitter TUN, first 40) ---\n" + tunHead + extraTxt + dpiTxt)
				for _, argv := range [][]string{{"ss", "-tnp"}, {"nft", "list", "table", "inet", "splitter"}} {
					out, _, err := l.Exec(ctx, argv...)
					fmt.Fprintf(&d, "--- client ns: %s (%v)\n%s\n", strings.Join(argv, " "), err, out)
				}
				fmt.Fprintf(&d, "--- nfqws2 debug log (tail)\n%s\n", nfqDebugTail(6000))
				fmt.Fprintf(&d, "--- app log lines about nfqws2\n%s--- app log 'process output' lines\n%s",
					grepLines(s.Log(), "nfqws2"), grepLines(s.Log()+"\n"+s.output(), "process output"))
				_ = os.WriteFile(filepath.Join(l.logDir, "desync-diag-"+c.host+".txt"), []byte(d.String()), 0o644)
				errs = append(errs, d.String())
			}
			return fmt.Errorf("%s", strings.Join(errs, "; "))
		}
		return nil
	}
	for _, c := range cases {
		name := c.host + " via " + c.tag
		if c.note != "" {
			name += " (" + c.note + ")"
		}
		check(name, func() error { return runCase(c) })
	}

	// --- 4. QUIC -----------------------------------------------------------------------------

	check("QUIC: with block_quic a UDP/443 datagram never crosses ispa", func() error {
		flows := len(l.DPI(isp).Seen())
		got := quicSeen()
		for i := 0; i < 3; i++ {
			if err := sendUDP(quicAddr); err != nil {
				return err
			}
		}
		time.Sleep(2 * time.Second) // a negative: nothing to poll for, so wait out the delivery time
		if n := quicSeen() - got; n != 0 {
			return fmt.Errorf("%d datagram(s) reached the site address in inet", n)
		}
		if fl := udpFlows(flows); len(fl) != 0 {
			return fmt.Errorf("fakedpi saw UDP/443 flows: %+v", fl)
		}
		return nil
	})

	// --- 5. findings: the current behaviour, with the review note ------------------------------

	dnsAsk := func(server, name string, qtype uint16) (*dns.Msg, error) {
		m := new(dns.Msg)
		m.SetQuestion(dns.Fqdn(name), qtype)
		c := &dns.Client{Net: "udp", Timeout: 5 * time.Second}
		var co *dns.Conn
		if err := l.cli.Do(func() error {
			var err error
			co, err = c.Dial(server)
			return err
		}); err != nil {
			return nil, err
		}
		defer co.Close()
		r, _, err := c.ExchangeWithConn(m, co)
		return r, err
	}
	dohCount := func(name, typ string) int {
		n := 0
		for _, q := range l.DoH().Queries() {
			if q.Name == name && q.Type == typ && q.Via == "doh" {
				n++
			}
		}
		return n
	}
	// A public resolver address (the VPS's resolver, in inet): the query enters the TUN and sing-box
	// hijacks it.
	hijacked := net.JoinHostPort(resolverAddr, "53")
	var fakeIP netip.Addr

	check("DNS through the TUN: a tunnel domain gets a FakeIP, a direct domain its real address", func() error {
		r, err := dnsAsk(hijacked, "tunnel-a.test", dns.TypeA)
		if err != nil {
			return err
		}
		for _, rr := range r.Answer {
			if a, ok := rr.(*dns.A); ok {
				ip, _ := netip.AddrFromSlice(a.A)
				fakeIP = ip.Unmap()
			}
		}
		if !netip.MustParsePrefix("198.18.0.0/15").Contains(fakeIP) {
			return fmt.Errorf("A tunnel-a.test = %v (%v), want an address in 198.18.0.0/15", fakeIP, r.Answer)
		}
		r, err = dnsAsk(hijacked, "direct-a.test", dns.TypeA)
		if err != nil {
			return err
		}
		if len(r.Answer) != 1 {
			return fmt.Errorf("A direct-a.test = %v", r.Answer)
		}
		if a, ok := r.Answer[0].(*dns.A); !ok || !a.A.Equal(siteIP("direct-a.test").AsSlice()) {
			return fmt.Errorf("A direct-a.test = %v, want the site address %v", r.Answer, siteIP("direct-a.test"))
		}
		return nil
	})
	if fakeIP.IsValid() {
		check("tunnel via FakeIP: connecting to the FakeIP address reaches the site through the VPS", func() error {
			c := routeCase{host: "tunnel-a.test", tag: "tunnel", from: "vps", dpi: "none"}
			return runCase(c, "--resolve", "tunnel-a.test:443:"+fakeIP.String())
		})
	}

	check("D1 (REVIEW-NOTES Input for decisions): a TXT query for a tunnel domain reaches the direct DoH server", func() error {
		beforeTXT, beforeHTTPS := dohCount("tunnel-a.test", "TXT"), dohCount("tunnel-a.test", "HTTPS")
		_, _ = dnsAsk(hijacked, "tunnel-a.test", dns.TypeTXT) // the answer does not matter
		_, _ = dnsAsk(hijacked, "tunnel-a.test", dns.TypeHTTPS)
		ctx2, cancel := context.WithTimeout(ctx, 10*time.Second)
		defer cancel()
		err := poll(ctx2, func() error {
			if dohCount("tunnel-a.test", "TXT") <= beforeTXT {
				return fmt.Errorf("no TXT query yet")
			}
			return nil
		})
		if err != nil {
			return fmt.Errorf("the TXT query did not reach the DoH server (D1 would be fixed or the DoH server unreachable for sing-box): %v", err)
		}
		if n := dohCount("tunnel-a.test", "HTTPS"); n != beforeHTTPS {
			return fmt.Errorf("the HTTPS query reached the DoH server (%d new), the generated config rejects it", n-beforeHTTPS)
		}
		return nil
	})

	check("LAN resolver (REVIEW-NOTES Session 3): the system resolver bypasses the TUN, so the ISP resolver sees the tunnel domain and the app gets the real address", func() error {
		out, _, err := l.Exec(ctx, "getent", "ahostsv4", "tunnel-a.test")
		if err != nil {
			return err
		}
		f := strings.Fields(string(out))
		if len(f) == 0 {
			return fmt.Errorf("getent returned nothing")
		}
		ip, _ := netip.ParseAddr(f[0])
		if ip != siteIP("tunnel-a.test") {
			return fmt.Errorf("getent ahostsv4 tunnel-a.test = %v, want the real address %v (current behaviour: no FakeIP for apps using the LAN resolver)", ip, siteIP("tunnel-a.test"))
		}
		if !slices.ContainsFunc(l.isp(isp).dns.Queries(), func(q fakedns.Query) bool {
			return q.Name == "tunnel-a.test" && q.Type == "A" && q.Via == "udp"
		}) {
			return fmt.Errorf("the ISP's plain resolver has no A query for tunnel-a.test: %+v", l.isp(isp).dns.Queries())
		}
		return nil
	})
	l.Report("routing: IPv6 bypass (REVIEW-NOTES Session 3)", true, "SKIPPED: the lab is IPv4-only")
	t.Log("IPv6 bypass: skipped, the lab has no IPv6")

	// --- 6. flags and files the app used -------------------------------------------------------

	check("F4: nfqws2 keeps the app identity (--uid=0:0), the hostlist is the 0600 file in run", func() error {
		// nfqws2 rewrites its argv in place when it parses --uid=uid:gid, so /proc shows "--uid=0" and "0".
		uidOK := slices.Contains(nfqArgs, "--uid=0:0")
		for i := 0; i+1 < len(nfqArgs) && !uidOK; i++ {
			uidOK = nfqArgs[i] == "--uid=0" && nfqArgs[i+1] == "0"
		}
		if !uidOK {
			return fmt.Errorf("the nfqws2 command line lacks --uid=0:0: %q", nfqArgs)
		}
		fi, err := os.Stat(filepath.Join(s.root, "run", "hostlist.txt"))
		if err != nil {
			return err
		}
		if fi.Mode().Perm() != 0o600 {
			return fmt.Errorf("hostlist mode %v, want 0600", fi.Mode().Perm())
		}
		return nil
	})
	check("nfqws2 command line and hostlist", func() error {
		if len(nfqArgs) == 0 {
			return fmt.Errorf("no nfqws2 command line was read")
		}
		var errs []string
		for _, want := range []string{"--qnum=200", "--hostlist=hostlist.txt", "--filter-l7=tls", "--payload=tls_client_hello", "--lua-desync=multisplit:pos=midsld"} {
			if !slices.Contains(nfqArgs, want) {
				errs = append(errs, "missing "+want)
			}
		}
		if !slices.ContainsFunc(nfqArgs, func(a string) bool { return strings.HasPrefix(a, "--lua-init=@") }) {
			errs = append(errs, "no --lua-init=@...")
		}
		if slices.ContainsFunc(nfqArgs, func(a string) bool { return strings.HasPrefix(a, "--fwmark") }) {
			errs = append(errs, "--fwmark is emitted although the mark is the default 0x40000000 (nfqws/API.md: only for a non-default mark)")
		}
		hl, err := os.ReadFile(filepath.Join(s.root, "run", "hostlist.txt"))
		if err != nil {
			errs = append(errs, err.Error())
		} else {
			lines := strings.Fields(string(hl))
			for _, w := range []string{"desync-a.test", "desync-p.test"} {
				if !slices.Contains(lines, w) {
					errs = append(errs, "hostlist lacks "+w)
				}
			}
			for _, w := range []string{"direct-a.test", "direct.desync-p.test", "tunnel-a.test"} {
				if slices.Contains(lines, w) {
					errs = append(errs, "hostlist holds "+w)
				}
			}
		}
		if len(errs) > 0 {
			return fmt.Errorf("%s; args: %q", strings.Join(errs, "; "), nfqArgs)
		}
		return nil
	})
	l.Report("routing: nfqws2 --fwmark (flag unverified in nfqws/API.md)", true,
		"NOT EXERCISED: the app emits --fwmark only for a non-default mark and the lab uses the default; the command line used is recorded in the previous scenario: "+strings.Join(nfqArgs, " "))
	l.Report("routing: nfqws2 hostlist re-read (G13)", true,
		"NOT EXERCISED: it needs a table change moving a domain to desync while connected; the checker is held still here (L9 scenario, PLAN.md)")
	t.Logf("nfqws2 command line: %q", nfqArgs)

	check("the table did not change during the run", func() error {
		st, err := statusNow()
		if err != nil {
			return err
		}
		if st.State != "connected" {
			return fmt.Errorf("state %q at the end, want connected", st.State)
		}
		// Recent holds the route changes of the first seeded table (defaults to the seed) too; only
		// entries that were not there when the seeded table was reached count.
		var fresh []labChange
		for _, c := range st.Recent {
			if !slices.ContainsFunc(baseRecent, func(b labChange) bool { return b == c }) {
				fresh = append(fresh, c)
			}
		}
		if len(fresh) != 0 || !mapsEqual(st.Routes, wantRoutes) {
			return fmt.Errorf("routes %v, new route changes %+v; want %v and none", st.Routes, fresh, wantRoutes)
		}
		return nil
	})
	if t.Failed() {
		t.Log(s.Diagnostics(ctx, "TestRouting failed"))
	}
}

func flatten(cmds [][]string) []string {
	var out []string
	for _, c := range cmds {
		out = append(out, c...)
	}
	return out
}

func mapsEqual(a, b map[route.Route]int) bool {
	for k, v := range a {
		if v != 0 && b[k] != v {
			return false
		}
	}
	for k, v := range b {
		if a[k] != v {
			return false
		}
	}
	return true
}

// procInfo is one running process found in /proc.
type procInfo struct {
	pid  int
	args []string
}

// procsOf lists the processes whose executable is exe and that share the network namespace of
// refPID (the splitter), so a same-named process elsewhere on the host (the lab's VPS runs a copy of
// sing-box from another path, a real nfqws2 of the host) is never picked up. Read only.
func procsOf(exe string, refPID int) []procInfo {
	want, err := filepath.EvalSymlinks(exe)
	if err != nil {
		want = exe
	}
	refNS, _ := os.Readlink("/proc/" + strconv.Itoa(refPID) + "/ns/net")
	ents, _ := os.ReadDir("/proc")
	var out []procInfo
	for _, e := range ents {
		pid, err := strconv.Atoi(e.Name())
		if err != nil {
			continue
		}
		dir := "/proc/" + e.Name()
		link, err := os.Readlink(dir + "/exe")
		if err != nil || strings.TrimSuffix(link, " (deleted)") != want {
			continue
		}
		if ns, err := os.Readlink(dir + "/ns/net"); err != nil || ns != refNS || refNS == "" {
			continue
		}
		b, _ := os.ReadFile(dir + "/cmdline")
		out = append(out, procInfo{pid: pid, args: strings.Split(strings.TrimRight(string(b), "\x00"), "\x00")})
	}
	return out
}

// nftQueue200 matches the queue rule in the Setup text ("queue num 200 bypass") and in the kernel
// listing, which nftables 1.1.6 normalizes to "queue flags bypass to 200".
var nftQueue200 = regexp.MustCompile(`queue .*\bto 200\b|queue num 200\b`)

// namedSniffer is a capture point of one desync case.
type namedSniffer struct {
	name string
	sn   *Sniffer
}

// grepLines returns the lines of text that contain sub (each with a trailing newline), at most the
// last 60.
func grepLines(text, sub string) string {
	var out []string
	for _, l := range strings.Split(text, "\n") {
		if strings.Contains(l, sub) {
			out = append(out, l)
		}
	}
	if len(out) > 60 {
		out = out[len(out)-60:]
	}
	if len(out) == 0 {
		return "(none)\n"
	}
	return strings.Join(out, "\n") + "\n"
}
