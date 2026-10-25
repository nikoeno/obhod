//go:build linux && lab

package lab

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/netip"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nikoejento/obhod/internal/lib/faultserver"
	"github.com/nikoejento/obhod/internal/lib/route"
	"github.com/nikoejento/obhod/lab/fakedpi"
)

// lifecycleAppYAML is merged over the lab defaults for TestLifecycle.
//
//   - A 40 s check interval (not the lab default of 15 s): a failure-triggered recheck of one domain
//     has to be told apart from a periodic full check (a targeted recheck rewrites one decision, a
//     full check rewrites all), and a full check must not start while the failure-triggered one runs.
//     decide.confirmations stays 2; schedule.failthreshold is the default 3 within 5 min.
//   - The bulk stage is on (64 KiB download, stall window 12-36 KiB): STALL_16_20 is only visible there.
//   - The desync strategy is the one of TestRouting (split the ClientHello at the second-level label).
const lifecycleAppYAML = `
checker:
  schedule: {interval: 40s, jitter: 5s, mingap: 10s}
analyzer:
  options:
    stages: tcp,tls,http,bulk
    bulk: {mode: download, size: 65536, stall_min: 12288, stall_max: 36864}
desync:
  strategy:
    - "--filter-l7=tls"
    - "--payload=tls_client_hello"
    - "--lua-desync=multisplit:pos=midsld"
`

// TestLifecycle is L9: one `splitter run` (sing-box TUN, nfqws2) in the client namespace, driven
// through a live failure, a table change that moves a domain to desync, an uplink change, a second
// instance, and finally kill -9. Scenarios run in order on the same process.
//
// Domains (all 1 MiB sites): pin-desync.test is `desync` on both ISPs and its fake TSPU rule is
// Reassemble, so the checker's probes keep failing (TLS_RST) and keep it in desync (PLAN.md L-D5):
// the table always has a desync domain, so the engine plan never disappears whatever the checker does
// to the other domains (the probes of a desync domain are mangled by the engine too, so a domain that
// only NaiveSNI blocks flaps back to direct after two more checks).
func TestLifecycle(t *testing.T) {
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
	sb, err := thirdParty("sing-box")
	if err != nil {
		t.Skipf("the pinned sing-box is missing (make fetch): %v", err)
	}
	before := rootSnapshot(t)
	t.Cleanup(func() { before.compare(t, rootSnapshot(t)) }) // registered first: runs after the lab's teardown

	l := Up(t)
	const (
		isp   = "ispa"
		other = "ispb"
		pin   = "pin-desync.test"
		live  = "live-stall.test"
		late  = "to-desync.test"
		swap  = "swap-desync.test"
	)
	ctx := context.Background()

	// --- sites, rules, seed -----------------------------------------------------------------

	ok := faultserver.Spec{Behavior: faultserver.OK, Bytes: 1024}
	for _, n := range []string{"control-a.test", "control-b.test", "canary.test", "neutral.test"} {
		l.AddSite(n, ok)
	}
	big := faultserver.Spec{Behavior: faultserver.OK, Bytes: 1 << 20}
	for _, n := range []string{pin, live, late, swap} {
		l.AddSite(n, big)
	}
	canary := fakedpi.Rule{Kind: fakedpi.NaiveSNI, Domains: []string{"canary.test"}}
	pinRule := fakedpi.Rule{Kind: fakedpi.Reassemble, Domains: []string{pin}}
	host32 := func(h string) netip.Prefix { return netip.PrefixFrom(l.Site(h).Addr.Addr(), 32) }
	stallRule := fakedpi.Rule{Kind: fakedpi.Stall, Prefix: host32(live), Bytes: 16000}
	lateRule := fakedpi.Rule{Kind: fakedpi.NaiveSNI, Domains: []string{late}}
	l.SetRules(isp, canary, pinRule)
	l.SetRules(other, canary, pinRule)

	// --- helpers ---------------------------------------------------------------------------

	check := func(name string, fn func() error) bool {
		t.Helper()
		err := fn()
		note := "ok"
		if err != nil {
			note = err.Error()
			t.Errorf("%s: %v", name, err)
		}
		l.Report("lifecycle: "+name, err == nil, note)
		return err == nil
	}
	mustPass := func(name string, fn func() error) {
		t.Helper()
		if !check(name, fn) {
			t.FailNow()
		}
	}
	note := func(name, text string) {
		t.Logf("%s: %s", name, text)
		l.Report("lifecycle: "+name, true, text)
	}

	s := l.RunSplitter(ctx, SplitterOptions{
		Args:    []string{"run"},
		AppYAML: lifecycleAppYAML,
		Decisions: map[string]route.Route{
			pin: route.Desync, live: route.Direct, late: route.Direct, swap: route.Direct,
		},
		OtherDecisions: map[string]map[string]route.Route{
			other: {pin: route.Desync, live: route.Direct, late: route.Direct, swap: route.Desync},
		},
	})
	exitedErr := func() error {
		if s.exited() {
			return &stopError{fmt.Errorf("splitter exited (code %d); output:\n%s", s.code, tail([]byte(s.output()), 4096))}
		}
		return nil
	}
	// waitFor polls fn every 100 ms until it reports done, the splitter exits, or within passes.
	waitFor := func(within time.Duration, what string, fn func() (bool, error)) error {
		pctx, cancel := context.WithTimeout(ctx, within)
		defer cancel()
		err := poll(pctx, func() error {
			if err := exitedErr(); err != nil {
				return err
			}
			done, err := fn()
			if err != nil {
				return err
			}
			if !done {
				return errors.New("not yet")
			}
			return nil
		})
		if err != nil {
			return fmt.Errorf("%s (waited up to %v): %w\n%s", what, within, err, s.Diagnostics(ctx, what))
		}
		return nil
	}
	statusNow := func() (labStatus, error) {
		var st labStatus
		b, err := s.Status(ctx)
		if err != nil {
			return st, err
		}
		return st, json.Unmarshal(b, &st)
	}
	// decisionsOf is the stored table of isp's network under whichever ID the app uses (asn first).
	decisionsOf := func(isp string) (map[string]storeDecision, error) {
		b, err := s.Decisions()
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		if err != nil {
			return nil, err
		}
		f, err := parseStore(b)
		if err != nil {
			return nil, err
		}
		for _, id := range s.networkIDs(isp) {
			if n := f.Networks[id]; n != nil {
				return n.Decisions, nil
			}
		}
		return nil, nil
	}
	fmtDec := func(d storeDecision) string {
		p := "none"
		if d.Pending != nil {
			p = fmt.Sprintf("%s×%d", d.Pending.Route, d.Pending.Count)
		}
		return fmt.Sprintf("{route %s, pending %s, last %v, checked %s}", d.Route, p, d.Last, d.CheckedAt.Format("15:04:05.000"))
	}

	logCount := func(sub string) int { return strings.Count(s.Log(), sub) }
	const (
		engineStart = "name=nfqws2 state=starting"
		markEngine  = "reload: desync engine not (re)started"
		markSingBox = "sing-box restarted with a new config"
	)
	engineProcs := func() []procInfo { return procsOf(nfq, s.PID()) }
	enginePID := func() int {
		if p := engineProcs(); len(p) > 0 {
			return p[0].pid
		}
		return 0
	}
	waitLog := func(sub string, n int, within time.Duration) error {
		return waitFor(within, fmt.Sprintf("a new %q in the app log", sub), func() (bool, error) { return logCount(sub) > n, nil })
	}
	nftListing := func() (string, error) {
		out, _, err := l.Exec(ctx, "nft", "list", "table", "inet", "splitter")
		return string(out), err
	}
	// bounce takes the client's active link down and up (the pc uplink watcher polls every 2 s and
	// arms the 3 s reload debounce); UseISP restores the link, the default route and the resolver.
	bounce := func(name string) {
		l.must(l.topo.SetLink(l.cli, l.isp(name).cliIf, false))
		time.Sleep(3500 * time.Millisecond) // longer than the watcher's poll, so the down event is seen
		l.UseISP(name)
	}
	// applyLogged parses the commands of the last logged marker line and applies them in the client
	// namespace (only text parseNFTSetup accepted: nft add/flush on inet splitter, or the exact ip
	// rule exemption).
	applyLogged := func() (cmds [][]string, msg string, err error) {
		msg, err = logErrValue(s.Log(), markEngine)
		if err != nil {
			return nil, "", err
		}
		if cmds, err = parseNFTSetup(msg); err != nil {
			return nil, msg, fmt.Errorf("%v; message:\n%s", err, msg)
		}
		for _, argv := range cmds {
			actx, cancel := context.WithTimeout(ctx, 30*time.Second)
			_, _, err := l.Exec(actx, argv...)
			cancel()
			if err != nil {
				return cmds, msg, fmt.Errorf("%q: %w", argv, err)
			}
		}
		return cmds, msg, nil
	}

	// fetchResult is one rate-limited HTTPS request.
	type fetchResult struct {
		err  error
		conn *ClashConn // the live connection the Clash API listed while the transfer was open
		site []faultserver.Conn
	}
	// fetch downloads https://host/ at 400 KB/s (about 2.6 s, so the Clash API lists the connection)
	// and returns what the site and the Clash API saw. X25519 only: a post-quantum key share spans
	// two segments and NaiveSNI never reads such a hello.
	fetch := func(host string, extra ...string) fetchResult {
		srv := l.Site(host)
		n := len(srv.Server.Conns())
		done := make(chan error, 1)
		go func() {
			_, err := l.Curl(ctx, append([]string{"--curves", "X25519", "--limit-rate", "400k", "-o", "/dev/null"},
				append(extra, "https://"+host+"/")...)...)
			done <- err
		}()
		var r fetchResult
	wait:
		for {
			select {
			case r.err = <-done:
				break wait
			case <-time.After(150 * time.Millisecond):
			}
			if r.conn != nil {
				continue
			}
			conns, err := s.Clash(ctx)
			if err != nil {
				continue
			}
			for _, c := range conns {
				if c.Host == host || c.DstIP == srv.Addr.Addr() {
					c := c
					r.conn = &c
					break
				}
			}
		}
		r.site = srv.Server.Conns()[n:]
		return r
	}
	// expectFetch checks a fetch result: success, every connection of the site log from want with
	// the SNI, and (when tag is not empty) the Clash API's outbound.
	expectFetch := func(r fetchResult, host string, want netip.Addr, tag string) error {
		if r.err != nil {
			return fmt.Errorf("curl %s: %v", host, r.err)
		}
		if len(r.site) == 0 {
			return fmt.Errorf("the site %s saw no connection", host)
		}
		for _, g := range r.site {
			if g.Remote.Addr() != want || g.SNI != host {
				return fmt.Errorf("the site %s saw %v with SNI %q, want %v with SNI %q", host, g.Remote.Addr(), g.SNI, want, host)
			}
		}
		if tag != "" {
			switch {
			case r.conn == nil:
				return fmt.Errorf("the Clash API never listed the connection to %s", host)
			case r.conn.Outbound() != tag:
				return fmt.Errorf("the Clash API shows chain %v for %s, want outbound %q", r.conn.Chains, host, tag)
			}
		}
		return nil
	}
	// isReset: the censor's RST seen through the TUN and sing-box reaches curl as exit 35/52/55/56
	// or as "unexpected eof"/"connection reset"; a timeout (exit 28) is a different outcome.
	isReset := func(err error) bool { return isCurlReset(err) }

	// --- 0. start: connected, the nft table applied, the engine up ------------------------------
	//
	// The first table the app takes has no desync domains (the network is not identified yet), so it
	// connects; the stored table arrives with the identification and the first reload logs the nft
	// commands. An exit with code 2 at start would mean the first table already had desync domains.

	wantRoutes := map[route.Route]int{route.Direct: 3, route.Desync: 1}
	mustPass("start: connected with the stored table", func() error {
		return waitFor(150*time.Second, "connected with the seeded routes", func() (bool, error) {
			st, err := statusNow()
			if err != nil {
				return false, err
			}
			return st.State == "connected" && mapsEqual(st.Routes, wantRoutes), nil
		})
	})
	mustPass("start: the nft table is missing, the app logs the commands, the test applies them", func() error {
		if err := waitLog(markEngine, 0, 60*time.Second); err != nil {
			return err
		}
		cmds, msg, err := applyLogged()
		if err != nil {
			return err
		}
		if !strings.Contains(msg, `oifname "eth0"`) {
			return fmt.Errorf("the message does not name the uplink eth0:\n%s", msg)
		}
		out, err := nftListing()
		if err != nil {
			return err
		}
		if !nftQueue200.MatchString(out) {
			return fmt.Errorf("the kernel listing has no queue 200 rule after %d command(s): %s", len(cmds), out)
		}
		return nil
	})
	var engine0 int
	mustPass("start: after a forced reload nfqws2 and sing-box run the stored table", func() error {
		bounce(isp)
		if err := waitFor(90*time.Second, "nfqws2 running and a sing-box with the table config", func() (bool, error) {
			if enginePID() == 0 || logCount(markSingBox) == 0 {
				return false, nil
			}
			if _, err := s.Clash(ctx); err != nil {
				return false, nil
			}
			st, err := statusNow()
			if err != nil {
				return false, err
			}
			return st.State == "connected", nil
		}); err != nil {
			return err
		}
		engine0 = enginePID()
		return nil
	})
	mustPass("start: a direct domain works, the site sees the ISP's address", func() error {
		if err := expectFetch(fetch(live), live, l.ClientAddr(isp), "direct"); err != nil {
			return err
		}
		return nil
	})

	// --- 1. live failure: a stall on a direct domain while connected ---------------------------
	//
	// The monitor logs nothing and the status page shows no failure counts (monitor/API.md), so the
	// evidence is indirect, in three steps: (1) the stalled flows are visible in the Clash API inside
	// the monitor's stall window and frozen; (2) a failure-triggered recheck is a targeted one: it
	// rewrites the decision of live-stall.test alone, a periodic check rewrites every decision in one
	// store write (sampled every 200 ms); (3) after the confirmations the route is tunnel.

	check("live failure: a stall rule on a direct domain moves it to tunnel through monitor failures and a recheck", func() error {
		sbN, engN := logCount(markSingBox), logCount(engineStart)
		// Start right after a periodic check ended (its writes show as a change of the sibling
		// decision), so the next one is about 40 s away and the failure-triggered recheck runs first.
		d0, err := decisionsOf(isp)
		if err != nil {
			return err
		}
		prev := map[string]time.Time{}
		for k, v := range d0 {
			prev[k] = v.CheckedAt
		}
		if err := waitFor(checksWithinLC(2), "a periodic check to end", func() (bool, error) {
			d, err := decisionsOf(isp)
			if err != nil {
				return false, err
			}
			return d[late].CheckedAt.After(prev[late]), nil
		}); err != nil {
			return err
		}
		l.SetRules(isp, canary, pinRule, stallRule)

		var wg sync.WaitGroup
		stalled := func() {
			for i := 0; i < 3; i++ { // the failure threshold is 3 within 5 min
				wg.Add(1)
				go func() {
					defer wg.Done()
					// A later --max-time wins over Curl's 10 s: the flow must live past the monitor's 10 s.
					_, _ = l.Curl(ctx, "--curves", "X25519", "--max-time", "22", "-o", "/dev/null", "https://"+live+"/")
				}()
			}
		}
		stalled()
		// (1) the stalled flows are in the stall window and frozen.
		var window string
		err = waitFor(20*time.Second, "stalled connections in the Clash API", func() (bool, error) {
			conns, err := s.Clash(ctx)
			if err != nil {
				return false, nil
			}
			var n int
			for _, c := range conns {
				if c.Host == live && c.Download >= 12288 && c.Download <= 36864 {
					n++
					window = fmt.Sprintf("download %d B, chain %v", c.Download, c.Chains)
				}
			}
			return n >= 3, nil
		})
		if err != nil {
			wg.Wait()
			return fmt.Errorf("the stall is not visible as the monitor's precondition: %w", err)
		}
		// (2) and (3): sample the store.
		cur, _ := decisionsOf(isp)
		prev = map[string]time.Time{}
		for k, v := range cur {
			prev[k] = v.CheckedAt
		}
		var lone, firstPending string
		var batches = 1
		batchEnd := time.Now().Add(23 * time.Second)
		err = waitFor(150*time.Second, "live-stall.test to reach tunnel", func() (bool, error) {
			d, err := decisionsOf(isp)
			if err != nil {
				return false, err
			}
			var changed []string
			for k, v := range d {
				if !v.CheckedAt.Equal(prev[k]) {
					changed = append(changed, k)
					prev[k] = v.CheckedAt
				}
			}
			if len(changed) == 1 && changed[0] == live && lone == "" {
				lone = fmtDec(d[live])
			}
			if p := d[live].Pending; p != nil && firstPending == "" {
				firstPending = fmtDec(d[live])
			}
			if time.Now().After(batchEnd) && d[live].Route != route.Tunnel && batches < 4 {
				batches++
				batchEnd = time.Now().Add(23 * time.Second)
				stalled() // the next confirmation: three more failures
			}
			return d[live].Route == route.Tunnel, nil
		})
		wg.Wait()
		if err != nil {
			return err
		}
		if lone == "" {
			return fmt.Errorf("the route reached tunnel but no recheck of %s alone was seen: either the monitor's failures (3 stalled flows after %s) did not trigger the checker, "+
				"or a periodic full check coincided with them. Decisions: %v", live, window, prev)
		}
		// The reload follows: sing-box restarts with the new config (30 s gap), the engine is untouched
		// (its plan did not change).
		if err := waitLog(markSingBox, sbN, 90*time.Second); err != nil {
			return err
		}
		if n := logCount(engineStart); n != engN {
			return fmt.Errorf("the engine was restarted (%d new start lines) although a direct→tunnel change does not touch its plan", n-engN)
		}
		if p := enginePID(); p != engine0 {
			return fmt.Errorf("nfqws2 pid changed %d → %d", engine0, p)
		}
		// A new request goes through the VPS.
		var last error
		for i := 0; i < 15; i++ {
			if last = expectFetch(fetch(live), live, l.VPSAddr(), "tunnel"); last == nil {
				break
			}
			time.Sleep(time.Second) // sing-box may still be starting
		}
		if last != nil {
			return last
		}
		t.Logf("live failure: stall window %s; first pending state %s; lone recheck %s", window, firstPending, lone)
		return nil
	})

	// --- 2. table change to desync: engine first, then sing-box ---------------------------------

	check("table change to desync: the engine restarts with the new hostlist before sing-box, then the request succeeds", func() error {
		engN, sbN := logCount(engineStart), logCount(markSingBox)
		pid0 := enginePID()
		l.SetRules(isp, canary, pinRule, stallRule, lateRule)
		_, err := l.Curl(ctx, "--curves", "X25519", "-o", "/dev/null", "https://"+late+"/")
		switch {
		case err == nil:
			return fmt.Errorf("precondition: %s direct through the naive-SNI rule succeeded, the rule does not hold", late)
		case isCurlTimeout(err):
			return fmt.Errorf("precondition: %s direct through the naive-SNI rule timed out (curl exit 28), not reset: %v", late, err)
		case !isReset(err):
			return fmt.Errorf("precondition: %s direct through the naive-SNI rule: want a connection reset, got %v", late, err)
		}
		if err := waitFor(checksWithinLC(3), late+" to reach desync", func() (bool, error) {
			d, err := decisionsOf(isp)
			if err != nil {
				return false, err
			}
			return d[late].Route == route.Desync, nil
		}); err != nil {
			return err
		}
		if err := waitLog(engineStart, engN, 60*time.Second); err != nil {
			return err
		}
		if err := waitLog(markSingBox, sbN, 90*time.Second); err != nil {
			return err
		}
		engTimes, sbTimes := logLineTimes(s.Log(), engineStart), logLineTimes(s.Log(), markSingBox)
		if len(engTimes) <= engN || len(sbTimes) <= sbN {
			return fmt.Errorf("could not read the log times (engine %d, sing-box %d lines)", len(engTimes), len(sbTimes))
		}
		if tE, tS := engTimes[engN], sbTimes[sbN]; tE.After(tS) {
			return fmt.Errorf("sing-box was restarted (%s) before the engine (%s)", tS.Format("15:04:05.000"), tE.Format("15:04:05.000"))
		}
		pid1 := enginePID()
		if pid1 == 0 || pid1 == pid0 {
			return fmt.Errorf("nfqws2 pid %d → %d: want a new process", pid0, pid1)
		}
		hl, err := os.ReadFile(filepath.Join(s.root, "run", "hostlist.txt"))
		if err != nil {
			return err
		}
		if !hostlistHas(hl, late) || !hostlistHas(hl, pin) {
			return fmt.Errorf("hostlist %q lacks %s or %s", hl, late, pin)
		}
		var last error
		for i := 0; i < 15; i++ {
			if last = expectFetch(fetch(late), late, l.ClientAddr(isp), "desync"); last == nil {
				break
			}
			time.Sleep(time.Second)
		}
		if last != nil {
			return last
		}
		d, _ := decisionsOf(isp)
		t.Logf("table change: engine pid %d → %d; decision %s", pid0, pid1, fmtDec(d[late]))
		return nil
	})
	note("G13: hostlist re-read", "the app restarts nfqws2 for any hostlist change (new pid, 'process name=nfqws2 state=starting' before the sing-box restart); whether nfqws2 re-reads the file on its own is not exercised because the app never relies on it")

	// --- 3. uplink change while connected ------------------------------------------------------

	check("uplink change: the nft rules no longer match, the app logs the commands and keeps the engine; after the test applies them it restarts", func() error {
		engN, errN := logCount(engineStart), logCount(markEngine)
		pid0 := enginePID()
		if pid0 == 0 {
			return fmt.Errorf("nfqws2 is not running before the uplink change")
		}
		l.UseISP(other)
		if err := waitLog(markEngine, errN, 60*time.Second); err != nil {
			return err
		}
		msg, err := logErrValue(s.Log(), markEngine)
		if err != nil {
			return err
		}
		cmds, err := parseNFTSetup(msg)
		if err != nil {
			return fmt.Errorf("%v; message:\n%s", err, msg)
		}
		for _, want := range []string{`oifname "eth1"`, "queue num 200"} {
			if !strings.Contains(msg, want) {
				return fmt.Errorf("the message lacks %s:\n%s", want, msg)
			}
		}
		for _, argv := range cmds {
			if isEngineMarkRule(argv) {
				return fmt.Errorf("the message prints the ip rule exemption again although it is present (only what is missing may be printed): %q", argv)
			}
		}
		if n := logCount(engineStart); n != engN {
			return fmt.Errorf("the engine was started again (%d new start lines) while the nft table did not match", n-engN)
		}
		engineKept := "kept (same pid)"
		switch p := enginePID(); {
		case p == pid0:
		case p == 0:
			engineKept = "stopped by an intermediate table without desync domains (the network was briefly unidentified)"
		default:
			return fmt.Errorf("nfqws2 pid changed %d → %d while the nft table did not match", pid0, p)
		}
		if out, err := nftListing(); err != nil || !strings.Contains(out, `oifname "eth0"`) {
			return fmt.Errorf("the old nft rules (eth0) should still be in place until the test applies the new ones: %v\n%s", err, out)
		}

		// The test applies the commands (same safety rules: parseNFTSetup text, client namespace).
		applied, _, err := applyLogged()
		if err != nil {
			return err
		}
		out, err := nftListing()
		if err != nil {
			return err
		}
		if !strings.Contains(out, `oifname "eth1"`) || !nftQueue200.MatchString(out) {
			return fmt.Errorf("after %d command(s) the kernel listing lacks oifname \"eth1\" or the queue rule:\n%s", len(applied), out)
		}
		// Force the reload and wait for the engine.
		bounce(other)
		if err := waitFor(90*time.Second, "nfqws2 running with the new plan", func() (bool, error) {
			p := enginePID()
			return p != 0 && p != pid0 && logCount(engineStart) > engN, nil
		}); err != nil {
			return err
		}
		if out, err := nftListing(); err != nil || !strings.Contains(out, `oifname "eth1"`) {
			return fmt.Errorf("the nft rules changed under the engine: %v\n%s", err, out)
		}
		// ispb has no desync-breaking rule for swap-desync.test: the request works and the site sees ispb.
		var res fetchResult
		var last error
		for i := 0; i < 15; i++ {
			res = fetch(swap)
			if last = expectFetch(res, swap, l.ClientAddr(other), ""); last == nil {
				break
			}
			time.Sleep(time.Second)
		}
		if last != nil {
			return last
		}
		tag := "(not listed)"
		if res.conn != nil {
			tag = res.conn.Outbound()
		}
		t.Logf("uplink change: engine %s while the table mismatched; commands applied: %d; engine pid %d → %d; %s went through outbound %q", engineKept, len(applied), pid0, enginePID(), swap, tag)
		return nil
	})
	l.UseISP(other) // idempotent; the later scenarios stay on ispb

	// --- 4. a second instance ----------------------------------------------------------------

	check("second instance: the same paths.root exits with code 3 and the first keeps running", func() error {
		sbPids := pids(procsOf(sb, s.PID()))
		engPids := pids(engineProcs())
		s2 := l.RunSplitter(ctx, SplitterOptions{Args: []string{"run"}, ReuseRootOf: s})
		wctx, cancel := context.WithTimeout(ctx, 40*time.Second)
		defer cancel()
		code, err := s2.Wait(wctx)
		if err != nil {
			return fmt.Errorf("the second splitter did not exit within 40 s: %v\noutput:\n%s", err, s2.output())
		}
		out := s2.output()
		if code != 3 {
			return fmt.Errorf("exit code %d, want 3; output:\n%s", code, out)
		}
		if lo := strings.ToLower(out); !strings.Contains(lo, "another") || !strings.Contains(lo, "instance") {
			return fmt.Errorf("the message does not name another instance: %q", out)
		}
		if s.exited() {
			return fmt.Errorf("the first splitter exited (code %d)", s.code)
		}
		st, err := statusNow()
		if err != nil {
			return err
		}
		if st.State != "connected" {
			return fmt.Errorf("the first splitter is %q, want connected", st.State)
		}
		// The loser must not have killed the first instance's children (the pre-flight order).
		if got := pids(procsOf(sb, s.PID())); !slices.Equal(got, sbPids) {
			return fmt.Errorf("sing-box pids %v → %v", sbPids, got)
		}
		if got := pids(engineProcs()); !slices.Equal(got, engPids) {
			return fmt.Errorf("nfqws2 pids %v → %v", engPids, got)
		}
		return nil
	})

	// --- 5. kill -9 (destructive, last) -------------------------------------------------------

	check("kill -9: sing-box and nfqws2 die with the app, the TUN disappears, the client reaches a site directly", func() error {
		nsLink, err := os.Readlink("/proc/" + strconv.Itoa(s.PID()) + "/ns/net")
		if err != nil {
			return err
		}
		vpsBin := filepath.Join(l.tmp, "vps-bin", "sing-box")
		children := func() []procInfo {
			return append(procsInNS(sb, nsLink), procsInNS(nfq, nsLink)...)
		}
		kids, vps := children(), procsInNS(vpsBin, "")
		if len(procsInNS(sb, nsLink)) == 0 || len(procsInNS(nfq, nsLink)) == 0 {
			return fmt.Errorf("before the kill sing-box or nfqws2 is not running: %v", pids(kids))
		}
		if len(vps) == 0 {
			return fmt.Errorf("the VPS sing-box is not found (%s)", vpsBin)
		}
		if _, _, err := l.Exec(ctx, "ip", "-br", "link", "show", "splitter"); err != nil {
			return fmt.Errorf("the TUN device splitter does not exist before the kill: %v", err)
		}
		s.Kill()
		t0 := time.Now()
		wctx, cancel := context.WithTimeout(ctx, 15*time.Second)
		defer cancel()
		if code, err := s.Wait(wctx); err != nil || code != -1 {
			return fmt.Errorf("the splitter did not die from the signal: code %d, %v", code, err)
		}
		if err := poll(wctx, func() error {
			if k := children(); len(k) > 0 {
				return fmt.Errorf("still running: %v", pids(k))
			}
			return nil
		}); err != nil {
			return fmt.Errorf("the app's children survive the kill -9 (Pdeathsig): %w", err)
		}
		gone := time.Since(t0)
		if err := poll(wctx, func() error {
			out, _, err := l.Exec(ctx, "ip", "-br", "link")
			if err != nil {
				return err
			}
			for _, f := range strings.Split(string(out), "\n") {
				if strings.HasPrefix(f, "splitter ") || strings.HasPrefix(f, "splitter@") {
					return fmt.Errorf("the TUN device is still there: %s", f)
				}
			}
			return nil
		}); err != nil {
			return err
		}
		if still := procsInNS(vpsBin, ""); !slices.Equal(pids(still), pids(vps)) {
			return fmt.Errorf("the VPS sing-box pids %v → %v (a lab process must not be affected)", pids(vps), pids(still))
		}
		// With no listener on queue 200 and `bypass` in the rule, traffic passes.
		var last error
		for i := 0; i < 15; i++ {
			srv := l.Site("neutral.test")
			n := len(srv.Server.Conns())
			_, cerr := l.Curl(ctx, "--curves", "X25519", "-o", "/dev/null", "https://neutral.test/")
			last = expectFetch(fetchResult{err: cerr, site: srv.Server.Conns()[n:]}, "neutral.test", l.ClientAddr(other), "")
			if last == nil {
				break
			}
			time.Sleep(time.Second)
		}
		if last != nil {
			return fmt.Errorf("a plain curl after the kill: %w", last)
		}
		t.Logf("kill -9: children gone after %v", gone.Round(100*time.Millisecond))
		return nil
	})

	// What is left behind after the kill, for docs/RECOVERY.md (read-only listings in the client
	// namespace and the app's run folder).
	check("kill -9: what is left behind is recorded", func() error {
		var b strings.Builder
		for _, argv := range [][]string{
			{"ip", "rule", "show"}, {"ip", "route", "show", "table", "all"}, {"nft", "list", "ruleset"}, {"ip", "-br", "link"},
		} {
			out, _, err := l.Exec(ctx, argv...)
			fmt.Fprintf(&b, "$ %s (%v)\n%s\n", strings.Join(argv, " "), err, out)
		}
		var files []string
		ents, err := os.ReadDir(filepath.Join(s.root, "run"))
		if err != nil {
			return err
		}
		for _, e := range ents {
			files = append(files, e.Name())
		}
		fmt.Fprintf(&b, "run/ after the kill: %s\n", strings.Join(files, ", "))
		if err := os.WriteFile(filepath.Join(l.logDir, "kill9-leftovers.txt"), []byte(b.String()), 0o644); err != nil {
			return err
		}
		rules, _, _ := l.Exec(ctx, "ip", "rule", "show")
		tbl, tblErr := nftListing()
		var rs []string
		for _, line := range strings.Split(string(rules), "\n") {
			if strings.TrimSpace(line) != "" && !strings.Contains(line, "lookup local") && !strings.Contains(line, "lookup main") && !strings.Contains(line, "lookup default") ||
				strings.Contains(line, "fwmark") {
				rs = append(rs, strings.TrimSpace(line))
			}
		}
		l.Report("lifecycle: kill -9 leftovers (docs/RECOVERY.md)", true,
			fmt.Sprintf("ip rules left: %q; table inet splitter still present: %v; run/: %s; full listing: kill9-leftovers.txt in the run dir",
				rs, tblErr == nil && tbl != "", strings.Join(files, ", ")))
		return nil
	})

	if t.Failed() {
		t.Log(s.Diagnostics(ctx, "TestLifecycle failed"))
	}
}

// checksWithinLC bounds n further checks of TestLifecycle's 40 s schedule (interval + jitter each),
// plus one long check run (four candidates with the bulk stage).
func checksWithinLC(n int) time.Duration {
	return time.Duration(n)*45*time.Second + 45*time.Second
}

// pids returns the sorted process ids of ps.
func pids(ps []procInfo) []int {
	out := make([]int, 0, len(ps))
	for _, p := range ps {
		out = append(out, p.pid)
	}
	slices.Sort(out)
	return out
}

// procsInNS lists the processes whose executable is exe and whose network namespace link is nsLink
// (any namespace when nsLink is empty). Unlike procsOf it needs no live reference process, so it
// works after the splitter was killed. Read only.
func procsInNS(exe, nsLink string) []procInfo {
	want, err := filepath.EvalSymlinks(exe)
	if err != nil {
		want = exe
	}
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
		if nsLink != "" {
			if ns, err := os.Readlink(dir + "/ns/net"); err != nil || ns != nsLink {
				continue
			}
		}
		b, _ := os.ReadFile(dir + "/cmdline")
		out = append(out, procInfo{pid: pid, args: strings.Split(strings.TrimRight(string(b), "\x00"), "\x00")})
	}
	return out
}
