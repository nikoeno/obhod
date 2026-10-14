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
	"github.com/nikoejento/obhod/internal/lib/route"
	"github.com/nikoejento/obhod/internal/pc/desync"
	"github.com/nikoejento/obhod/internal/pc/singbox"
)

// rendered is the output of the two generators for one table.
type rendered struct {
	plan    desync.Plan
	planErr error // the plan could not be built; the sing-box config is still valid
	singBox []byte
}

// render builds the desync plan (when wantPlan) and the sing-box config for t.
// It touches no file and starts no process. A plan error is returned in
// rendered.planErr, so a reload can still update sing-box.
func (a *App) render(ctx context.Context, b binaries, t route.Table, wantPlan bool) (rendered, error) {
	var r rendered
	if wantPlan {
		r.plan, r.planErr = a.buildPlan(ctx, b, t)
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

// buildPlan builds the desync plan for t. Without desync domains there is
// nothing to run, so no address to protect and no uplink are needed.
func (a *App) buildPlan(ctx context.Context, b binaries, t route.Table) (desync.Plan, error) {
	if len(t.Domains(route.Desync)) == 0 {
		return desync.Plan{}, nil
	}
	dcfg := a.cfg.Desync
	dcfg.Binary = b.desync
	a.mu.Lock()
	resolved := a.tunnelAddrs
	a.mu.Unlock()
	// A host-name tunnel is protected only by the addresses it resolved to:
	// an unrelated desync.exclude_ips entry must not count as protection.
	if tunnelHost(a.outbounds.Tunnel) != "" && len(resolved) == 0 {
		return desync.Plan{}, fail(exitConfig, "%v", errTunnelUnresolved)
	}
	dcfg.ExcludeIPs = excludeIPs(dcfg.ExcludeIPs, a.outbounds.Tunnel, resolved...)
	if len(dcfg.ExcludeIPs) == 0 {
		return desync.Plan{}, fail(exitConfig, "%v", errNoExclude)
	}
	if b.desync == "" {
		return desync.Plan{}, fail(exitConfig, "the desync engine binary was not found; set desync.binary or put it next to the executable")
	}
	up, err := a.d.uplink(ctx)
	if err != nil {
		return desync.Plan{}, fail(exitRuntime, "no uplink interface: %v", err)
	}
	// Our own TUN must never be taken for the uplink, whatever netif thinks.
	if up.Name == a.tunName() {
		up.Virtual = true
	}
	plan, err := desync.Build(dcfg, up, t)
	switch {
	case errors.Is(err, desync.ErrNoExcludeIP), errors.Is(err, desync.ErrInvalidConfig):
		return desync.Plan{}, fail(exitConfig, "%v", err)
	case err != nil:
		return desync.Plan{}, fail(exitRuntime, "desync plan: %v", err)
	}
	return plan, nil
}

func (a *App) tunName() string {
	if n := a.cfg.SingBox.TUN.Name; n != "" {
		return n
	}
	return defaultTUNName
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
// is merged by a trailing debounce on the app's clock. tables is the
// subscription Run took before the first table was read, so a table published
// during Connect is delivered here and arms the first reload.
func (a *App) reloadLoop(ctx context.Context, tables <-chan route.Table) {
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
	if err := a.op.Lock(ctx); err != nil {
		return 0
	}
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

	// The new sing-box config is checked before anything changes, so a
	// rejected config leaves the engine and sing-box on the same table.
	cand, err := a.prepareSingBox(ctx, bins, r.singBox, cfgFile)
	if err != nil {
		a.log.Error("reload: new sing-box config rejected, keeping the old one", "err", err)
		return 0
	}

	// Desync engine next: it must be ahead of the TUN. A plan that cannot be
	// built leaves the engine as it is; sing-box is still updated.
	if r.planErr != nil {
		a.log.Error("reload: desync plan failed; the engine keeps its current state", "err", r.planErr)
	} else {
		a.applyEngine(ctx, r.plan)
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
	// Keep the running config until the new one is proven, so a sing-box
	// that fails to start with it can be brought back on the old one.
	backup := cfgFile + ".old"
	_ = os.Remove(backup)
	if err := os.Rename(cfgFile, backup); err != nil && !errors.Is(err, os.ErrNotExist) {
		_ = os.Remove(cand)
		a.log.Error("reload: keep a copy of the sing-box config", "err", err)
		return 0
	}
	if err := os.Rename(cand, cfgFile); err != nil {
		_ = os.Rename(backup, cfgFile)
		a.log.Error("reload: replace sing-box config", "err", err)
		return 0
	}
	sctx, cancel := context.WithTimeout(ctx, startTimeout)
	defer cancel()
	if err := a.d.procs.Restart(sctx, nameSingBox, a.singBoxSpec(bins, cfgFile)); err != nil {
		a.log.Error("reload: sing-box did not start with the new config; going back to the previous one", "err", err)
		a.logProcOutput(nameSingBox)
		rerr := os.Rename(backup, cfgFile)
		if rerr == nil {
			rctx, rcancel := context.WithTimeout(ctx, startTimeout)
			rerr = a.d.procs.Restart(rctx, nameSingBox, a.singBoxSpec(bins, cfgFile))
			rcancel()
		}
		if rerr != nil {
			a.singBoxLostLocked(ctx, errors.Join(err, rerr))
			return 0
		}
		a.mu.Lock()
		a.lastSBRun = a.d.clk.Now()
		a.mu.Unlock()
		a.log.Warn("sing-box runs with the previous config")
		return 0
	}
	a.mu.Lock()
	a.lastSBRun = a.d.clk.Now()
	a.mu.Unlock()
	_ = os.Remove(backup)
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
	setup := strings.Join(plan.Setup, "\n")
	if eng.running && eng.name == string(plan.Engine) && eng.args == strings.Join(plan.Args, "\x00") &&
		sameFiles(eng.files, plan.Files) && eng.setup == setup {
		return
	}
	// G13: whether the engine re-reads its hostlist is unverified, so any change restarts it.
	// Changed nft rules (Linux: new uplink or tunnel address) need the human
	// to re-apply them; the engine would otherwise silently get no packets.
	if !eng.running || eng.setup != setup {
		if err := a.checkNFT(ctx, plan); err != nil {
			a.log.Error("reload: desync engine not (re)started", "err", err)
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
