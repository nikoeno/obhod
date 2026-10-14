//go:build linux && lab

package lab

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"net/netip"
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/nikoejento/obhod/internal/lib/faultserver"
	"github.com/nikoejento/obhod/internal/lib/route"
	"github.com/nikoejento/obhod/lab/fakedpi"
)

// labStatus is the part of status.Snapshot (GET /api/status, Go field names) the scenarios read.
// The lab may not import internal/pc/status, so the shape is repeated here.
type labStatus struct {
	State     string
	Network   string // ASN name, or "unknown network"
	LastCheck time.Time
	NextCheck time.Time
	Routes    map[route.Route]int
	Warnings  []string
	Recent    []labChange
}

// labChange is status.Change.
type labChange struct {
	Domain   string
	From, To route.Route
	At       time.Time
}

// Timing of TestChecker, derived from the schedule in the lab defaults (splitterconf.go).
const (
	// checkRun bounds one check: calibration (two controls, the canary) and four targets with the
	// analyzer timeouts of the lab defaults (an IP-blocked target needs 2 × 2 s TCP timeouts).
	checkRun = 30 * time.Second
	// identifyWithin bounds a network identification after a start or a link change: the netif
	// debounce, then up to two refreshes (the first one may find no gateway MAC in the ARP table
	// yet) plus the echo timeout (8 s).
	identifyWithin = labNetDebounce + 2*labNetRefresh + 8*time.Second + 5*time.Second
)

// checksWithin bounds n further checks: each waits at most one interval plus the jitter, and the
// last one has to finish.
func checksWithin(n int) time.Duration {
	return time.Duration(n)*(labCheckInterval+labCheckJitter) + checkRun
}

// TestChecker is L7 (checker M8): `splitter run --ui` (disconnected: the checker and the status
// page only; no sing-box, no desync engine) in the client namespace, with a short schedule and
// Confirmations 2. State is read through the status API and decisions.json (store schema 1).
// Cases run in order on one splitter; case 7 (L-D5) runs before case 6 so ispa's stored table
// (one desync entry) differs from ispb's (all direct), which makes the restore observable.
func TestChecker(t *testing.T) {
	if err := Preflight(); err != nil {
		t.Skip(err)
	}
	before := rootSnapshot(t)
	t.Cleanup(func() { before.compare(t, rootSnapshot(t)) }) // registered first: runs after the lab's teardown

	l := Up(t)
	const (
		isp      = "ispa"
		other    = "ispb"
		directOK = "direct-ok.test"
		sniDom   = "sni.test"
		ipbDom   = "ipb.test"
		reDom    = "re.test"
	)
	cands := []string{directOK, sniDom, ipbDom, reDom}
	ok := faultserver.Spec{Behavior: faultserver.OK, Bytes: 1024}
	for _, n := range append([]string{"control-a.test", "control-b.test", "canary.test", "neutral.test"}, cands...) {
		l.AddSite(n, ok)
	}
	// ispa's TSPU always blocks the canary (a realistic TSPU; calibration reports dpi_present).
	canary := fakedpi.Rule{Kind: fakedpi.NaiveSNI, Domains: []string{"canary.test"}}
	setRules := func(r ...fakedpi.Rule) { l.SetRules(isp, append([]fakedpi.Rule{canary}, r...)...) }
	setRules()
	ipb := l.Site(ipbDom).Addr.Addr()

	ctx := context.Background()
	s := l.RunSplitter(ctx, SplitterOptions{Args: []string{"run", "--ui"}, Candidates: cands})

	// --- helpers ---------------------------------------------------------------------------

	report := func(t *testing.T, scenario string, errs []string, note string) {
		t.Helper()
		for _, e := range errs {
			t.Error(e)
		}
		if len(errs) > 0 {
			note = strings.Join(errs, "; ")
		} else if note == "" {
			note = "ok"
		}
		l.Report("checker: "+scenario, len(errs) == 0, note)
	}
	exitedErr := func() error {
		if s.exited() {
			return &stopError{fmt.Errorf("splitter exited (code %d); output:\n%s", s.code, tail([]byte(s.output()), 4096))}
		}
		return nil
	}
	// diagnose dumps the state of a failed wait; during the network change case (deepDiag) it also
	// runs the stale-binding experiments of DiagnosticsDeep.
	deepDiag := false
	diagnose := func(what string) string {
		if deepDiag {
			return s.DiagnosticsDeep(ctx, what)
		}
		return s.Diagnostics(ctx, what)
	}
	// waitFor polls fn (every 100 ms) until it reports done, the splitter exits, or within passes.
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
			return fmt.Errorf("%s (waited up to %v): %w\n%s", what, within, err, diagnose(what))
		}
		return nil
	}
	status := func() (labStatus, error) {
		var st labStatus
		b, err := s.Status(ctx)
		if err != nil {
			return st, err
		}
		if err := json.Unmarshal(b, &st); err != nil {
			return st, fmt.Errorf("status JSON: %w", err)
		}
		return st, nil
	}
	store := func() (storeFile, error) {
		b, err := s.Decisions()
		if errors.Is(err, os.ErrNotExist) {
			return storeFile{Schema: 1, Networks: map[string]*storeNetwork{}}, nil
		}
		if err != nil {
			return storeFile{}, err
		}
		return parseStore(b)
	}
	// netOf returns the stored network of isp under whichever ID the app used (asn first).
	netOf := func(f storeFile, isp string) (string, *storeNetwork) {
		for _, id := range s.networkIDs(isp) {
			if n := f.Networks[id]; n != nil {
				return id, n
			}
		}
		return "", nil
	}
	decision := func(isp, dom string) (storeDecision, bool, error) {
		f, err := store()
		if err != nil {
			return storeDecision{}, false, err
		}
		_, n := netOf(f, isp)
		if n == nil {
			return storeDecision{}, false, nil
		}
		d, ok := n.Decisions[dom]
		return d, ok, nil
	}
	defaults := func(st labStatus) bool {
		return st.Routes[route.Tunnel] == len(cands) && st.Routes[route.Direct] == 0 && st.Routes[route.Desync] == 0
	}
	fmtDec := func(d storeDecision) string {
		p := "none"
		if d.Pending != nil {
			p = fmt.Sprintf("%s×%d", d.Pending.Route, d.Pending.Count)
		}
		return fmt.Sprintf("{route %s, pending %s, last %v, checked %s}", d.Route, p, d.Last, d.CheckedAt.Format("15:04:05.000"))
	}
	// awaitRoutes polls the stored decisions of isp until every domain in want has its route, and
	// returns, per domain, the distinct decision states it went through (oldest first).
	awaitRoutes := func(isp string, want map[string]route.Route, within time.Duration) (map[string][]storeDecision, error) {
		hist := map[string][]storeDecision{}
		err := waitFor(within, fmt.Sprintf("decisions %v on %s", want, isp), func() (bool, error) {
			f, err := store()
			if err != nil {
				return false, err
			}
			_, n := netOf(f, isp)
			if n == nil {
				return false, nil
			}
			all := true
			for dom, r := range want {
				d, ok := n.Decisions[dom]
				if !ok {
					all = false
					continue
				}
				h := hist[dom]
				if len(h) == 0 || fmtDec(h[len(h)-1]) != fmtDec(d) {
					hist[dom] = append(h, d)
				}
				if d.Route != r {
					all = false
				}
			}
			return all, nil
		})
		if err != nil {
			var last []string
			for dom, h := range hist {
				if len(h) > 0 {
					last = append(last, dom+" "+fmtDec(h[len(h)-1]))
				}
			}
			slices.Sort(last)
			return hist, fmt.Errorf("%w; last seen: %s", err, strings.Join(last, ", "))
		}
		return hist, nil
	}
	// hysteresis checks Confirmations 2 in a decision history that ends on want: the first check
	// after the change (checked after t0) that points at want leaves the old route with a pending
	// want×1; only the second one switches.
	hysteresis := func(dom string, hist []storeDecision, t0 time.Time, old, want route.Route) []string {
		var seq []string
		for _, d := range hist {
			seq = append(seq, fmtDec(d))
		}
		for _, d := range hist {
			if !d.CheckedAt.After(t0) {
				continue
			}
			if d.Route != want && (d.Pending == nil || d.Pending.Route != want) {
				continue // a check that did not see the change yet
			}
			if d.Route != old || d.Pending == nil || d.Pending.Route != want || d.Pending.Count != 1 {
				return []string{fmt.Sprintf("%s: the first check pointing at %s did not keep %s with a pending %s×1 (Confirmations 2); states: %s",
					dom, want, old, want, strings.Join(seq, " → "))}
			}
			if last := hist[len(hist)-1]; last.Route != want || last.Pending != nil {
				return []string{fmt.Sprintf("%s: final state %s, want %s without a pending route", dom, fmtDec(last), want)}
			}
			return nil
		}
		return []string{fmt.Sprintf("%s: no check after the change pointed at %s; states: %s", dom, want, strings.Join(seq, " → "))}
	}
	dpiReset := func(dom string, kind fakedpi.Kind) []string {
		if !slices.ContainsFunc(l.DPI(isp).Seen(), func(f fakedpi.Flow) bool {
			return f.SNI == dom && f.Verdict == fakedpi.Reset && f.Rule == kind
		}) {
			return []string{fmt.Sprintf("fakedpi on %s never reset a flow to %s with %s", isp, dom, kind)}
		}
		return nil
	}
	recentChange := func(dom string, to route.Route) []string {
		st, err := status()
		if err != nil {
			return []string{err.Error()}
		}
		if !slices.ContainsFunc(st.Recent, func(c labChange) bool {
			return c.Domain == dom && c.To == to
		}) {
			return []string{fmt.Sprintf("status Recent has no change of %s to %s: %+v", dom, to, st.Recent)}
		}
		return nil
	}
	countRoutes := func(n *storeNetwork) map[route.Route]int {
		m := map[route.Route]int{}
		for _, dom := range cands {
			if d, ok := n.Decisions[dom]; ok {
				m[d.Route]++
			} else {
				m[route.Tunnel]++ // UnknownDefault
			}
		}
		return m
	}
	sameCounts := func(st labStatus, want map[route.Route]int) bool {
		for _, r := range []route.Route{route.Direct, route.Desync, route.Tunnel} {
			if st.Routes[r] != want[r] {
				return false
			}
		}
		return true
	}
	// awaitLabel polls the status page until its network label is the ISP's name (netwatch names the
	// network by the ASN name once the ASN is looked up; right after a link change the label is
	// "unknown network" for a moment: debounce, echo request and ASN lookup). A timeout carries the
	// diagnostics of waitFor.
	awaitLabel := func(isp string) error {
		return waitFor(identifyWithin, fmt.Sprintf("status label %q (asn:%d)", isp, l.isp(isp).p.ASN), func() (bool, error) {
			st, err := status()
			return err == nil && st.Network == isp, err
		})
	}

	// --- 1. unknown network at first --------------------------------------------------------

	var first labStatus
	startErr := waitFor(30*time.Second, "status page up", func() (bool, error) {
		st, err := status()
		if err != nil {
			return false, nil // not listening yet
		}
		first = st
		return true, nil
	})
	if startErr != nil {
		t.Fatalf("%v\napp log:\n%s", startErr, tail([]byte(s.Log()), 4096))
	}

	t.Run("1 defaults or stored table at start, then a first check", func(t *testing.T) {
		var errs []string
		if first.State != "disconnected" {
			errs = append(errs, fmt.Sprintf("state %q, want disconnected (run --ui)", first.State))
		}
		// The network may already be identified (label = ISP name) by the first status answer, or
		// still be "unknown network". The store is empty, so either way every candidate is on its
		// default (tunnel) until a check has run.
		if first.Network != "unknown network" && first.Network != isp {
			errs = append(errs, fmt.Sprintf("first network label %q, want \"unknown network\" or %q", first.Network, isp))
		}
		if first.LastCheck.IsZero() && !defaults(first) {
			errs = append(errs, fmt.Sprintf("first table %v, want all %d candidates on tunnel (default) before the first check", first.Routes, len(cands)))
		}
		var bad []string
		err := waitFor(identifyWithin+checksWithin(1), "the first check", func() (bool, error) {
			st, err := status()
			if err != nil {
				return false, err
			}
			if st.LastCheck.IsZero() {
				if !defaults(st) && len(bad) < 5 {
					bad = append(bad, fmt.Sprint(st.Routes))
				}
				return false, nil
			}
			return true, nil
		})
		if err != nil {
			errs = append(errs, err.Error())
		}
		if len(bad) > 0 {
			errs = append(errs, fmt.Sprintf("before the first check the table left the defaults: %v", bad))
		}
		report(t, "defaults or stored table at start, then a first check", errs, "")
	})

	t.Run("1 network identified as AS64500", func(t *testing.T) {
		var errs []string
		if err := awaitLabel(isp); err != nil {
			errs = append(errs, err.Error())
		}
		f, ferr := store()
		if ferr != nil {
			errs = append(errs, ferr.Error())
		} else if id, n := netOf(f, isp); n == nil {
			errs = append(errs, fmt.Sprintf("decisions.json has no network for %s (want %q, have %q)", isp, s.networkIDs(isp)[0], slices.Sorted(maps.Keys(f.Networks))))
		} else if want := s.networkIDs(isp)[0]; id != want {
			errs = append(errs, fmt.Sprintf("decisions.json key %q, want %q (asn:<n>)", id, want))
		}
		report(t, "network identified as AS64500 (label ispa, key asn:64500)", errs, "")
	})

	// --- 2. first decisions ---------------------------------------------------------------

	t.Run("2 direct-ok.test is direct after a check", func(t *testing.T) {
		var errs []string
		want := map[string]route.Route{}
		for _, d := range cands {
			want[d] = route.Direct // no TSPU rule for any candidate yet
		}
		if _, err := awaitRoutes(isp, want, checksWithin(2)); err != nil {
			errs = append(errs, err.Error())
		} else if d, _, _ := decision(isp, directOK); d.Source != route.SourceChecked || d.Pending != nil {
			errs = append(errs, fmt.Sprintf("%s: %s source %q, want checked without pending", directOK, fmtDec(d), d.Source))
		}
		if err := waitFor(10*time.Second, "status shows every candidate direct", func() (bool, error) {
			st, err := status()
			return err == nil && st.Routes[route.Direct] == len(cands), err
		}); err != nil {
			errs = append(errs, err.Error())
		}
		report(t, "direct-ok.test is direct after a check", errs, "")
	})

	// --- 3. NaiveSNI: desync after two checks ----------------------------------------------

	t.Run("3 naive-sni: sni.test desync after 2 checks", func(t *testing.T) {
		t0 := time.Now()
		setRules(fakedpi.Rule{Kind: fakedpi.NaiveSNI, Domains: []string{sniDom}})
		hist, err := awaitRoutes(isp, map[string]route.Route{sniDom: route.Desync}, checksWithin(3))
		var errs []string
		if err != nil {
			errs = append(errs, err.Error())
		} else {
			errs = append(errs, hysteresis(sniDom, hist[sniDom], t0, route.Direct, route.Desync)...)
			errs = append(errs, recentChange(sniDom, route.Desync)...)
		}
		errs = append(errs, dpiReset(sniDom, fakedpi.NaiveSNI)...)
		report(t, "naive-sni: sni.test pending after 1 check, desync after 2", errs, "")
	})

	// --- 4. IPBlock: tunnel after two checks ------------------------------------------------

	t.Run("4 ip-block: ipb.test tunnel after 2 checks", func(t *testing.T) {
		t0 := time.Now()
		setRules(fakedpi.Rule{Kind: fakedpi.NaiveSNI, Domains: []string{sniDom}},
			fakedpi.Rule{Kind: fakedpi.IPBlock, Prefix: netip.PrefixFrom(ipb, ipb.BitLen())})
		hist, err := awaitRoutes(isp, map[string]route.Route{ipbDom: route.Tunnel}, checksWithin(3))
		var errs []string
		if err != nil {
			errs = append(errs, err.Error())
		} else {
			errs = append(errs, hysteresis(ipbDom, hist[ipbDom], t0, route.Direct, route.Tunnel)...)
			if d, _, _ := decision(isp, sniDom); d.Route != route.Desync {
				errs = append(errs, fmt.Sprintf("%s: %s, want it to stay desync", sniDom, fmtDec(d)))
			}
		}
		// Connections of earlier checks are expected; none once the block is in place (a probe that
		// was already connected when the rule came may finish within a second).
		for _, c := range l.Site(ipbDom).Server.Conns() {
			if c.At.After(t0.Add(time.Second)) {
				errs = append(errs, fmt.Sprintf("%s accepted a connection at %v through the IP block", ipbDom, c.At))
				break
			}
		}
		report(t, "ip-block: ipb.test pending after 1 check, tunnel after 2", errs, "")
	})

	// --- 5. rules cleared: back to direct after two checks ----------------------------------

	t.Run("5 rules cleared: sni.test and ipb.test direct after 2 checks", func(t *testing.T) {
		t0 := time.Now()
		setRules()
		hist, err := awaitRoutes(isp, map[string]route.Route{sniDom: route.Direct, ipbDom: route.Direct}, checksWithin(3))
		var errs []string
		if err != nil {
			errs = append(errs, err.Error())
		} else {
			errs = append(errs, hysteresis(sniDom, hist[sniDom], t0, route.Desync, route.Direct)...)
			errs = append(errs, hysteresis(ipbDom, hist[ipbDom], t0, route.Tunnel, route.Direct)...)
		}
		report(t, "rules cleared: sni.test and ipb.test back to direct after 2 checks", errs, "")
	})

	// --- 7. Reassemble: the known gap L-D5 --------------------------------------------------

	t.Run("7 reassemble: re.test moves to desync and stays (known gap L-D5)", func(t *testing.T) {
		// lab/PLAN.md L-D5: the analyzer probes direct only, so the checker sees TLS_RST with a
		// working SNI swap and picks desync, although a reassembling DPI would also block the
		// desync'ed hello. It never notices that desync fails, so the domain stays desync. This
		// asserts the CURRENT behaviour; a fix (a desync dialer, or monitor failures escalating
		// desync to tunnel) is a separate product decision.
		t0 := time.Now()
		setRules(fakedpi.Rule{Kind: fakedpi.Reassemble, Domains: []string{reDom}})
		hist, err := awaitRoutes(isp, map[string]route.Route{reDom: route.Desync}, checksWithin(3))
		var errs []string
		if err != nil {
			errs = append(errs, err.Error())
		} else {
			errs = append(errs, hysteresis(reDom, hist[reDom], t0, route.Direct, route.Desync)...)
			h := hist[reDom]
			at := h[len(h)-1].CheckedAt
			var after storeDecision
			if err := waitFor(checksWithin(1), "one more check of re.test", func() (bool, error) {
				d, ok, err := decision(isp, reDom)
				after = d
				return ok && d.CheckedAt.After(at), err
			}); err != nil {
				errs = append(errs, err.Error())
			} else if after.Route != route.Desync || after.Pending != nil {
				errs = append(errs, fmt.Sprintf("%s after another check: %s, want it to stay desync (current behaviour, L-D5)", reDom, fmtDec(after)))
			}
		}
		errs = append(errs, dpiReset(reDom, fakedpi.Reassemble)...)
		report(t, "reassemble: re.test moves to desync and stays", errs,
			"known gap L-D5: desync does not beat a reassembling DPI, but the checker probes direct only and keeps desync")
	})

	// --- 6. network change and restore ------------------------------------------------------

	// The status label of each network after the switches; checked in its own case so the
	// restore itself is judged separately from the label (which follows a re-identification).
	var labelErrs []string
	labelChecked := false
	t.Run("6 network change: ispb's own table, then ispa's restored", func(t *testing.T) {
		var errs []string
		deepDiag = true
		defer func() { deepDiag = false }()
		l.UseISP(other)
		// Defaults first (ispb has no decisions), then ispb's own decisions (no TSPU rules there).
		if err := waitFor(identifyWithin+10*time.Second, "the defaults table after the switch to ispb", func() (bool, error) {
			st, err := status()
			return err == nil && defaults(st), err
		}); err != nil {
			errs = append(errs, err.Error())
		}
		want := map[string]route.Route{}
		for _, d := range cands {
			want[d] = route.Direct
		}
		if _, err := awaitRoutes(other, want, identifyWithin+checksWithin(2)); err != nil {
			errs = append(errs, err.Error())
		}
		if err := waitFor(10*time.Second, "status shows ispb's table (all direct)", func() (bool, error) {
			st, err := status()
			return err == nil && st.Routes[route.Direct] == len(cands), err
		}); err != nil {
			errs = append(errs, err.Error())
		}
		f, err := store()
		if err != nil {
			errs = append(errs, err.Error())
			report(t, "network change: ispb's own table, then ispa's restored", errs, "")
			return
		}
		aID, aNet := netOf(f, isp)
		bID, _ := netOf(f, other)
		if aNet == nil || bID == "" || aID == bID {
			errs = append(errs, fmt.Sprintf("want separate networks for %s and %s, got %q and %q", isp, other, aID, bID))
			report(t, "network change: ispb's own table, then ispa's restored", errs, "")
			return
		}
		if err := awaitLabel(other); err != nil {
			labelErrs = append(labelErrs, "on ispb: "+err.Error())
		}
		if want := s.networkIDs(other)[0]; bID != want {
			labelErrs = append(labelErrs, fmt.Sprintf("ispb stored under %q, want %q", bID, want))
		}

		// ispa cannot be checked while the client is on ispb: its stored state is final here.
		ispaCounts := countRoutes(aNet)
		var ispaChecked time.Time
		for _, d := range aNet.Decisions {
			if d.CheckedAt.After(ispaChecked) {
				ispaChecked = d.CheckedAt
			}
		}
		if ispaCounts[route.Desync] == 0 {
			errs = append(errs, fmt.Sprintf("ispa's stored table %v has no desync entry; the restore is not distinguishable from ispb's table", ispaCounts))
		}

		l.UseISP(isp)
		// Restored immediately: ispa's table shows up while no new ispa check has finished
		// (decisions.json read after the status answer still has the old checked_at).
		var restored, checkedFirst bool
		err = waitFor(identifyWithin+10*time.Second, "ispa's stored table back", func() (bool, error) {
			st, err := status()
			if err != nil {
				return false, err
			}
			if !sameCounts(st, ispaCounts) {
				return false, nil
			}
			f, err := store()
			if err != nil {
				return false, err
			}
			_, n := netOf(f, isp)
			if n == nil {
				return false, errors.New("ispa's network vanished from decisions.json")
			}
			for _, d := range n.Decisions {
				if d.CheckedAt.After(ispaChecked) {
					checkedFirst = true
				}
			}
			restored = true
			return true, nil
		})
		switch {
		case err != nil:
			errs = append(errs, err.Error())
		case checkedFirst:
			errs = append(errs, "ispa's table was first seen after a new ispa check had finished: the restore from the store was not observable")
		case !restored:
			errs = append(errs, "ispa's table not restored")
		}
		if err := awaitLabel(isp); err != nil {
			labelErrs = append(labelErrs, "back on ispa: "+err.Error())
		}
		if want := s.networkIDs(isp)[0]; aID != want {
			labelErrs = append(labelErrs, fmt.Sprintf("ispa stored under %q, want %q", aID, want))
		}
		labelChecked = true
		report(t, "network change: ispb's own table, then ispa's restored", errs, "")
	})

	t.Run("6 status names the network by ASN (AS64501, then AS64500)", func(t *testing.T) {
		errs := labelErrs
		if !labelChecked {
			errs = append(errs, "not checked: the network change case stopped early")
		}
		report(t, "status names the network by ASN after the switches", errs, "")
	})

	t.Run("clean shutdown on SIGTERM", func(t *testing.T) {
		var errs []string
		code, err := s.stop(30 * time.Second)
		if err != nil || code != 0 {
			errs = append(errs, fmt.Sprintf("exit code %d (%v), want 0; output:\n%s", code, err, tail([]byte(s.output()), 2048)))
		}
		report(t, "clean shutdown on SIGTERM", errs, "")
	})
}
