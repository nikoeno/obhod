package desktop

import (
	"context"
	"sync"

	"github.com/nikoejento/obhod/internal/pc"
)

// desyncState is the fake's desync choice. It mirrors the rules of pc: a catalogue id, auto or custom;
// a search needs a connection, mode auto and a site routed to desync.
type desyncState struct {
	mu        sync.Mutex
	mode      pc.DesyncMode
	preset    string
	searching bool
	last      *pc.DesyncSearch
	noDomains bool
	lastSet   string
	err       map[string]error
}

func (f *fakeEngine) failDesyncCall(method string, err error) {
	f.desy.mu.Lock()
	if f.desy.err == nil {
		f.desy.err = map[string]error{}
	}
	f.desy.err[method] = err
	f.desy.mu.Unlock()
}

func (f *fakeEngine) desyncErr(method string) error {
	f.desy.mu.Lock()
	defer f.desy.mu.Unlock()
	return f.desy.err[method]
}

var fakePresets = []pc.DesyncPreset{
	{ID: "alt2-multisplit", Name: "ALT2 (multisplit)", Description: "Splits the first packet and hides a decoy prefix in front of it."},
	{ID: "alt3-hostfakesplit", Name: "ALT3 (host fake split)", Description: "Sends a fake hello, then splits the real one around a fake host name."},
}

func (f *fakeEngine) Desync() pc.DesyncStatus {
	f.call("Desync")
	f.desy.mu.Lock()
	defer f.desy.mu.Unlock()
	st := pc.DesyncStatus{Mode: f.desy.mode, Preset: f.desy.preset, Searching: f.desy.searching}
	if st.Mode == "" {
		st.Mode, st.Preset = pc.DesyncModePreset, "alt2-multisplit"
	}
	if f.desy.last != nil {
		l := *f.desy.last
		st.LastSearch = &l
	}
	return st
}

func (f *fakeEngine) DesyncPresets() []pc.DesyncPreset {
	f.call("DesyncPresets")
	return append([]pc.DesyncPreset{}, fakePresets...)
}

func (f *fakeEngine) SetDesync(_ context.Context, preset string) error {
	f.call("SetDesync " + preset)
	f.desy.mu.Lock()
	f.desy.lastSet = preset
	f.desy.mu.Unlock()
	if err := f.desyncErr("SetDesync"); err != nil {
		return err
	}
	f.desy.mu.Lock()
	defer f.desy.mu.Unlock()
	if preset == "auto" {
		f.desy.mode, f.desy.preset = pc.DesyncModeAuto, "alt2-multisplit"
		return nil
	}
	for _, p := range fakePresets {
		if p.ID == preset {
			f.desy.mode, f.desy.preset = pc.DesyncModePreset, preset
			return nil
		}
	}
	return pc.ErrDesyncPreset
}

func (f *fakeEngine) StartDesyncSearch() error {
	f.call("StartDesyncSearch")
	if err := f.desyncErr("StartDesyncSearch"); err != nil {
		return err
	}
	f.mu.Lock()
	connected := f.state == pc.StateConnected
	f.mu.Unlock()
	f.desy.mu.Lock()
	defer f.desy.mu.Unlock()
	switch {
	case !connected || f.desy.searching:
		return pc.ErrDesyncBusy
	case f.desy.mode != pc.DesyncModeAuto:
		return pc.ErrDesyncNotAuto
	case f.desy.noDomains:
		return pc.ErrDesyncNoDomain
	}
	f.desy.searching = true
	return nil
}
