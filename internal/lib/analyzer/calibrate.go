package analyzer

import (
	"context"
	"fmt"

	"github.com/nikoejento/obhod/internal/lib/analyzer/lib/verdict"
	"github.com/nikoejento/obhod/internal/lib/proc"
)

// CheckState is a tri-state calibration result: "ok" | "failed" for reachability, "yes" | "no" | "unknown" for detection.
type CheckState string

// The calibration states.
const (
	CheckOK      CheckState = "ok"
	CheckFailed  CheckState = "failed"
	CheckYes     CheckState = "yes"
	CheckNo      CheckState = "no"
	CheckUnknown CheckState = "unknown"
)

// Calibration says whether the network is usable for probing and whether DPI is at work.
type Calibration struct {
	Internet   CheckState `json:"internet"`    // ok | failed
	DPIPresent CheckState `json:"dpi_present"` // yes | no | unknown
	Warnings   []string   `json:"warnings,omitempty"`
}

func reached(v Verdict) bool { return v == verdict.OK || v == verdict.OKRedirect }

// calibrate probes the control domains (never blocked) and the canary domains (known blocked) once each.
// All controls failing means the network itself is down (or our probing is broken), so nothing else can be trusted.
func (p *prober) calibrate(ctx context.Context, d Dialer, o Options) Calibration {
	o.Repeats = 1
	o.Stages &^= StageBulk
	c := Calibration{Internet: CheckUnknown, DPIPresent: CheckUnknown}

	okControls := 0
	for _, dom := range p.cfg.ControlDomains {
		if ctx.Err() != nil {
			return c
		}
		r := p.Probe(ctx, Target{Domain: dom}, d, o)
		if reached(r.Verdict) {
			okControls++
		} else {
			c.Warnings = append(c.Warnings, fmt.Sprintf("control domain %s: %s at %s", dom, r.Verdict, r.Stage))
		}
	}
	switch {
	case len(p.cfg.ControlDomains) == 0:
		c.Warnings = append(c.Warnings, "no control domains configured: cannot tell whether the network is up")
	case okControls == 0:
		c.Internet = CheckFailed
		return c
	default:
		c.Internet = CheckOK
	}

	blocked, reachedCanaries := 0, 0
	for _, dom := range p.cfg.CanaryDomains {
		if ctx.Err() != nil {
			return c
		}
		r := p.Probe(ctx, Target{Domain: dom}, d, o)
		switch {
		case reached(r.Verdict):
			reachedCanaries++
		case r.Blocking == verdict.BlockingLikely || r.Blocking == verdict.BlockingConfirmed:
			blocked++
		}
	}
	switch {
	case blocked > 0:
		c.DPIPresent = CheckYes
	case len(p.cfg.CanaryDomains) > 0 && reachedCanaries == len(p.cfg.CanaryDomains):
		c.DPIPresent = CheckNo
	}
	return c
}

// bypassWarnings names foreign bypass tools (winws, nfqws, sing-box, ...) that are running: they change what the
// probes see, so the results describe the network plus that tool, not the bare network.
func (p *prober) bypassWarnings(ctx context.Context) []string {
	if p.procs == nil || len(p.cfg.BypassProcesses) == 0 {
		return nil
	}
	found, err := proc.Find(ctx, p.procs, p.cfg.BypassProcesses...)
	if err != nil {
		if ctx.Err() != nil {
			return nil
		}
		return []string{"process scan failed, cannot tell whether a bypass tool is running: " + err.Error()}
	}
	var out []string
	for _, f := range found {
		out = append(out, fmt.Sprintf("foreign bypass tool running: %s (pid %d); probe results include its effect", f.Name, f.PID))
	}
	return out
}
