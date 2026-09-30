// Package decide turns probe results and the current decision into a new decision, with hysteresis (pure).
package decide

import (
	"fmt"
	"time"

	"github.com/nikoejento/obhod/internal/lib/analyzer"
	"github.com/nikoejento/obhod/internal/lib/checker/lib/model"
	"github.com/nikoejento/obhod/internal/lib/route"
)

// Policy tunes how results become decisions. Zero fields take their defaults.
type Policy struct {
	Confirmations int           // results needed before switching; default 2
	TTL           time.Duration // default 24h
	Order         []route.Route // preference among working routes; default direct, desync, tunnel
}

func (p Policy) withDefaults() Policy {
	if p.Confirmations <= 0 {
		p.Confirmations = 2
	}
	if p.TTL <= 0 {
		p.TTL = 24 * time.Hour
	}
	if len(p.Order) == 0 {
		p.Order = []route.Route{route.Direct, route.Desync, route.Tunnel}
	}
	return p
}

// Input is everything Decide needs for one domain.
type Input struct {
	Domain  string
	Current *model.Decision                      // nil if none
	Results map[route.Route]analyzer.ProbeResult // routes probed this run
	Now     time.Time
	Policy  Policy
}

// Output is the new decision for a domain.
type Output struct {
	Decision model.Decision
	Changed  bool   // Route differs from Current.Route
	Reason   string // human-readable, for logs/status
}

// Candidate computes the route this run points to, ignoring hysteresis.
// ok=false → no conclusion (e.g. only LOCAL_ERROR / UNKNOWN, or site down).
// It never returns a route that is not Checkable (proxy and block are static only).
func Candidate(results map[route.Route]analyzer.ProbeResult, order []route.Route) (r route.Route, ok bool, reason string) {
	if len(results) == 0 {
		return "", false, "no results"
	}
	if len(order) == 0 {
		order = Policy{}.withDefaults().Order
	}
	if len(results) == 1 {
		for rt, res := range results {
			return single(rt, res)
		}
	}
	for _, rt := range order {
		res, probed := results[rt]
		if !probed || !isOK(res.Verdict) {
			continue
		}
		if !rt.Checkable() {
			return "", false, fmt.Sprintf("route %q is not decidable", rt)
		}
		return rt, true, fmt.Sprintf("%s works (%s)", rt, res.Verdict)
	}
	return "", false, "site down: no probed route works"
}

func single(rt route.Route, res analyzer.ProbeResult) (route.Route, bool, string) {
	if rt == route.Direct {
		s, ok := analyzer.SuggestRoute(res)
		if !ok || !s.Checkable() {
			return "", false, fmt.Sprintf("no conclusion from %s", res.Verdict)
		}
		return s, true, fmt.Sprintf("direct probe %s suggests %s", res.Verdict, s)
	}
	if isOK(res.Verdict) && rt.Checkable() {
		return rt, true, fmt.Sprintf("%s works (%s)", rt, res.Verdict)
	}
	return "", false, fmt.Sprintf("no conclusion from %s over %s", res.Verdict, rt)
}

// The analyzer does not re-export its verdict constants, so the two "reached the site" ones are spelled here.
const (
	verdictOK         analyzer.Verdict = "OK"
	verdictOKRedirect analyzer.Verdict = "OK_REDIRECT"
)

func isOK(v analyzer.Verdict) bool { return v == verdictOK || v == verdictOKRedirect }

// Decide computes the new decision for in.Domain.
//
// No conclusion keeps Current untouched (a nil Current yields the zero Decision, which the caller must not
// store). Otherwise CheckedAt, ExpiresAt and Last are refreshed, and the route changes only after
// Policy.Confirmations agreeing runs (immediately when there is no Current decision).
func Decide(in Input) Output {
	p := in.Policy.withDefaults()
	cand, ok, reason := Candidate(in.Results, p.Order)
	if ok && !cand.Checkable() { // guard: never emit proxy/block
		ok, reason = false, fmt.Sprintf("route %q is not decidable", cand)
	}
	if !ok {
		if in.Current == nil {
			return Output{Reason: reason}
		}
		return Output{Decision: in.Current.Clone(), Reason: reason}
	}

	last := make(map[route.Route]analyzer.Verdict, len(in.Results))
	for rt, res := range in.Results {
		last[rt] = res.Verdict
	}
	base := func(d model.Decision) model.Decision {
		d.Domain = in.Domain
		d.CheckedAt = in.Now
		d.ExpiresAt = in.Now.Add(p.TTL)
		d.Last = last
		return d
	}

	if in.Current == nil {
		d := base(model.Decision{Route: cand, Source: route.SourceChecked, DecidedAt: in.Now})
		return Output{Decision: d, Changed: true, Reason: "first decision: " + reason}
	}

	d := base(in.Current.Clone())
	if d.Domain == "" {
		d.Domain = in.Current.Domain
	}
	if cand == d.Route {
		d.Pending = nil
		return Output{Decision: d, Reason: "confirmed " + reason}
	}
	if d.Pending != nil && d.Pending.Route == cand {
		d.Pending.Count++
	} else {
		d.Pending = &model.Pending{Route: cand, Count: 1, Since: in.Now}
	}
	if d.Pending.Count >= p.Confirmations {
		from := d.Route
		d.Route, d.Source, d.DecidedAt, d.Pending = cand, route.SourceChecked, in.Now, nil
		return Output{Decision: d, Changed: true, Reason: fmt.Sprintf("switch %s → %s: %s", from, cand, reason)}
	}
	return Output{Decision: d, Reason: fmt.Sprintf("pending %s %d/%d: %s", cand, d.Pending.Count, p.Confirmations, reason)}
}
