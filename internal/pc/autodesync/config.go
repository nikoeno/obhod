package autodesync

import (
	"fmt"
	"time"
)

// Hard limits. A search is a burst of probes at sites the user listed, so
// these are caps, not just defaults: Validate refuses more.
const (
	// MaxConcurrency is the most probes a search runs at once.
	MaxConcurrency = 2
	maxPresets     = 30
	maxDomains     = 20
)

// Config tunes a search. Zero fields take their defaults (see DefaultConfig);
// the YAML keys are the snake_case names.
type Config struct {
	// MaxPresets is the most presets one search tries (skipped ones, whose
	// files are missing, do not count). Default 6.
	MaxPresets int `yaml:"max_presets"`
	// Settle is the wait after the engine was restarted with a preset and before the first probe. Default 2s.
	Settle time.Duration `yaml:"settle"`
	// Pause is [min, max]: a random pause between two presets. Default [1s, 3s].
	Pause [2]time.Duration `yaml:"pause"`
	// Budget is the time cap of one search; checked between presets, so the preset in progress still finishes
	// (its probes have their own timeouts). Default 3m.
	Budget time.Duration `yaml:"budget"`
	// MaxDomains is the most sites one search probes. Default 8.
	MaxDomains int `yaml:"max_domains"`
	// ProbeTimeout caps every stage of one probe (TCP connect, TLS, HTTP, bulk). Default 8s.
	ProbeTimeout time.Duration `yaml:"probe_timeout"`
	// Concurrency is the number of probes at once, 1 or 2. Default 2.
	Concurrency int `yaml:"concurrency"`
	// Backoff is the wait before an automatic search may start again after a search that could not conclude
	// (the engine did not start, the internet check failed) or after a live-failure search. Default 10m.
	Backoff time.Duration `yaml:"backoff"`
	// FailedTTL is how long a site that no preset fixed stays on the tunnel route before a search may try it
	// again by itself. Default 24h.
	FailedTTL time.Duration `yaml:"failed_ttl"`
}

// DefaultConfig returns the safe defaults.
func DefaultConfig() Config {
	return Config{
		MaxPresets:   6,
		Settle:       2 * time.Second,
		Pause:        [2]time.Duration{time.Second, 3 * time.Second},
		Budget:       3 * time.Minute,
		MaxDomains:   8,
		ProbeTimeout: 8 * time.Second,
		Concurrency:  2,
		Backoff:      10 * time.Minute,
		FailedTTL:    24 * time.Hour,
	}
}

// WithDefaults fills the zero fields.
func (c Config) WithDefaults() Config {
	d := DefaultConfig()
	if c.MaxPresets == 0 {
		c.MaxPresets = d.MaxPresets
	}
	if c.Settle == 0 {
		c.Settle = d.Settle
	}
	if c.Pause == ([2]time.Duration{}) {
		c.Pause = d.Pause
	}
	if c.Budget == 0 {
		c.Budget = d.Budget
	}
	if c.MaxDomains == 0 {
		c.MaxDomains = d.MaxDomains
	}
	if c.ProbeTimeout == 0 {
		c.ProbeTimeout = d.ProbeTimeout
	}
	if c.Concurrency == 0 {
		c.Concurrency = d.Concurrency
	}
	if c.Backoff == 0 {
		c.Backoff = d.Backoff
	}
	if c.FailedTTL == 0 {
		c.FailedTTL = d.FailedTTL
	}
	return c
}

// Validate checks the ranges (after the defaults are filled).
func (c Config) Validate() error {
	c = c.WithDefaults()
	bad := func(f string, a ...any) error { return fmt.Errorf("desync_auto: "+f, a...) }
	switch {
	case c.MaxPresets < 1 || c.MaxPresets > maxPresets:
		return bad("max_presets must be 1 to %d", maxPresets)
	case c.Settle < 0 || c.Settle > 30*time.Second:
		return bad("settle must be 0 to 30s")
	case c.Pause[0] < 0 || c.Pause[1] < c.Pause[0] || c.Pause[1] > time.Minute:
		return bad("pause must be [min, max] with 0 <= min <= max <= 1m")
	case c.Budget < 10*time.Second || c.Budget > 30*time.Minute:
		return bad("budget must be 10s to 30m")
	case c.MaxDomains < 1 || c.MaxDomains > maxDomains:
		return bad("max_domains must be 1 to %d", maxDomains)
	case c.ProbeTimeout < time.Second || c.ProbeTimeout > time.Minute:
		return bad("probe_timeout must be 1s to 1m")
	case c.Concurrency < 1 || c.Concurrency > MaxConcurrency:
		return bad("concurrency must be 1 or %d (probes at censored sites stay polite)", MaxConcurrency)
	case c.Backoff < 0 || c.Backoff > 24*time.Hour:
		return bad("backoff must be 0 to 24h")
	case c.FailedTTL < time.Minute:
		return bad("failed_ttl must be at least 1m")
	}
	return nil
}
