package autodesync

import (
	"context"
	"errors"
	"math/rand/v2"
	"time"

	"github.com/nikoejento/obhod/internal/lib/clock"
)

// Errors an Applier or Prober returns.
var (
	// ErrSkip: this preset cannot be tried here (for example its payload files are missing). The search goes on
	// with the next one; a skipped preset does not count against Config.MaxPresets.
	ErrSkip = errors.New("autodesync: preset skipped")
	// ErrNoInternet: the probes' own internet check failed, so the result says nothing about the preset.
	ErrNoInternet = errors.New("autodesync: the internet check failed")
)

// Applier puts a preset to work for exactly these domains: the desync engine runs with the preset and a
// hostlist of the domains (a probe of a domain that is not in the hostlist cannot measure the desync).
// It returns when the engine has been (re)started. The app implements it; see internal/pc.
type Applier interface {
	Apply(ctx context.Context, preset string, domains []string) error
}

// Prober probes the domains through the real direct path (the engine mangles the physical path
// transparently) and reports which of them were reached (the analyzer verdict OK). Concurrency and per-probe
// timeouts are the Prober's business and stay within Config.
type Prober interface {
	Probe(ctx context.Context, domains []string) (reached map[string]bool, err error)
}

// Phase names a Progress report.
type Phase string

// The phases.
const (
	PhaseStart  Phase = "start"  // the search begins
	PhaseTrying Phase = "trying" // preset Index of Total is applied next
)

// Progress is reported to Request.Progress. It holds no domain.
type Progress struct {
	Phase  Phase
	Index  int // 1-based position among the presets this search tries (PhaseTrying)
	Total  int // presets the search may try at most
	Preset string
}

// Request is one search.
type Request struct {
	Domains  []string // probed in this order; cut to Config.MaxDomains
	Order    []string // preset ids, best candidate first; the search tries at most Config.MaxPresets of them
	Progress func(Progress)
}

// Attempt is the result of one preset.
type Attempt struct {
	Preset       string
	Fixed, Total int
	FixedDomains []string
	Skipped      bool // ErrSkip
	Unusable     bool // the probes could not tell (no internet check, a probe error)
}

// Outcome is how a search ended.
type Outcome string

// The outcomes.
const (
	OutcomeChosen       Outcome = "chosen"       // a preset fixes some or all of the domains
	OutcomeNone         Outcome = "none"         // presets were tried and none fixes a domain
	OutcomeInconclusive Outcome = "inconclusive" // no preset gave a usable measurement
	OutcomeCanceled     Outcome = "canceled"
	OutcomeError        Outcome = "error" // the engine could not be put to work
)

// Result is what Run returns.
type Result struct {
	Outcome Outcome
	Chosen  string   // OutcomeChosen: the best preset
	Fixed   []string // domains Chosen fixes
	Failed  []string // domains no tried preset fixes (OutcomeChosen with a partial fix, and OutcomeNone)
	Tried   []Attempt
	Domains []string // what was probed (after the MaxDomains cut)
	// Stopped says why the loop ended: "all fixed", "budget", "presets" (MaxPresets reached), "exhausted"
	// (the order ran out), "canceled" or "error".
	Stopped string
	Err     error // OutcomeError
}

// Searcher runs searches. It holds no state between runs; the app runs one at a time.
type Searcher struct {
	cfg    Config
	clk    clock.Clock
	apply  Applier
	probe  Prober
	jitter func(lo, hi time.Duration) time.Duration
}

// Option customizes New.
type Option func(*Searcher)

// WithJitter replaces the random draw of the pause between presets (tests).
func WithJitter(f func(lo, hi time.Duration) time.Duration) Option {
	return func(s *Searcher) { s.jitter = f }
}

// New returns a Searcher. cfg's zero fields take their defaults.
func New(cfg Config, clk clock.Clock, a Applier, p Prober, opts ...Option) *Searcher {
	s := &Searcher{cfg: cfg.WithDefaults(), clk: clk, apply: a, probe: p, jitter: uniform}
	if s.cfg.Concurrency > MaxConcurrency {
		s.cfg.Concurrency = MaxConcurrency
	}
	for _, o := range opts {
		o(s)
	}
	return s
}

func uniform(lo, hi time.Duration) time.Duration {
	if hi <= lo {
		return lo
	}
	return lo + rand.N(hi-lo)
}

// Run tries the presets in req.Order one after the other and stops at the first that fixes every domain, when
// Config.MaxPresets presets were tried, or when Config.Budget has passed between two presets. It returns the
// preset that fixed the most domains (the earlier one on a tie). It never applies the winner: the caller puts
// the final state in place. A cancelled ctx ends it at once with OutcomeCanceled.
func (s *Searcher) Run(ctx context.Context, req Request) Result {
	domains := cut(req.Domains, s.cfg.MaxDomains)
	order := cut(req.Order, 0)
	res := Result{Domains: domains}
	if len(domains) == 0 || len(order) == 0 {
		res.Outcome, res.Stopped = OutcomeInconclusive, "exhausted"
		return res
	}
	total := min(s.cfg.MaxPresets, len(order))
	report := func(p Progress) {
		if req.Progress != nil {
			req.Progress(p)
		}
	}
	report(Progress{Phase: PhaseStart, Total: total})
	canceled := func() Result {
		res.Outcome, res.Stopped = OutcomeCanceled, "canceled"
		return res
	}

	start := s.clk.Now()
	tried, usable := 0, 0
	best := -1 // index into res.Tried
	res.Stopped = "exhausted"
loop:
	for _, preset := range order {
		if ctx.Err() != nil {
			return canceled()
		}
		if tried >= s.cfg.MaxPresets {
			res.Stopped = "presets"
			break
		}
		if tried > 0 {
			if s.clk.Now().Sub(start) >= s.cfg.Budget {
				res.Stopped = "budget"
				break
			}
			if err := s.clk.Sleep(ctx, s.jitter(s.cfg.Pause[0], s.cfg.Pause[1])); err != nil {
				return canceled()
			}
			if s.clk.Now().Sub(start) >= s.cfg.Budget {
				res.Stopped = "budget"
				break
			}
		}
		report(Progress{Phase: PhaseTrying, Index: tried + 1, Total: total, Preset: preset})
		err := s.apply.Apply(ctx, preset, domains)
		switch {
		case ctx.Err() != nil:
			return canceled()
		case errors.Is(err, ErrSkip):
			res.Tried = append(res.Tried, Attempt{Preset: preset, Total: len(domains), Skipped: true})
			continue
		case err != nil:
			res.Outcome, res.Err, res.Stopped = OutcomeError, err, "error"
			return res
		}
		tried++
		if err := s.clk.Sleep(ctx, s.cfg.Settle); err != nil {
			return canceled()
		}
		reached, err := s.probe.Probe(ctx, domains)
		if ctx.Err() != nil {
			return canceled()
		}
		att := Attempt{Preset: preset, Total: len(domains)}
		if err != nil {
			att.Unusable = true
			res.Tried = append(res.Tried, att)
			continue
		}
		usable++
		for _, d := range domains {
			if reached[d] {
				att.FixedDomains = append(att.FixedDomains, d)
			}
		}
		att.Fixed = len(att.FixedDomains)
		res.Tried = append(res.Tried, att)
		i := len(res.Tried) - 1
		if att.Fixed == att.Total {
			best = i
			res.Stopped = "all fixed"
			break loop
		}
		if att.Fixed > 0 && (best < 0 || att.Fixed > res.Tried[best].Fixed) {
			best = i
		}
	}
	switch {
	case best >= 0:
		b := res.Tried[best]
		res.Outcome, res.Chosen, res.Fixed = OutcomeChosen, b.Preset, append([]string(nil), b.FixedDomains...)
		fixed := map[string]bool{}
		for _, d := range res.Fixed {
			fixed[d] = true
		}
		for _, d := range domains {
			if !fixed[d] {
				res.Failed = append(res.Failed, d)
			}
		}
	case usable == 0:
		res.Outcome = OutcomeInconclusive
	default:
		res.Outcome, res.Failed = OutcomeNone, append([]string(nil), domains...)
	}
	return res
}

// cut removes duplicates and empty entries, keeps the order and stops after max entries (max <= 0: no limit).
func cut(in []string, max int) []string {
	seen := map[string]bool{}
	var out []string
	for _, s := range in {
		if s == "" || seen[s] {
			continue
		}
		seen[s] = true
		out = append(out, s)
		if max > 0 && len(out) == max {
			break
		}
	}
	return out
}
