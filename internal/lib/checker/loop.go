package checker

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/nikoejento/obhod/internal/lib/analyzer"
	"github.com/nikoejento/obhod/internal/lib/checker/decide"
	"github.com/nikoejento/obhod/internal/lib/checker/lib/model"
	"github.com/nikoejento/obhod/internal/lib/checker/schedule"
	"github.com/nikoejento/obhod/internal/lib/clock"
	"github.com/nikoejento/obhod/internal/lib/route"
)

// Run watches the network, schedules checks and updates decisions until ctx is done. It returns nil then, or
// the error of a failed store write. Probe problems never stop the loop. Run may be called once.
func (c *Checker) Run(ctx context.Context) error {
	c.mu.Lock()
	if c.started {
		c.mu.Unlock()
		return errors.New("checker: Run called twice")
	}
	c.started = true
	c.mu.Unlock()

	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		if err := c.net.Run(ctx); err != nil && ctx.Err() == nil {
			c.warn("network detection stopped: %v", err)
		}
	}()
	go func() {
		defer wg.Done()
		_ = c.sched.Run(ctx)
	}()

	err := c.loop(ctx)
	cancel()
	wg.Wait()
	if cerr := c.store.Close(); err == nil {
		err = cerr
	}
	return err
}

func (c *Checker) loop(ctx context.Context) error {
	keep := append([]string{}, c.candidates...) // non-nil: decisions of dropped domains go too
	if err := c.store.Prune(c.clk.Now().Add(-c.cfg.Retention), keep); err != nil {
		return err
	}

	var expTimer clock.Timer
	var expC <-chan time.Time
	arm := func() {
		if expTimer != nil {
			expTimer.Stop()
			expTimer, expC = nil, nil
		}
		if d, ok := c.nextExpiry(); ok {
			expTimer = c.clk.NewTimer(d)
			expC = expTimer.C()
		}
	}
	defer func() {
		if expTimer != nil {
			expTimer.Stop()
		}
	}()

	if err := c.applyNetwork(c.net.Current()); err != nil {
		return err
	}
	c.recheckExpired()
	arm()

	for {
		select {
		case <-ctx.Done():
			return nil
		case n := <-c.net.Changes():
			if err := c.applyNetwork(n); err != nil {
				return err
			}
			c.recheckExpired()
			arm()
		case t := <-c.sched.Triggers():
			err := c.cycle(ctx, t)
			c.sched.Done(t)
			if err != nil {
				return err
			}
			arm()
		case <-expC:
			c.recheckExpired()
			arm()
		}
	}
}

// applyNetwork switches to n: it marks the network as seen, publishes that network's stored decisions and
// asks the scheduler for a check.
func (c *Checker) applyNetwork(n Network) error {
	c.mu.Lock()
	c.cur = n
	c.mu.Unlock()
	if n.Known {
		if err := c.store.Touch(modelID(n.ID), n.ASName); err != nil {
			return err
		}
	}
	c.rebuild()
	if n.Known {
		c.sched.NetworkChanged(modelID(n.ID))
	}
	return nil
}

// candidateDecisions returns the stored decisions of the current network for current candidates.
func (c *Checker) candidateDecisions() (model.NetworkID, []model.Decision) {
	c.mu.Lock()
	n := c.cur
	c.mu.Unlock()
	if !n.Known {
		return "", nil
	}
	var out []model.Decision
	for _, d := range c.store.All(modelID(n.ID)) {
		if c.candSet[d.Domain] {
			out = append(out, d)
		}
	}
	return modelID(n.ID), out
}

// nextExpiry returns the time until the earliest not yet expired decision runs out.
func (c *Checker) nextExpiry() (time.Duration, bool) {
	_, ds := c.candidateDecisions()
	now := c.clk.Now()
	var best time.Duration
	found := false
	for _, d := range ds {
		if d.ExpiresAt.IsZero() || d.Expired(now) {
			continue
		}
		if wait := d.ExpiresAt.Sub(now); !found || wait < best {
			best, found = wait, true
		}
	}
	return best, found
}

// recheckExpired asks the scheduler to recheck the decisions that have expired. Expired entries stay in use
// meanwhile. An inconclusive recheck is not retried here (that would loop): the periodic check covers it.
func (c *Checker) recheckExpired() {
	id, ds := c.candidateDecisions()
	now := c.clk.Now()
	var domains []string
	for _, d := range ds {
		if d.Expired(now) {
			domains = append(domains, d.Domain)
		}
	}
	if len(domains) > 0 {
		c.sched.Expired(id, domains)
	}
}

// cycle runs one probe → decide → store → publish round for trigger t. Only a store failure is an error.
func (c *Checker) cycle(ctx context.Context, t schedule.Trigger) error {
	c.mu.Lock()
	net := c.cur
	c.mu.Unlock()
	if !net.Known {
		return nil // decisions need a network ID; the identification will trigger a check
	}
	targets := c.targets(t)
	if len(targets) == 0 {
		return nil
	}

	c.setRunning(true)
	defer c.setRunning(false)
	batch := analyzer.Batch{Dialers: c.deps.Dialers}
	for _, d := range targets {
		batch.Targets = append(batch.Targets, analyzer.Target{Domain: d})
	}
	report, err := c.deps.Runner.Run(ctx, batch)
	if ctx.Err() != nil {
		return nil
	}
	now := c.clk.Now()
	c.mu.Lock()
	c.lastCheck = now
	c.lastReport = &report
	c.mu.Unlock()
	if err != nil {
		c.warn("probe run failed: %v", err)
		return nil
	}
	if report.Calibration.Internet != analyzer.CheckOK {
		c.warn("calibration: internet check %q; decisions left unchanged", report.Calibration.Internet)
		return nil
	}
	if cur := c.net.Current(); cur.ID != net.ID || cur.Known != net.Known {
		return nil // the network changed while probing: these results belong to the old one
	}

	byDomain := map[string]map[route.Route]analyzer.ProbeResult{}
	for _, r := range report.Results {
		rt := r.Route
		if rt == "" {
			rt = route.Direct
		}
		m := byDomain[r.Target.Domain]
		if m == nil {
			m = map[route.Route]analyzer.ProbeResult{}
			byDomain[r.Target.Domain] = m
		}
		m[rt] = r
	}
	id := modelID(net.ID)
	var out []model.Decision
	for _, dom := range targets {
		results := byDomain[dom]
		if len(results) == 0 {
			continue
		}
		in := decide.Input{Domain: dom, Results: results, Now: now, Policy: c.cfg.Decide}
		if cur, ok := c.store.Get(id, dom); ok {
			in.Current = &cur
		}
		o := decide.Decide(in)
		if o.Decision.Route == "" {
			continue // no conclusion and nothing stored
		}
		out = append(out, o.Decision)
	}
	if len(out) > 0 {
		if err := c.store.Put(id, out...); err != nil {
			return fmt.Errorf("checker: %w", err)
		}
	}
	c.rebuild()
	return nil
}

// targets returns the candidate domains a trigger asks for: its domains, or all candidates.
func (c *Checker) targets(t schedule.Trigger) []string {
	if t.Domains == nil {
		return c.candidates
	}
	var out []string
	for _, d := range t.Domains {
		if c.candSet[d] {
			out = append(out, d)
		}
	}
	return out
}

func (c *Checker) setRunning(v bool) {
	c.mu.Lock()
	c.running = v
	c.mu.Unlock()
}
