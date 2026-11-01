package pc

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/nikoejento/obhod/internal/lib/analyzer"
	"github.com/nikoejento/obhod/internal/lib/checker"
	"github.com/nikoejento/obhod/internal/lib/clock"
	"github.com/nikoejento/obhod/internal/lib/route"
	"github.com/nikoejento/obhod/internal/pc/sites"
)

// idleRunner never probes anything: the network stays unidentified, so the real
// checker only builds tables from the lists, the defaults and the user rules.
type idleRunner struct{}

func (idleRunner) Run(context.Context, analyzer.Batch) (analyzer.Report, error) {
	return analyzer.Report{}, errors.New("no probes in this test")
}

// useRealChecker replaces the harness's fake checker with the real one over
// small list files in a temporary directory: listed.example is a candidate,
// blocked.example a block entry.
func useRealChecker(t *testing.T, h *harness) {
	t.Helper()
	dir := t.TempDir()
	w := func(name, body string) []string {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
		return []string{p}
	}
	cfg := checker.Config{Lists: checker.ListsConfig{
		Candidates: w("c.txt", "listed.example\n"),
		Block:      w("b.txt", "blocked.example\n"),
	}}
	cfg.Schedule.Interval = 1000 * time.Hour
	ck, err := checker.New(cfg, checker.Deps{
		Runner: idleRunner{}, Clock: clock.NewFake(time.Unix(1_000_000, 0)), DataDir: t.TempDir(),
		Network: checker.NewFakeNetwork(checker.Network{}),
	})
	if err != nil {
		t.Fatal(err)
	}
	h.d.checker = ck
}

func (h *harness) routeOf(domain string) (RouteInfo, bool) {
	for _, r := range h.app.Routes() {
		if r.Domain == domain {
			return r, true
		}
	}
	return RouteInfo{}, false
}

func (h *harness) waitRouteInfo(domain string, r route.Route, src route.Source) {
	h.t.Helper()
	waitUntil(h.t, domain+" as "+string(r)+"/"+string(src), func() bool {
		ri, ok := h.routeOf(domain)
		return ok && ri.Route == r && ri.Source == src
	})
}

func TestSiteRulesChangeTheTableAndApplyWhileConnected(t *testing.T) {
	h := newHarness(t, baseTable(t), WithUI(true))
	useRealChecker(t, h)
	h.start()
	h.waitRunning()
	if err := h.app.Connect(context.Background()); err != nil {
		t.Fatal(err)
	}
	h.fc.Advance(time.Minute) // past the minimum gap between sing-box restarts
	h.waitRouteInfo("listed.example", route.Tunnel, route.SourceDefault)
	h.waitRouteInfo("blocked.example", route.Block, route.SourceStatic)
	if _, ok := h.routeOf("new.example"); ok {
		t.Fatal("new.example is in the table before any rule")
	}
	waitUntil(t, "reload loop idle", func() bool { return h.fc.Waiters() == 0 })
	h.log.clear()
	events, cancel := h.app.Subscribe()
	defer cancel()

	// 1. A rule for a domain in no list adds it to the table, source "user".
	rule, created, err := h.app.SetSite(" New.Example. ", "direct")
	if err != nil || !created || rule.Domain != "new.example" || rule.Action != sites.Direct {
		t.Fatalf("SetSite = %+v %v %v", rule, created, err)
	}
	h.waitRouteInfo("new.example", route.Direct, route.SourceUser)
	ev := next(t, events, EventRoutes)
	if len(ev.Changes) != 1 || ev.Changes[0].Domain != "new.example" || ev.Changes[0].From != "" || ev.Changes[0].To != route.Direct {
		t.Fatalf("routes event = %+v", ev.Changes)
	}
	// Live apply: the debounce fires, sing-box restarts with the new config, no reconnect.
	waitUntil(t, "debounce timer", func() bool { return h.fc.Waiters() >= 1 })
	h.fc.Advance(reloadDebounce)
	h.waitOp("restart sing-box")
	if !strings.Contains(string(h.singBoxFile()), "new.example") {
		t.Fatal("sing-box config has no new.example")
	}
	if h.app.State() != StateConnected {
		t.Fatalf("state = %s", h.app.State())
	}

	// 2. A rule beats a block-list entry of the same domain and a candidate's default.
	if _, _, err := h.app.SetSite("blocked.example", "tunnel"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := h.app.SetSite("listed.example", "block"); err != nil {
		t.Fatal(err)
	}
	h.waitRouteInfo("blocked.example", route.Tunnel, route.SourceUser)
	h.waitRouteInfo("listed.example", route.Block, route.SourceUser)

	// 3. Update keeps one rule per domain; list is sorted.
	if _, created, err := h.app.SetSite("new.example", "desync"); err != nil || created {
		t.Fatalf("update = %v %v", created, err)
	}
	list, err := h.app.Sites()
	if err != nil || len(list) != 3 || list[0].Domain != "blocked.example" || list[2].Domain != "new.example" || list[2].Action != sites.Desync {
		t.Fatalf("Sites = %+v %v", list, err)
	}

	// 4. Removal: list entries return to automatic, the unlisted domain leaves the table.
	for _, d := range []string{"blocked.example", "listed.example", "new.example"} {
		if err := h.app.DeleteSite(d); err != nil {
			t.Fatal(err)
		}
	}
	h.waitRouteInfo("blocked.example", route.Block, route.SourceStatic)
	h.waitRouteInfo("listed.example", route.Tunnel, route.SourceDefault)
	waitUntil(t, "new.example gone", func() bool { _, ok := h.routeOf("new.example"); return !ok })
	if err := h.app.DeleteSite("new.example"); !errors.Is(err, ErrSiteNotFound) {
		t.Fatalf("second delete = %v", err)
	}
	if list, _ := h.app.Sites(); len(list) != 0 {
		t.Fatalf("rules left: %+v", list)
	}
}

func TestSiteCallErrors(t *testing.T) {
	h := newHarness(t, baseTable(t), WithUI(true))
	if _, _, err := h.app.SetSite("https://example.com/x", "direct"); !errors.Is(err, ErrSiteDomain) {
		t.Errorf("URL = %v", err)
	}
	if _, _, err := h.app.SetSite("example.com", "proxy"); !errors.Is(err, ErrSiteAction) {
		t.Errorf("proxy = %v", err)
	}
	if err := h.app.DeleteSite("1.2.3.4"); !errors.Is(err, ErrSiteDomain) {
		t.Errorf("IP delete = %v", err)
	}
	if len(h.chk.overrides) != 0 {
		t.Errorf("a rejected call reached the checker: %+v", h.chk.overrides)
	}
	for i := 0; i < sites.MaxRules; i++ {
		if _, _, err := h.app.SetSite("d"+strings.Repeat("x", i%5)+string(rune('a'+i%26))+string(rune('a'+i/26%26))+string(rune('a'+i/676))+".example", "direct"); err != nil {
			t.Fatalf("rule %d: %v", i, err)
		}
	}
	if _, _, err := h.app.SetSite("over.example", "direct"); !errors.Is(err, ErrSiteLimit) {
		t.Errorf("limit = %v", err)
	}
	if got := len(h.chk.overrides); got != sites.MaxRules {
		t.Errorf("checker has %d rules, want %d", got, sites.MaxRules)
	}
}

func TestStoredSiteRulesAreInTheFirstTable(t *testing.T) {
	h := newHarness(t, baseTable(t), WithUI(true))
	useRealChecker(t, h)
	st, err := sites.Open(h.p.Data, sites.OSFS())
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := st.Set("kept.example", "tunnel"); err != nil {
		t.Fatal(err)
	}
	h.start()
	h.waitRunning()
	h.waitRouteInfo("kept.example", route.Tunnel, route.SourceUser)
	if list, err := h.app.Sites(); err != nil || len(list) != 1 || list[0].Domain != "kept.example" {
		t.Fatalf("Sites = %+v %v", list, err)
	}
}

func TestCorruptSiteRulesDoNotStopTheStart(t *testing.T) {
	h := newHarness(t, baseTable(t), WithUI(true))
	useRealChecker(t, h)
	if err := os.MkdirAll(h.p.Data, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(h.p.Data, sites.FileName), []byte("{{{"), 0o600); err != nil {
		t.Fatal(err)
	}
	h.start()
	h.waitRunning()
	if list, err := h.app.Sites(); err != nil || len(list) != 0 {
		t.Fatalf("Sites = %+v %v", list, err)
	}
	if _, err := os.Stat(filepath.Join(h.p.Data, sites.CorruptName)); err != nil {
		t.Fatalf("corrupt file not kept: %v", err)
	}
}
