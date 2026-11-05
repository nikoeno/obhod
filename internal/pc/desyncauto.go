package pc

import (
	"context"
	"errors"
	"fmt"
	"net/netip"
	"sort"
	"strings"
	"time"

	"github.com/nikoejento/obhod/internal/lib/analyzer"
	"github.com/nikoejento/obhod/internal/lib/netif"
	"github.com/nikoejento/obhod/internal/lib/route"
	"github.com/nikoejento/obhod/internal/pc/autodesync"
	"github.com/nikoejento/obhod/internal/pc/desync"
)

// Desync preset selection and the automatic search (decided 2026-11-01, stage 2 of 3; the logic of one search
// is internal/pc/autodesync, the HTTP side is api/desktop).
//
// With desync.preset "auto", a domain the checker routes to desync is "pending" until a search has shown that
// a preset reaches it from this network ("verified"), or that none does ("failed": the app then serves it on
// the tunnel route, which is the degraded direct route without a detour server). The checker is not told:
// its decision stays desync/checked. The app applies this on top of the checker's table (effectiveTable), so
// the stored decisions, the checker's hysteresis and its tests are unchanged.

// DesyncMode says where the active preset comes from.
type DesyncMode string

// The modes.
const (
	DesyncModePreset DesyncMode = "preset" // one named catalogue preset, chosen by the user
	DesyncModeAuto   DesyncMode = "auto"   // the app searches and remembers the best per network
	DesyncModeCustom DesyncMode = "custom" // desync.strategy of app.yaml
)

// The per-route desync states of RouteInfo.Desync (only in mode auto).
const (
	DesyncPending  = "pending"  // routed to desync by the checker; no search has verified it yet
	DesyncVerified = "verified" // a search showed the active preset reaches it
	DesyncFailed   = "failed"   // no preset reached it; served on the tunnel route
)

// DesyncTry is one preset of a search.
type DesyncTry struct {
	Preset string
	Fixed  int // sites that were reachable with it
	Total  int // sites probed
}

// DesyncSearch is the summary of the last search of this run. It holds no site name.
type DesyncSearch struct {
	At      time.Time
	Tried   []DesyncTry // the presets that were applied, in order
	Chosen  string      // "" when no preset fixed a site
	Outcome string      // chosen | none | inconclusive | error
}

// DesyncStatus is the state of the desync choice.
type DesyncStatus struct {
	Mode DesyncMode
	// Preset is the catalogue id in use: the chosen one, or in mode auto the one stored for this network (the
	// default preset when there is none yet); "custom" in mode custom.
	Preset     string
	Searching  bool
	LastSearch *DesyncSearch // nil until a search ended in this run
}

// DesyncPreset is one catalogue entry for a picker.
type DesyncPreset struct {
	ID, Name, Description string
}

// DesyncEvent is the payload of a desync event. It carries counts and catalogue ids, never a site name.
type DesyncEvent struct {
	Phase  string // searching | trying | done
	Index  int    // trying: this is preset Index of Total
	Total  int
	Preset string // trying: the catalogue id
	// done:
	Outcome string // chosen | none | inconclusive | error | canceled
	Chosen  string
	Fixed   int // sites reachable with Chosen
	Of      int // sites probed
}

// The error kinds of the desync calls (errors.Is).
var (
	ErrDesyncPreset   = errors.New("pc: unknown desync preset")                       // SetDesync: not auto, custom or a catalogue id
	ErrDesyncBusy     = errors.New("pc: a desync search is running or not connected") // StartDesyncSearch
	ErrDesyncNotAuto  = errors.New("pc: the desync preset is not automatic")          // StartDesyncSearch
	ErrDesyncNoDomain = errors.New("pc: no site is routed to desync")                 // StartDesyncSearch
)

// errPayloadMissing marks a preset whose fake payload file is not there (with errProgramMissing).
var errPayloadMissing = errors.New("desync preset payload not found")

// presetPriority is the order in which a search tries the catalogue presets (the stored winner of the network
// goes first). The first three are the ones reported to work on the author's network; then the multisplit
// variants (most faithful translations, no fake packets), the fake-and-split combinations, the fake-only and
// disorder ones, and the faketls-auto series and the experimental one last. alt12 renders the same as alt11
// and is left out.
var presetPriority = []string{
	"alt2-multisplit", "alt3-hostfakesplit", "alt-fakedsplit",
	"general-multisplit", "alt6-multisplit-681", "alt7-multisplit-sniext",
	"alt11-fake-multisplit", "alt4-fake-multisplit", "alt9-hostfakesplit-ozon",
	"alt8-fake-badseq", "alt10-fake-ts", "alt5-multidisorder",
	"simple-fake", "simple-fake-alt", "simple-fake-alt2",
	"faketls-auto", "faketls-auto-alt-fakedsplit", "faketls-auto-alt2-multisplit", "faketls-auto-alt3-multisplit-ts",
	"exp-fake-multisplit",
}

// searchRun is a search in progress.
type searchRun struct {
	cancel context.CancelFunc
	done   chan struct{}
}

// trialState is the preset and hostlist the engine is held at while a search runs: reloads render this plan
// instead of the normal one, so they cannot disturb a measurement.
type trialState struct {
	preset  string
	domains []string
}

// ---- state ----

// dsStoreLocked opens the choice store on first use. dsMu must be held. A store that cannot be opened is
// logged once and the app goes on without persistence (nil).
func (a *App) dsStoreLocked() *autodesync.Store {
	if a.dsStore == nil && !a.dsStoreFailed {
		st, err := autodesync.OpenStore(a.p.Data, autodesync.OSFS())
		if err != nil {
			a.dsStoreFailed = true
			a.log.Warn("the desync choice file cannot be used; choices are not remembered", "err", err)
			return nil
		}
		if st.Recovered() {
			a.log.Warn("the desync choice file was not valid; it was moved aside and the choices start empty", "file", autodesync.CorruptName)
		}
		a.dsStore = st
	}
	return a.dsStore
}

func (a *App) dsStoreGet() *autodesync.Store {
	a.dsMu.Lock()
	defer a.dsMu.Unlock()
	return a.dsStoreLocked()
}

// selectedPreset is the user's choice: the API's selection when there is one, else app.yaml's value.
func (a *App) selectedPreset() string {
	if st := a.dsStoreGet(); st != nil {
		if s := st.Selected(); s == desync.PresetAuto {
			return s
		} else if _, ok := desync.LookupPreset(s); ok {
			return s
		}
	}
	return a.cfg.Desync.PresetID()
}

// desyncSelection returns the mode and, for a named preset, its id.
func (a *App) desyncSelection() (DesyncMode, string) {
	switch sel := a.selectedPreset(); sel {
	case desync.PresetAuto:
		return DesyncModeAuto, ""
	case desync.PresetCustom:
		return DesyncModeCustom, desync.PresetCustom
	default:
		return DesyncModePreset, sel
	}
}

func (a *App) record(netID string) (autodesync.Record, bool) {
	if netID == "" {
		return autodesync.Record{}, false
	}
	st := a.dsStoreGet()
	if st == nil {
		return autodesync.Record{}, false
	}
	return st.Get(netID)
}

// activePreset is the preset the engine runs with for the network netID (a trial overrides it).
func (a *App) activePreset(netID string) string {
	a.dsMu.Lock()
	if a.dsTrial != nil {
		p := a.dsTrial.preset
		a.dsMu.Unlock()
		return p
	}
	a.dsMu.Unlock()
	mode, id := a.desyncSelection()
	switch mode {
	case DesyncModeAuto:
		if rec, ok := a.record(netID); ok {
			if _, known := desync.LookupPreset(rec.Preset); known {
				return rec.Preset
			}
		}
		return desync.DefaultPreset
	case DesyncModeCustom:
		return desync.PresetCustom
	}
	return id
}

// desyncConfig is the desync config the plan for network netID is built from.
func (a *App) desyncConfig(netID string) desync.Config {
	dcfg := a.cfg.Desync
	id := a.activePreset(netID)
	if id == desync.PresetCustom {
		if dcfg.Preset == desync.PresetAuto { // cannot happen: Validate refuses a strategy with auto
			dcfg.Preset = ""
		}
		return dcfg
	}
	dcfg.Preset, dcfg.Strategy, dcfg.Ports = id, nil, nil
	return dcfg
}

// desyncStates classifies the entries of a raw table the checker routes to desync (checked or default
// entries only: a user rule or a list entry is not verified). It also lists the failed ones that are still
// within FailedTTL.
type dsClass struct {
	pending, verified, failed []string
}

func (a *App) classify(raw route.Table, rec autodesync.Record, haveRec bool) dsClass {
	var c dsClass
	now := a.d.clk.Now()
	ttl := a.auto.FailedTTL
	fixed := map[string]bool{}
	for _, d := range rec.Fixed {
		fixed[d] = true
	}
	for _, e := range raw.Entries {
		if e.Route != route.Desync || (e.Source != route.SourceChecked && e.Source != route.SourceDefault) {
			continue
		}
		switch at, failed := rec.Failed[e.Domain]; {
		case haveRec && fixed[e.Domain]:
			c.verified = append(c.verified, e.Domain)
		case haveRec && failed && now.Sub(at) < ttl:
			c.failed = append(c.failed, e.Domain)
		default:
			c.pending = append(c.pending, e.Domain)
		}
	}
	return c
}

// effectiveTable is the checker's table with the desync results applied: in mode auto, a site no preset
// fixed is served on the tunnel route. It also returns the desync state of every checked desync site.
func (a *App) effectiveTable(raw route.Table) (route.Table, map[string]string) {
	mode, _ := a.desyncSelection()
	if mode != DesyncModeAuto || raw.NetworkID == "" {
		return raw, nil
	}
	rec, have := a.record(raw.NetworkID)
	c := a.classify(raw, rec, have)
	states := make(map[string]string, len(c.pending)+len(c.verified)+len(c.failed))
	for _, d := range c.pending {
		states[d] = DesyncPending
	}
	for _, d := range c.verified {
		states[d] = DesyncVerified
	}
	for _, d := range c.failed {
		states[d] = DesyncFailed
	}
	if len(c.failed) == 0 {
		return raw, states
	}
	entries := append([]route.Entry(nil), raw.Entries...)
	for i, e := range entries {
		if states[e.Domain] == DesyncFailed && e.Route == route.Desync {
			entries[i].Route = route.Tunnel
		}
	}
	t, err := route.NewTable(raw.NetworkID, raw.Generated, entries)
	if err != nil {
		return raw, states
	}
	return t, states
}

// adjustCounts moves the failed sites from the desync to the tunnel count of the checker's per-route counts.
func (a *App) adjustCounts(counts map[route.Route]int) {
	a.mu.Lock()
	n := 0
	for _, s := range a.dsStates {
		if s == DesyncFailed {
			n++
		}
	}
	a.mu.Unlock()
	n = min(n, counts[route.Desync])
	if n > 0 {
		counts[route.Desync] -= n
		counts[route.Tunnel] += n
	}
}

func (a *App) kickReload() {
	select {
	case a.kick <- struct{}{}:
	default:
	}
}

// ---- API for the gateway ----

// Desync returns the state of the desync choice.
func (a *App) Desync() DesyncStatus {
	mode, id := a.desyncSelection()
	a.mu.Lock()
	netID := a.raw.NetworkID
	a.mu.Unlock()
	st := DesyncStatus{Mode: mode, Preset: id}
	if mode == DesyncModeAuto {
		st.Preset = a.activePreset(netID)
	}
	a.dsMu.Lock()
	st.Searching = a.dsSearch != nil
	if a.dsLast != nil {
		l := *a.dsLast
		l.Tried = append([]DesyncTry(nil), l.Tried...)
		st.LastSearch = &l
	}
	a.dsMu.Unlock()
	return st
}

// DesyncPresets lists the catalogue in display order.
func (a *App) DesyncPresets() []DesyncPreset {
	ps := desync.Presets()
	out := make([]DesyncPreset, 0, len(ps))
	for _, p := range ps {
		out = append(out, DesyncPreset{ID: p.ID, Name: p.Name, Description: p.Description})
	}
	return out
}

// SetDesync chooses the preset: "auto", a catalogue id, or "custom" (only when app.yaml has a strategy; it
// removes the API's choice). The choice is stored in Paths.Data/desync.json and wins over app.yaml until
// changed. A named preset needs its fake payload files (otherwise the error is Classify's program_missing);
// auto skips presets whose files are missing. While connected the reload path applies it (the engine
// restarts with the new preset) and, for auto, a search starts when sites wait for one. Errors:
// ErrDesyncPreset, a program_missing error, or an internal one.
func (a *App) SetDesync(ctx context.Context, preset string) error {
	preset = strings.TrimSpace(preset)
	stored := preset
	switch preset {
	case desync.PresetAuto:
	case desync.PresetCustom:
		if len(a.cfg.Desync.Strategy) == 0 && a.cfg.Desync.Preset != desync.PresetCustom {
			return ErrDesyncPreset
		}
		stored = ""
	default:
		if _, ok := desync.LookupPreset(preset); !ok {
			return ErrDesyncPreset
		}
		if err := a.checkPresetFiles(ctx, preset); err != nil {
			return err
		}
	}
	a.dsMu.Lock()
	st := a.dsStoreLocked()
	a.dsMu.Unlock()
	if st == nil {
		return errors.New("the desync choice cannot be stored")
	}
	if err := st.SetSelected(stored); err != nil {
		return err
	}
	if preset != desync.PresetAuto {
		a.stopSearch(false)
	}
	a.setTable(a.d.checker.Table())
	a.log.Info("desync preset chosen", "preset", preset)
	if a.State() == StateConnected {
		a.kickReload()
		_ = a.startSearch(searchAuto)
	}
	return nil
}

// StartDesyncSearch starts a search now, for every site the checker routes to desync (also verified and failed
// ones: that is the way to try a failed site again). It returns at once; progress arrives as desync events.
// Errors: ErrDesyncBusy (not connected, or a search runs), ErrDesyncNotAuto, ErrDesyncNoDomain.
func (a *App) StartDesyncSearch() error { return a.startSearch(searchUser) }

type searchReason int

const (
	searchAuto searchReason = iota // a connect, a new table or a preset choice: only when sites are pending
	searchUser                     // the user asked: everything routed to desync
	searchLive                     // live failures of a verified site
)

// checkPresetFiles refuses a named preset whose fake payload files are missing next to the engine, the same
// check the connect makes. Without an engine binary there is nothing to compare with: connect reports it.
func (a *App) checkPresetFiles(ctx context.Context, preset string) error {
	if a.d.fileExists == nil {
		return nil
	}
	bins, err := a.resolveBinaries()
	if err != nil || bins.desync == "" {
		return nil
	}
	dcfg := a.cfg.Desync
	dcfg.Binary, dcfg.Preset, dcfg.Strategy, dcfg.Ports = bins.desync, preset, nil, nil
	dcfg.ExcludeIPs = []netip.Prefix{noTunnelExclude}
	tb, err := route.NewTable("", time.Time{}, []route.Entry{{Domain: "check.invalid", Route: route.Desync, Source: route.SourceStatic}})
	if err != nil {
		return err
	}
	plan, err := desync.Build(dcfg, netif.Interface{Name: "check0", Index: 1}, tb)
	if err != nil {
		return fmt.Errorf("desync plan: %w", err)
	}
	for _, f := range plan.Requires {
		if !a.d.fileExists(f) {
			a.log.Warn("desync preset not usable: a payload file is missing", "preset", preset, "file", f)
			return fmt.Errorf("%w: a payload file of the desync preset %q is missing", errProgramMissing, preset)
		}
	}
	return nil
}

// ---- the search ----

// startSearch starts a background search when the conditions hold. For searchAuto and searchLive it is silent
// when there is nothing to do; for searchUser it says why not.
func (a *App) startSearch(reason searchReason) error {
	user := reason == searchUser
	refuse := func(err error) error {
		if user {
			return err
		}
		return nil
	}
	if a.d.trialProbe == nil || a.State() != StateConnected {
		return refuse(ErrDesyncBusy)
	}
	if mode, _ := a.desyncSelection(); mode != DesyncModeAuto {
		return refuse(ErrDesyncNotAuto)
	}
	a.mu.Lock()
	raw := a.raw
	a.mu.Unlock()
	if raw.NetworkID == "" {
		return refuse(ErrDesyncNoDomain)
	}
	rec, have := a.record(raw.NetworkID)
	c := a.classify(raw, rec, have)
	var domains []string
	switch {
	case user:
		domains = concat(c.pending, c.verified, c.failed)
	case len(c.pending) > 0:
		domains = concat(c.pending, c.verified)
	}
	if len(domains) == 0 {
		return refuse(ErrDesyncNoDomain)
	}
	now := a.d.clk.Now()
	a.dsMu.Lock()
	if a.dsSearch != nil {
		a.dsMu.Unlock()
		return refuse(ErrDesyncBusy)
	}
	if !user && now.Before(a.dsCool) {
		a.dsMu.Unlock()
		return nil
	}
	if reason == searchLive {
		a.dsCool = now.Add(a.auto.Backoff)
	}
	ctx, cancel := context.WithCancel(a.runContext())
	run := &searchRun{cancel: cancel, done: make(chan struct{})}
	a.dsSearch = run
	a.dsMu.Unlock()

	order := searchOrder(rec.Preset)
	go func() {
		defer close(run.done)
		a.runSearch(ctx, raw.NetworkID, domains, order)
	}()
	return nil
}

func concat(lists ...[]string) []string {
	var out []string
	for _, l := range lists {
		out = append(out, l...)
	}
	return out
}

// searchOrder is presetPriority with the stored winner of the network first.
func searchOrder(stored string) []string {
	out := make([]string, 0, len(presetPriority)+1)
	if _, ok := desync.LookupPreset(stored); ok {
		out = append(out, stored)
	}
	for _, p := range presetPriority {
		if p != stored {
			out = append(out, p)
		}
	}
	return out
}

func (a *App) runSearch(ctx context.Context, netID string, domains, order []string) {
	a.log.Info("desync search started", "sites", len(domains))
	s := autodesync.New(a.auto, a.d.clk, trialApplier{a}, trialProber{a})
	res := s.Run(ctx, autodesync.Request{
		Domains: domains, Order: order,
		Progress: func(p autodesync.Progress) {
			ev := DesyncEvent{Phase: "trying", Index: p.Index, Total: p.Total, Preset: p.Preset}
			if p.Phase == autodesync.PhaseStart {
				ev = DesyncEvent{Phase: "searching", Total: p.Total}
			}
			a.publish(Event{Kind: EventDesync, Desync: ev})
		},
	})
	a.finishSearch(netID, res)
}

// finishSearch ends the trial state, stores the result (when it is conclusive and the network is still the
// same) and lets the reload path put the engine in its final state.
func (a *App) finishSearch(netID string, res autodesync.Result) {
	now := a.d.clk.Now()
	a.mu.Lock()
	sameNet := a.raw.NetworkID == netID
	a.mu.Unlock()

	outcome := string(res.Outcome)
	if (res.Outcome == autodesync.OutcomeChosen || res.Outcome == autodesync.OutcomeNone) && !sameNet {
		outcome = string(autodesync.OutcomeInconclusive) // the network changed while it ran: the result is not for this one
	}
	if outcome == string(autodesync.OutcomeChosen) || outcome == string(autodesync.OutcomeNone) {
		if st := a.dsStoreGet(); st != nil {
			old, _ := st.Get(netID)
			if err := st.Put(netID, a.mergeRecord(old, res, now)); err != nil {
				a.log.Warn("the desync result could not be stored", "err", err)
			}
		}
	}

	// The new states are visible before "searching" turns false.
	a.setTable(a.d.checker.Table())

	sum := &DesyncSearch{At: now, Chosen: res.Chosen, Outcome: outcome}
	for _, t := range res.Tried {
		if !t.Skipped {
			sum.Tried = append(sum.Tried, DesyncTry{Preset: t.Preset, Fixed: t.Fixed, Total: t.Total})
		}
	}
	a.dsMu.Lock()
	a.dsTrial = nil
	if res.Outcome != autodesync.OutcomeCanceled {
		a.dsLast = sum
		if outcome == string(autodesync.OutcomeInconclusive) || res.Outcome == autodesync.OutcomeError {
			a.dsCool = now.Add(a.auto.Backoff)
		}
	}
	a.dsSearch = nil
	a.dsMu.Unlock()

	if res.Outcome == autodesync.OutcomeError {
		a.log.Warn("desync search stopped: the engine could not be put to work", "err", res.Err)
	}
	a.log.Info("desync search ended", "outcome", outcome, "chosen", res.Chosen, "tried", len(sum.Tried))
	a.kickReload()
	done := DesyncEvent{Phase: "done", Outcome: outcome, Chosen: res.Chosen, Fixed: len(res.Fixed), Of: len(res.Domains)}
	a.publish(Event{Kind: EventDesync, Desync: done})
}

// mergeRecord folds a search result into the network's record. Verified sites of an earlier search under the
// same preset that this search did not probe stay verified; under another preset they become pending again.
func (a *App) mergeRecord(old autodesync.Record, res autodesync.Result, now time.Time) autodesync.Record {
	searched := map[string]bool{}
	for _, d := range res.Domains {
		searched[d] = true
	}
	rec := autodesync.Record{Preset: old.Preset, At: now, Failed: map[string]time.Time{}}
	if res.Outcome == autodesync.OutcomeChosen {
		rec.Preset = res.Chosen
	}
	if rec.Preset == old.Preset {
		for _, d := range old.Fixed {
			if !searched[d] {
				rec.Fixed = append(rec.Fixed, d)
			}
		}
	}
	rec.Fixed = append(rec.Fixed, res.Fixed...)
	for d, at := range old.Failed {
		if !searched[d] && now.Sub(at) < a.auto.FailedTTL {
			rec.Failed[d] = at
		}
	}
	for _, d := range res.Failed {
		rec.Failed[d] = now
	}
	sort.Strings(rec.Fixed)
	return rec
}

// stopSearch cancels a running search; wait blocks (bounded) until it has ended. Callers that hold the op
// lock must not wait: the search may be waiting for it.
func (a *App) stopSearch(wait bool) {
	a.dsMu.Lock()
	run := a.dsSearch
	a.dsMu.Unlock()
	if run == nil {
		return
	}
	run.cancel()
	if !wait {
		return
	}
	select {
	case <-run.done:
	case <-time.After(stepTimeout):
		a.log.Warn("the desync search did not stop in time")
	}
}

// trialApplier puts a preset to work for the given sites, serialised with connect, disconnect and reload.
type trialApplier struct{ a *App }

func (t trialApplier) Apply(ctx context.Context, preset string, domains []string) error {
	a := t.a
	if err := a.op.Lock(ctx); err != nil {
		return err
	}
	defer a.op.Unlock()
	if ctx.Err() != nil || a.State() != StateConnected {
		return context.Canceled
	}
	a.dsMu.Lock()
	a.dsTrial = &trialState{preset: preset, domains: append([]string(nil), domains...)}
	a.dsMu.Unlock()
	bins, err := a.resolveBinaries()
	if err != nil {
		return err
	}
	plan, err := a.buildPlan(ctx, bins, a.currentTable())
	if err != nil {
		if errors.Is(err, errPayloadMissing) {
			a.log.Info("desync search skips a preset: a payload file is missing", "preset", preset)
			return autodesync.ErrSkip
		}
		return err
	}
	return a.applyEngine(ctx, plan)
}

// trialProber measures the sites through the real direct path.
type trialProber struct{ a *App }

func (t trialProber) Probe(ctx context.Context, domains []string) (map[string]bool, error) {
	return t.a.d.trialProbe(ctx, domains)
}

// trialTable is the table a trial renders: exactly the trial sites on the desync route.
func trialTable(domains []string) route.Table {
	es := make([]route.Entry, 0, len(domains))
	for _, d := range domains {
		es = append(es, route.Entry{Domain: d, Route: route.Desync, Source: route.SourceChecked})
	}
	t, err := route.NewTable("trial", time.Time{}, es)
	if err != nil {
		return route.Table{}
	}
	return t
}

// trialAnalyzerConfig derives the config of the search's probes from the checker's: at most the configured
// concurrency (never above 2), one probe per address, a jitter of at least 300-900 ms, no SNI swap, one
// attempt, every stage bounded by the probe timeout, and only the control domains (no canaries, no public IP
// lookup) in the calibration.
func trialAnalyzerConfig(base analyzer.Config, auto autodesync.Config) analyzer.Config {
	auto = auto.WithDefaults()
	c := base
	c.Concurrency = max(1, min(base.Concurrency, auto.Concurrency, autodesync.MaxConcurrency))
	c.PerIPConcurrency = 1
	if c.Jitter[1] < 900*time.Millisecond {
		c.Jitter = [2]time.Duration{300 * time.Millisecond, 900 * time.Millisecond}
	}
	c.CanaryDomains = nil
	c.PublicIPEndpoint = ""
	o := c.Options
	o.SNISwap, o.Repeats = false, 1
	t := &o.Timeouts
	for _, d := range []*time.Duration{&t.Resolve, &t.TCP, &t.TLS, &t.HTTP, &t.Bulk} {
		*d = min(*d, auto.ProbeTimeout)
	}
	c.Options = o
	return c
}

// directProbe runs one batch of the trial runner against the direct dialer and returns which sites were
// reached. A failed internet calibration is autodesync.ErrNoInternet: the result says nothing about a preset.
func directProbe(run analyzer.Runner, d analyzer.Dialer) func(context.Context, []string) (map[string]bool, error) {
	return func(ctx context.Context, domains []string) (map[string]bool, error) {
		b := analyzer.Batch{Dialers: []analyzer.Dialer{d}}
		for _, dom := range domains {
			b.Targets = append(b.Targets, analyzer.Target{Domain: dom})
		}
		rep, err := run.Run(ctx, b)
		if err != nil {
			return nil, err
		}
		if rep.Calibration.Internet != analyzer.CheckOK {
			return nil, autodesync.ErrNoInternet
		}
		out := make(map[string]bool, len(rep.Results))
		for _, r := range rep.Results {
			if v := string(r.Verdict); v == "OK" || v == "OK_REDIRECT" {
				out[r.Target.Domain] = true
			}
		}
		return out, nil
	}
}

// ---- live failures ----

// noteLiveFailure counts failures the monitor saw on the desync route for a verified site; as many as the
// checker's own failure threshold within its window send the site back to "pending" and start a search
// (the stored preset is tried first). It never blocks the monitor.
func (a *App) noteLiveFailure(domain string, at time.Time) {
	if mode, _ := a.desyncSelection(); mode != DesyncModeAuto {
		return
	}
	a.mu.Lock()
	raw := a.raw
	state := ""
	var e route.Entry
	var ok bool
	if e, ok = raw.Lookup(domain); ok {
		state = a.dsStates[e.Domain]
	}
	a.mu.Unlock()
	if !ok || e.Route != route.Desync || state != DesyncVerified {
		return
	}
	thr, win := a.cfg.Checker.Schedule.FailThreshold, a.cfg.Checker.Schedule.FailWindow
	if thr <= 0 {
		thr = 3
	}
	if win <= 0 {
		win = 5 * time.Minute
	}
	a.dsMu.Lock()
	if a.dsFails == nil {
		a.dsFails = map[string][]time.Time{}
	}
	kept := a.dsFails[e.Domain][:0:0]
	for _, t := range a.dsFails[e.Domain] {
		if t.After(at.Add(-win)) {
			kept = append(kept, t)
		}
	}
	kept = append(kept, at)
	fire := len(kept) >= thr
	if fire {
		delete(a.dsFails, e.Domain)
	} else {
		a.dsFails[e.Domain] = kept
	}
	a.dsMu.Unlock()
	if fire {
		go a.requeue(raw.NetworkID, e.Domain)
	}
}

// requeue drops the verification of a site, so the next search covers it, and starts that search.
func (a *App) requeue(netID, domain string) {
	if st := a.dsStoreGet(); st != nil {
		if rec, ok := st.Get(netID); ok {
			kept := rec.Fixed[:0:0]
			for _, d := range rec.Fixed {
				if d != domain {
					kept = append(kept, d)
				}
			}
			rec.Fixed = kept
			if err := st.Put(netID, rec); err != nil {
				a.log.Warn("the desync result could not be stored", "err", err)
			}
		}
	}
	a.setTable(a.d.checker.Table())
	a.log.Info("desync: live failures of a verified site; searching again")
	_ = a.startSearch(searchLive)
}
