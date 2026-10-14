package pc

import (
	"context"
	"log/slog"
	"strconv"
	"sync"

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

// watchedClash is the monitor's Clash API client. The monitor retries
// silently by contract; this wrapper logs when the API stops answering and
// when it is back, and the status page shows a warning meanwhile, so a wrong
// secret or a dead API does not look like a quiet network.
type watchedClash struct {
	clashapi.Client
	log *slog.Logger

	mu      sync.Mutex
	failing bool
}

// Connections calls the API and tracks whether it answers.
func (w *watchedClash) Connections(ctx context.Context) (clashapi.Snapshot, error) {
	snap, err := w.Client.Connections(ctx)
	if err != nil && ctx.Err() != nil {
		return snap, err // shutting down, not an outage
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	switch {
	case err != nil && !w.failing:
		w.failing = true
		w.log.Warn("clash api is not answering; traffic stats and failure detection are paused", "err", err)
	case err == nil && w.failing:
		w.failing = false
		w.log.Info("clash api answers again")
	}
	return snap, err
}

func (w *watchedClash) down() bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.failing
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
		State:     string(a.State()),
		Network:   networkLabel(st.Network),
		LastCheck: st.LastCheck,
		NextCheck: st.NextCheck,
		Routes:    make(map[route.Route]int, len(st.Counts)),
		Traffic:   map[route.Route]int64{},
		Warnings:  append([]string(nil), st.Warnings...),
	}
	for r, n := range st.Counts {
		snap.Routes[r] = n
	}
	for _, p := range a.d.procs.Status() {
		snap.Processes = append(snap.Processes, status.Process{Name: p.Name, State: string(p.State), Restarts: p.Restarts})
	}

	a.mu.Lock()
	mon, watch := a.monitor, a.clashWatch
	snap.Recent = append([]status.Change(nil), a.recent...)
	a.mu.Unlock()
	if watch != nil && watch.down() {
		snap.Warnings = append(snap.Warnings, "sing-box's Clash API is not answering: traffic stats and failure detection are paused")
	}
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

// networkLabel names the network for the status page: the AS name, else AS<number>, else a placeholder.
// It never contains the public IP.
func networkLabel(n checker.Network) string {
	switch {
	case n.ASName != "":
		return n.ASName
	case n.ASN != 0:
		return "AS" + strconv.FormatUint(uint64(n.ASN), 10)
	}
	return "unknown network"
}
