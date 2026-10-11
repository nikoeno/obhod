package pc

import (
	"github.com/nikoejento/obhod/internal/lib/checker"
	"github.com/nikoejento/obhod/internal/lib/route"
	"github.com/nikoejento/obhod/internal/lib/version"
	"github.com/nikoejento/obhod/internal/pc/lib/clashapi"
	"github.com/nikoejento/obhod/internal/pc/monitor"
	"github.com/nikoejento/obhod/internal/pc/status"
)

// routeByTag maps a Clash connection to a route by its outbound tag, the last
// element of its chain. The tags direct, desync, tunnel and proxy are the route
// names; anything else counts as direct.
func routeByTag(c clashapi.Connection) route.Route {
	if n := len(c.Chains); n > 0 {
		if r, err := route.Parse(c.Chains[n-1]); err == nil {
			return r
		}
	}
	return route.Direct
}

// failureSink feeds a monitor failure to the checker's failure threshold.
func (a *App) failureSink() func(monitor.Failure) {
	return func(f monitor.Failure) {
		a.d.checker.ReportFailure(checker.Failure{Domain: f.Domain, Route: f.Route, Verdict: f.Verdict, At: f.At})
	}
}

// statusSource builds the status page snapshot from the checker status, the
// monitor's stats and the process list.
type statusSource struct{ a *App }

func (s *statusSource) Snapshot() status.Snapshot {
	a := s.a
	st := a.d.checker.Status()
	snap := status.Snapshot{
		Version:   version.String(binary),
		Network:   st.Network.ASName,
		LastCheck: st.LastCheck,
		NextCheck: st.NextCheck,
		Routes:    make(map[route.Route]int, len(st.Counts)),
		Traffic:   map[route.Route]int64{},
		Warnings:  append([]string(nil), st.Warnings...),
	}
	if snap.Network == "" {
		snap.Network = "unknown network"
	}
	for r, n := range st.Counts {
		snap.Routes[r] = n
	}
	for _, p := range a.d.procs.Status() {
		snap.Processes = append(snap.Processes, status.Process{Name: p.Name, State: string(p.State), Restarts: p.Restarts})
	}

	a.mu.Lock()
	mon := a.monitor
	snap.Recent = append([]status.Change(nil), a.recent...)
	a.mu.Unlock()
	if mon != nil {
		var total, viaTunnel int64
		for r, b := range mon.Stats().Bytes {
			snap.Traffic[r] = b
			total += b
			if r == route.Tunnel {
				viaTunnel += b
			}
		}
		if total > 0 {
			snap.Savings = float64(total-viaTunnel) / float64(total)
		}
	}
	return snap
}
