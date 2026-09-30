package decide

import (
	"testing"
	"time"

	"github.com/nikoejento/obhod/internal/lib/analyzer"
	"github.com/nikoejento/obhod/internal/lib/checker/lib/model"
	"github.com/nikoejento/obhod/internal/lib/route"
)

var t0 = time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)

func res(rt route.Route, v string) analyzer.ProbeResult {
	return analyzer.ProbeResult{Route: rt, Verdict: analyzer.Verdict(v)}
}

func direct(v string) map[route.Route]analyzer.ProbeResult {
	return map[route.Route]analyzer.ProbeResult{route.Direct: res(route.Direct, v)}
}

func TestCandidateDirectOnly(t *testing.T) {
	cases := []struct {
		verdict string
		want    route.Route // "" = no conclusion
	}{
		{"OK", route.Direct},
		{"OK_REDIRECT", route.Direct},
		{"DNS_POISONED", route.Direct},
		{"DNS_FAIL", ""},
		{"TCP_TIMEOUT", route.Tunnel},
		{"TCP_REFUSED", route.Tunnel},
		{"TLS_MITM", route.Tunnel},
		{"HTTP_STUB", route.Tunnel},
		{"STALL_16_20", route.Tunnel},
		{"TLS_RST", route.Tunnel}, // no SNI-swap evidence: the address itself looks blocked
		{"TLS_TIMEOUT", route.Tunnel},
		{"TLS_ALERT", route.Tunnel},
		{"TLS_SPOOF", route.Tunnel},
		{"TLS_EOF", route.Tunnel},
		{"HTTP_RST", ""},
		{"READ_TIMEOUT", ""},
		{"CONN_LIMITED", ""},
		{"LOCAL_ERROR", ""},
		{"UNKNOWN", ""},
	}
	for _, c := range cases {
		got, ok, reason := Candidate(direct(c.verdict), nil)
		if ok != (c.want != "") || got != c.want {
			t.Errorf("%s: got (%q,%v), want %q", c.verdict, got, ok, c.want)
		}
		if reason == "" {
			t.Errorf("%s: empty reason", c.verdict)
		}
	}
}

func TestCandidateFollowsSuggestRoute(t *testing.T) {
	r := res(route.Direct, "TLS_RST")
	r.Evidence.SNISwap = &analyzer.SNISwapResult{}
	want, wantOK := analyzer.SuggestRoute(r)
	got, ok, _ := Candidate(map[route.Route]analyzer.ProbeResult{route.Direct: r}, nil)
	if got != want || ok != wantOK {
		t.Errorf("got (%q,%v), want (%q,%v)", got, ok, want, wantOK)
	}
}

func TestCandidateFull(t *testing.T) {
	full := func(d, s, tn string) map[route.Route]analyzer.ProbeResult {
		return map[route.Route]analyzer.ProbeResult{
			route.Direct: res(route.Direct, d), route.Desync: res(route.Desync, s), route.Tunnel: res(route.Tunnel, tn),
		}
	}
	cases := []struct {
		name  string
		in    map[route.Route]analyzer.ProbeResult
		order []route.Route
		want  route.Route
	}{
		{"direct first", full("OK", "OK", "OK"), nil, route.Direct},
		{"redirect counts", full("OK_REDIRECT", "TLS_RST", "OK"), nil, route.Direct},
		{"desync over tunnel", full("TLS_RST", "OK", "OK"), nil, route.Desync},
		{"tunnel only", full("TLS_RST", "TLS_RST", "OK"), nil, route.Tunnel},
		{"custom order", full("OK", "OK", "OK"), []route.Route{route.Tunnel, route.Direct}, route.Tunnel},
		{"site down", full("TLS_RST", "TLS_RST", "TCP_TIMEOUT"), nil, ""},
		{"errors only", full("LOCAL_ERROR", "UNKNOWN", "LOCAL_ERROR"), nil, ""},
	}
	for _, c := range cases {
		got, ok, _ := Candidate(c.in, c.order)
		if got != c.want || ok != (c.want != "") {
			t.Errorf("%s: got (%q,%v), want %q", c.name, got, ok, c.want)
		}
	}
	if _, ok, _ := Candidate(nil, nil); ok {
		t.Error("nil results must not conclude")
	}
}

func TestNeverEmitsStaticRoutes(t *testing.T) {
	for _, rt := range []route.Route{route.Proxy, route.Block} {
		results := map[route.Route]analyzer.ProbeResult{
			route.Direct: res(route.Direct, "TLS_RST"), route.Tunnel: res(route.Tunnel, "TLS_RST"), rt: res(rt, "OK"),
		}
		if got, ok, _ := Candidate(results, []route.Route{rt}); ok {
			t.Errorf("%s: Candidate returned %q", rt, got)
		}
		in := Input{Domain: "a.com", Now: t0, Results: results, Policy: Policy{Order: []route.Route{rt}}}
		if out := Decide(in); out.Decision.Route != "" || out.Changed {
			t.Errorf("%s: emitted %+v", rt, out)
		}
		cur := model.Decision{Domain: "a.com", Route: route.Direct}
		in.Current = &cur
		if out := Decide(in); out.Decision.Route != route.Direct || out.Changed {
			t.Errorf("%s: current changed: %+v", rt, out)
		}
		// A lone non-direct static probe must not conclude either.
		if _, ok, _ := Candidate(map[route.Route]analyzer.ProbeResult{rt: res(rt, "OK")}, nil); ok {
			t.Errorf("%s: single result concluded", rt)
		}
	}
}

func TestFirstDecision(t *testing.T) {
	out := Decide(Input{Domain: "a.com", Results: direct("TLS_RST"), Now: t0})
	d := out.Decision
	if !out.Changed || d.Route != route.Tunnel || d.Source != route.SourceChecked || d.Pending != nil {
		t.Fatalf("unexpected: %+v", out)
	}
	if d.Domain != "a.com" || !d.DecidedAt.Equal(t0) || !d.CheckedAt.Equal(t0) || !d.ExpiresAt.Equal(t0.Add(24*time.Hour)) {
		t.Errorf("times/domain wrong: %+v", d)
	}
	if d.Last[route.Direct] != "TLS_RST" {
		t.Errorf("Last = %v", d.Last)
	}
}

func TestNoConclusion(t *testing.T) {
	out := Decide(Input{Domain: "a.com", Results: direct("LOCAL_ERROR"), Now: t0})
	if out.Changed || out.Decision.Route != "" || out.Reason == "" {
		t.Errorf("nil current: %+v", out)
	}
	cur := &model.Decision{Domain: "a.com", Route: route.Tunnel, CheckedAt: t0.Add(-time.Hour), Pending: &model.Pending{Route: route.Direct, Count: 1}}
	out = Decide(Input{Domain: "a.com", Current: cur, Results: direct("UNKNOWN"), Now: t0})
	if out.Changed || out.Decision.Route != route.Tunnel || !out.Decision.CheckedAt.Equal(cur.CheckedAt) || out.Decision.Pending == nil {
		t.Errorf("current must be unchanged: %+v", out)
	}
	out.Decision.Pending.Count = 99
	if cur.Pending.Count != 1 {
		t.Error("output shares Pending with input")
	}
}

// run feeds direct verdicts through Decide, one minute apart, and returns the route after each.
func run(p Policy, start *model.Decision, verdicts ...string) []route.Route {
	cur := start
	var routes []route.Route
	now := t0
	for _, v := range verdicts {
		out := Decide(Input{Domain: "a.com", Current: cur, Results: direct(v), Now: now, Policy: p})
		d := out.Decision
		cur = &d
		routes = append(routes, d.Route)
		now = now.Add(time.Minute)
	}
	return routes
}

func TestHysteresis(t *testing.T) {
	const back, block = "OK", "TLS_RST" // OK → direct, TLS_RST → tunnel
	cases := []struct {
		name string
		p    Policy
		seq  []string
		want []route.Route
	}{
		{"flip-flop never switches", Policy{}, []string{block, back, block, back, block, back},
			[]route.Route{"direct", "direct", "direct", "direct", "direct", "direct"}},
		{"two agreeing switch", Policy{}, []string{back, block, block}, []route.Route{"direct", "direct", "tunnel"}},
		{"confirmations 1", Policy{Confirmations: 1}, []string{block}, []route.Route{"tunnel"}},
		{"confirmations 3", Policy{Confirmations: 3}, []string{block, block, block}, []route.Route{"direct", "direct", "tunnel"}},
		{"no conclusion keeps pending", Policy{}, []string{block, "LOCAL_ERROR", block}, []route.Route{"direct", "direct", "tunnel"}},
	}
	for _, c := range cases {
		start := &model.Decision{Domain: "a.com", Route: route.Direct, Source: route.SourceChecked}
		got := run(c.p, start, c.seq...)
		for i := range got {
			if got[i] != c.want[i] {
				t.Errorf("%s: step %d = %s, want %s (all %v)", c.name, i, got[i], c.want[i], got)
				break
			}
		}
	}
}

func TestStalePendingReplaced(t *testing.T) {
	cur := &model.Decision{Domain: "a.com", Route: route.Direct, Pending: &model.Pending{Route: route.Desync, Count: 5, Since: t0.Add(-time.Hour)}}
	out := Decide(Input{Domain: "a.com", Current: cur, Results: direct("TLS_RST"), Now: t0}) // candidate: tunnel
	p := out.Decision.Pending
	if out.Changed || p == nil || p.Route != route.Tunnel || p.Count != 1 || !p.Since.Equal(t0) {
		t.Errorf("stale pending not replaced: %+v", out.Decision)
	}
}

func TestTTLRefreshWithoutChange(t *testing.T) {
	decided := t0.Add(-48 * time.Hour)
	cur := &model.Decision{Domain: "a.com", Route: route.Direct, DecidedAt: decided, CheckedAt: decided, ExpiresAt: decided.Add(24 * time.Hour),
		Pending: &model.Pending{Route: route.Tunnel, Count: 1}}
	out := Decide(Input{Domain: "a.com", Current: cur, Results: direct("OK"), Now: t0, Policy: Policy{TTL: time.Hour}})
	d := out.Decision
	if out.Changed || d.Route != route.Direct || d.Pending != nil {
		t.Errorf("unexpected: %+v", out)
	}
	if !d.DecidedAt.Equal(decided) || !d.CheckedAt.Equal(t0) || !d.ExpiresAt.Equal(t0.Add(time.Hour)) {
		t.Errorf("times: %+v", d)
	}
	if cur.Pending == nil {
		t.Error("input mutated")
	}
}

func TestSwitchSetsDecidedAt(t *testing.T) {
	cur := &model.Decision{Domain: "a.com", Route: route.Direct, DecidedAt: t0.Add(-time.Hour), Pending: &model.Pending{Route: route.Tunnel, Count: 1}}
	out := Decide(Input{Domain: "a.com", Current: cur, Results: direct("TLS_RST"), Now: t0})
	if !out.Changed || out.Decision.Route != route.Tunnel || !out.Decision.DecidedAt.Equal(t0) || out.Decision.Pending != nil {
		t.Errorf("unexpected: %+v", out)
	}
}
