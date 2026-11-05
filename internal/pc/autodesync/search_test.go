package autodesync

import (
	"context"
	"errors"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/nikoejento/obhod/internal/lib/clock"
)

// stepClock is a fake clock whose Sleep advances time at once and records the pause.
type stepClock struct {
	*clock.Fake
	mu     sync.Mutex
	sleeps []time.Duration
}

func newStepClock() *stepClock { return &stepClock{Fake: clock.NewFake(time.Unix(1_000_000, 0))} }

func (c *stepClock) Sleep(ctx context.Context, d time.Duration) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	c.mu.Lock()
	c.sleeps = append(c.sleeps, d)
	c.mu.Unlock()
	c.Advance(d)
	return ctx.Err()
}

// script is a fake Applier and Prober: Apply records the preset, Probe answers from the table of the
// preset that was applied last.
type script struct {
	mu        sync.Mutex
	cur       string
	applied   []string
	hostlists [][]string
	fixes     map[string][]string // preset -> domains reached
	applyErr  map[string]error
	probeErr  map[string]error
	onProbe   func(preset string)
}

func (s *script) Apply(_ context.Context, preset string, domains []string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.applyErr[preset]; err != nil {
		return err
	}
	s.cur = preset
	s.applied = append(s.applied, preset)
	s.hostlists = append(s.hostlists, append([]string(nil), domains...))
	return nil
}

func (s *script) Probe(_ context.Context, domains []string) (map[string]bool, error) {
	s.mu.Lock()
	cur := s.cur
	err := s.probeErr[cur]
	hook := s.onProbe
	out := map[string]bool{}
	for _, d := range s.fixes[cur] {
		out[d] = true
	}
	s.mu.Unlock()
	if hook != nil {
		hook(cur)
	}
	return out, err
}

func zeroJitter(lo, _ time.Duration) time.Duration { return lo }

func newSearcher(cfg Config, s *script) (*Searcher, *stepClock) {
	clk := newStepClock()
	return New(cfg, clk, s, s, WithJitter(zeroJitter)), clk
}

var doms = []string{"a.example", "b.example", "c.example"}
var order = []string{"p1", "p2", "p3", "p4", "p5", "p6", "p7", "p8"}

func TestFirstPresetFixesNothingSecondFixesAll(t *testing.T) {
	s := &script{fixes: map[string][]string{"p2": doms}}
	sr, _ := newSearcher(Config{}, s)
	var seen []Progress
	res := sr.Run(context.Background(), Request{Domains: doms, Order: order, Progress: func(p Progress) { seen = append(seen, p) }})
	if res.Outcome != OutcomeChosen || res.Chosen != "p2" || res.Stopped != "all fixed" || len(res.Failed) != 0 {
		t.Fatalf("%+v", res)
	}
	if !reflect.DeepEqual(s.applied, []string{"p1", "p2"}) {
		t.Fatalf("applied %v: the search must stop at the first preset that fixes everything", s.applied)
	}
	for _, h := range s.hostlists {
		if !reflect.DeepEqual(h, doms) {
			t.Fatalf("trial hostlist %v, want exactly the failing domains", h)
		}
	}
	if len(res.Tried) != 2 || res.Tried[0].Fixed != 0 || res.Tried[1].Fixed != 3 || res.Tried[1].Total != 3 {
		t.Fatalf("tried %+v", res.Tried)
	}
	want := []Progress{{Phase: PhaseStart, Total: 6}, {Phase: PhaseTrying, Index: 1, Total: 6, Preset: "p1"}, {Phase: PhaseTrying, Index: 2, Total: 6, Preset: "p2"}}
	if !reflect.DeepEqual(seen, want) {
		t.Fatalf("progress %+v", seen)
	}
}

func TestBestPartialFixWinsAndEarlierBeatsLaterOnATie(t *testing.T) {
	s := &script{fixes: map[string][]string{"p1": {"a.example"}, "p2": {"a.example", "b.example"}, "p3": {"b.example", "c.example"}}}
	sr, _ := newSearcher(Config{MaxPresets: 3}, s)
	res := sr.Run(context.Background(), Request{Domains: doms, Order: order})
	if res.Outcome != OutcomeChosen || res.Chosen != "p2" || !reflect.DeepEqual(res.Fixed, []string{"a.example", "b.example"}) ||
		!reflect.DeepEqual(res.Failed, []string{"c.example"}) || res.Stopped != "presets" {
		t.Fatalf("%+v", res)
	}
}

func TestNoPresetFixesAnything(t *testing.T) {
	s := &script{}
	sr, _ := newSearcher(Config{MaxPresets: 3}, s)
	res := sr.Run(context.Background(), Request{Domains: doms, Order: order})
	if res.Outcome != OutcomeNone || res.Chosen != "" || !reflect.DeepEqual(res.Failed, doms) || len(res.Tried) != 3 {
		t.Fatalf("%+v", res)
	}
}

func TestBudgetCapsPresetsAndTime(t *testing.T) {
	// 20 presets could be tried; the cap is 6 by default.
	var many []string
	for i := 0; i < 20; i++ {
		many = append(many, "q"+string(rune('a'+i)))
	}
	s := &script{}
	sr, _ := newSearcher(Config{}, s)
	res := sr.Run(context.Background(), Request{Domains: doms, Order: many})
	if len(s.applied) != 6 || res.Stopped != "presets" {
		t.Fatalf("applied %d presets (%s), want 6", len(s.applied), res.Stopped)
	}

	// The time cap: each preset costs settle + pause = 2s + 1s = 3s of fake time plus a 40s probe.
	s = &script{}
	sr, clk := newSearcher(Config{MaxPresets: 20, Budget: 100 * time.Second}, s)
	s.onProbe = func(string) { clk.Advance(40 * time.Second) }
	res = sr.Run(context.Background(), Request{Domains: doms, Order: many})
	if res.Stopped != "budget" || len(s.applied) != 3 {
		t.Fatalf("stopped %q after %d presets, want budget after 3", res.Stopped, len(s.applied))
	}
	if res.Outcome != OutcomeNone {
		t.Fatalf("%+v", res)
	}
}

func TestDomainsAreCutAndDeduplicated(t *testing.T) {
	var ds []string
	for i := 0; i < 15; i++ {
		ds = append(ds, "d"+string(rune('a'+i))+".example", "da.example")
	}
	s := &script{}
	sr, _ := newSearcher(Config{MaxPresets: 1, MaxDomains: 5}, s)
	res := sr.Run(context.Background(), Request{Domains: ds, Order: order})
	if len(res.Domains) != 5 || len(s.hostlists[0]) != 5 {
		t.Fatalf("domains %v", res.Domains)
	}
}

func TestPausesAndSettleStayInTheirBounds(t *testing.T) {
	s := &script{}
	clk := newStepClock()
	draws := 0
	sr := New(Config{MaxPresets: 4, Settle: 3 * time.Second, Pause: [2]time.Duration{time.Second, 2 * time.Second}}, clk, s, s,
		WithJitter(func(lo, hi time.Duration) time.Duration {
			draws++
			if lo != time.Second || hi != 2*time.Second {
				t.Errorf("jitter asked for [%v, %v]", lo, hi)
			}
			return hi
		}))
	sr.Run(context.Background(), Request{Domains: doms, Order: order})
	// 4 presets: a settle after each apply, a pause before presets 2, 3 and 4.
	want := []time.Duration{3 * time.Second, 2 * time.Second, 3 * time.Second, 2 * time.Second, 3 * time.Second, 2 * time.Second, 3 * time.Second}
	if !reflect.DeepEqual(clk.sleeps, want) || draws != 3 {
		t.Fatalf("sleeps %v (draws %d), want %v", clk.sleeps, draws, want)
	}
}

func TestSkippedPresetDoesNotCountAndAbortStopsTheSearch(t *testing.T) {
	s := &script{applyErr: map[string]error{"p1": ErrSkip, "p2": ErrSkip}, fixes: map[string][]string{"p4": doms}}
	sr, _ := newSearcher(Config{MaxPresets: 2}, s)
	res := sr.Run(context.Background(), Request{Domains: doms, Order: order})
	if res.Chosen != "p4" || !res.Tried[0].Skipped || !res.Tried[1].Skipped {
		t.Fatalf("%+v", res)
	}

	boom := errors.New("engine did not start")
	s = &script{applyErr: map[string]error{"p2": boom}}
	sr, _ = newSearcher(Config{}, s)
	res = sr.Run(context.Background(), Request{Domains: doms, Order: order})
	if res.Outcome != OutcomeError || !errors.Is(res.Err, boom) || len(s.applied) != 1 {
		t.Fatalf("%+v", res)
	}
}

func TestUnusableMeasurementIsInconclusiveNotAFailure(t *testing.T) {
	s := &script{probeErr: map[string]error{"p1": ErrNoInternet, "p2": ErrNoInternet}}
	sr, _ := newSearcher(Config{MaxPresets: 2}, s)
	res := sr.Run(context.Background(), Request{Domains: doms, Order: order})
	if res.Outcome != OutcomeInconclusive || len(res.Failed) != 0 || !res.Tried[0].Unusable {
		t.Fatalf("%+v", res)
	}
	// One usable measurement that fixes nothing is a real "none".
	s = &script{probeErr: map[string]error{"p1": ErrNoInternet}}
	sr, _ = newSearcher(Config{MaxPresets: 2}, s)
	if res := sr.Run(context.Background(), Request{Domains: doms, Order: order}); res.Outcome != OutcomeNone {
		t.Fatalf("%+v", res)
	}
}

func TestCancelStopsAtOnce(t *testing.T) {
	s := &script{}
	sr, _ := newSearcher(Config{}, s)
	ctx, cancel := context.WithCancel(context.Background())
	s.onProbe = func(string) { cancel() }
	res := sr.Run(ctx, Request{Domains: doms, Order: order})
	if res.Outcome != OutcomeCanceled || len(s.applied) != 1 {
		t.Fatalf("%+v applied %v", res, s.applied)
	}
	if res := sr.Run(ctx, Request{Domains: doms, Order: order}); res.Outcome != OutcomeCanceled || len(s.applied) != 1 {
		t.Fatalf("a cancelled context must not apply anything: %+v", res)
	}
}

func TestEmptyRequestIsInconclusive(t *testing.T) {
	sr, _ := newSearcher(Config{}, &script{})
	if res := sr.Run(context.Background(), Request{Order: order}); res.Outcome != OutcomeInconclusive {
		t.Fatalf("%+v", res)
	}
}
