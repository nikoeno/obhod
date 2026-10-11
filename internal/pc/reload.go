package pc

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/nikoejento/obhod/internal/lib/clock"
	"github.com/nikoejento/obhod/internal/lib/netif"
	"github.com/nikoejento/obhod/internal/lib/route"
	"github.com/nikoejento/obhod/internal/pc/desync"
	"github.com/nikoejento/obhod/internal/pc/singbox"
)

// rendered is the output of the two generators for one table.
type rendered struct {
	plan    desync.Plan
	singBox []byte
}

// render builds the desync plan (when wantPlan) and the sing-box config for t.
// It touches no file and starts no process.
func (a *App) render(ctx context.Context, b binaries, t route.Table, wantPlan bool) (rendered, error) {
	var r rendered
	if wantPlan {
		dcfg := a.cfg.Desync
		dcfg.Binary = b.desync
		dcfg.ExcludeIPs = excludeIPs(dcfg.ExcludeIPs, a.outbounds.Tunnel)
		if len(dcfg.ExcludeIPs) == 0 {
			return r, fail(exitConfig, "%v", errNoExclude)
		}
		var up netif.Interface
		if len(t.Domains(route.Desync)) > 0 {
			if b.desync == "" {
				return r, fail(exitConfig, "the desync engine binary was not found; set desync.binary or put it next to the executable")
			}
			var err error
			if up, err = a.d.uplink(ctx); err != nil {
				return r, fail(exitRuntime, "no uplink interface: %v", err)
			}
		}
		plan, err := desync.Build(dcfg, up, t)
		switch {
		case errors.Is(err, desync.ErrNoExcludeIP):
			return r, fail(exitConfig, "%v", err)
		case err != nil:
			return r, fail(exitRuntime, "desync plan: %v", err)
		}
		r.plan = plan
	}
	scfg := a.cfg.SingBox
	scfg.Binary = b.singBox
	scfg.ClashSecret = a.currentSecret()
	scfg.OwnProcesses = a.ownProcesses(b)
	out, err := singbox.Generate(scfg, a.outbounds, t)
	if err != nil {
		return r, fail(exitConfig, "sing-box config: %v", err)
	}
	r.singBox = out
	return r, nil
}

func (a *App) currentSecret() string {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.secret
}

// prepareSingBox compares the generated config with the file on disk. When it
// differs, the new content is written next to it and checked; the returned
// path is the checked candidate (empty when nothing changed). A failed check
// removes the candidate and leaves the old file alone.
func (a *App) prepareSingBox(ctx context.Context, b binaries, data []byte, cfgFile string) (candidate string, err error) {
	if old, rerr := os.ReadFile(cfgFile); rerr == nil && bytes.Equal(old, data) {
		return "", nil
	}
	candidate = cfgFile + ".new"
	if err := writeAtomic(candidate, data, 0o600); err != nil {
		return "", err
	}
	if err := a.d.singCheck(ctx, b.singBox, candidate); err != nil {
		_ = os.Remove(candidate)
		return "", err
	}
	return candidate, nil
}

// stageSingBox makes cfgFile hold data after a successful check (used at connect).
func (a *App) stageSingBox(ctx context.Context, b binaries, data []byte, cfgFile string) error {
	cand, err := a.prepareSingBox(ctx, b, data, cfgFile)
	if err != nil {
		return err
	}
	if cand == "" {
		return nil
	}
	return os.Rename(cand, cfgFile)
}

// reloadLoop reacts to table changes and uplink changes. A burst of events
// is merged by a trailing debounce on the app's clock.
func (a *App) reloadLoop(ctx context.Context) {
	tables, unsub := a.d.checker.Subscribe()
	defer unsub()
	events, err := a.d.watchUplink(ctx)
	if err != nil {
		a.log.Warn("uplink watch unavailable; uplink changes need a reconnect", "err", err)
		events = nil
	}

	var timer clock.Timer
	var timerC <-chan time.Time
	arm := func(d time.Duration) {
		if timer == nil {
			timer = a.d.clk.NewTimer(d)
		} else {
			timer.Stop()
			timer.Reset(d)
		}
		timerC = timer.C()
	}
	defer func() {
		if timer != nil {
			timer.Stop()
		}
	}()

	for {
		select {
		case <-ctx.Done():
			return
		case t, ok := <-tables:
			if !ok {
				tables = nil
				continue
			}
			a.setTable(t)
			arm(reloadDebounce)
		case ev, ok := <-events:
			if !ok {
				events = nil
				continue
			}
			a.log.Info("network change", "kind", int(ev.Kind))
			if a.d.resetDialer != nil {
				a.d.resetDialer()
			}
			arm(reloadDebounce)
		case <-timerC:
			timerC = nil
			if retry := a.reload(ctx); retry > 0 {
				arm(retry)
			}
		}
	}
}

// reload regenerates both outputs and applies what changed. It returns a
// delay after which it wants to run again (a sing-box restart was postponed).
//
// In the disconnected state only the sing-box file is kept up to date. While
// connected, the desync engine is stopped, started or restarted when its plan
// changed, and sing-box is restarted when its bytes changed. A config that
// fails `sing-box check` is dropped: the old file and process stay.
func (a *App) reload(ctx context.Context) (retry time.Duration) {
	a.op.Lock()
	defer a.op.Unlock()

	t := a.d.checker.Table()
	a.setTable(t)
	bins, err := a.resolveBinaries()
	if err != nil {
		a.log.Error("reload", "err", err)
		return 0
	}
	connected := a.State() == StateConnected
	r, err := a.render(ctx, bins, t, connected)
	if err != nil {
		a.log.Error("reload: generate", "err", err)
		return 0
	}
	cfgFile := filepath.Join(a.p.Run, singBoxFileName)

	if !connected {
		if err := a.stageSingBox(ctx, bins, r.singBox, cfgFile); err != nil {
			a.log.Error("reload: new sing-box config rejected, keeping the old one", "err", err)
		}
		return 0
	}

	// Desync engine first: it must be ahead of the TUN.
	a.applyEngine(ctx, r.plan)

	cand, err := a.prepareSingBox(ctx, bins, r.singBox, cfgFile)
	if err != nil {
		a.log.Error("reload: new sing-box config rejected, keeping the old one", "err", err)
		return 0
	}
	if cand == "" {
		return 0
	}
	a.mu.Lock()
	since := a.d.clk.Now().Sub(a.lastSBRun)
	a.mu.Unlock()
	if since < singBoxMinGap {
		_ = os.Remove(cand) // regenerated from the latest table when the timer fires
		return singBoxMinGap - since
	}
	if err := os.Rename(cand, cfgFile); err != nil {
		a.log.Error("reload: replace sing-box config", "err", err)
		return 0
	}
	sctx, cancel := context.WithTimeout(ctx, startTimeout)
	defer cancel()
	if err := a.d.procs.Restart(sctx, nameSingBox, a.singBoxSpec(bins, cfgFile)); err != nil {
		a.log.Error("reload: restart sing-box", "err", err)
		return 0
	}
	a.mu.Lock()
	a.lastSBRun = a.d.clk.Now()
	a.mu.Unlock()
	a.log.Info("sing-box restarted with a new config")
	return 0
}

// applyEngine brings the engine in line with plan. Errors are logged: a
// broken engine must not take the tunnel down with it.
func (a *App) applyEngine(ctx context.Context, plan desync.Plan) {
	a.mu.Lock()
	eng := a.engine
	a.mu.Unlock()

	if plan.Path == "" {
		if eng.running {
			sctx, cancel := context.WithTimeout(ctx, stepTimeout)
			defer cancel()
			if err := a.d.procs.Stop(sctx, eng.name); err != nil {
				a.log.Error("reload: stop desync engine", "err", err)
			}
			a.mu.Lock()
			a.engine = engineState{}
			a.mu.Unlock()
			a.log.Info("desync engine stopped: no desync domains")
		}
		return
	}
	if eng.running && eng.name == string(plan.Engine) && eng.args == strings.Join(plan.Args, "\x00") && sameFiles(eng.files, plan.Files) {
		return
	}
	// G13: whether the engine re-reads its hostlist is unverified, so any change restarts it.
	if !eng.running {
		if err := a.checkNFT(ctx, plan); err != nil {
			a.log.Error("reload: desync engine not started", "err", err)
			return
		}
	}
	if err := a.writePlanFiles(plan); err != nil {
		a.log.Error("reload: write desync files", "err", err)
		return
	}
	sctx, cancel := context.WithTimeout(ctx, stepTimeout)
	defer cancel()
	var err error
	if eng.running {
		err = a.d.procs.Restart(sctx, string(plan.Engine), a.engineSpec(plan))
	} else {
		err = a.d.procs.Start(sctx, a.engineSpec(plan))
	}
	if err != nil {
		a.log.Error("reload: start desync engine", "err", err)
		return
	}
	a.rememberEngine(plan)
}

func sameFiles(old map[string]string, now map[string][]byte) bool {
	if len(old) != len(now) {
		return false
	}
	for k, v := range now {
		if o, ok := old[k]; !ok || o != string(v) {
			return false
		}
	}
	return true
}
