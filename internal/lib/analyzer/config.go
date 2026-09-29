package analyzer

import (
	"errors"
	"fmt"
	"io"
	"net/url"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/nikoejento/obhod/internal/lib/route"
)

// Config configures the prober and the runner.
type Config struct {
	Concurrency      int              `yaml:"concurrency"`        // default 8
	PerIPConcurrency int              `yaml:"per_ip_concurrency"` // default 1
	Jitter           [2]time.Duration `yaml:"jitter"`             // [min, max] pause before each probe
	Options          Options          `yaml:"options"`            // defaults for batches
	DoH              []string         `yaml:"doh"`                // DoH endpoints
	ControlDomains   []string         `yaml:"control_domains"`    // never-blocked, for calibration
	CanaryDomains    []string         `yaml:"canary_domains"`     // known-blocked, for calibration
	BypassProcesses  []string         `yaml:"bypass_processes"`   // foreign tools to warn about
	NeutralSNI       []string         `yaml:"neutral_sni"`        // for SNI-swap
	// PublicIPEndpoint is an https URL that answers with the caller's IP as plain text, used for the report's
	// network info. Empty (default) disables the lookup: it would tell a third party that this machine is probing.
	PublicIPEndpoint string `yaml:"public_ip_endpoint"`
}

// DefaultConfig returns the configuration used when nothing is specified.
func DefaultConfig() Config {
	return Config{
		Concurrency:      8,
		PerIPConcurrency: 1,
		Jitter:           [2]time.Duration{100 * time.Millisecond, 500 * time.Millisecond},
		Options: Options{
			Stages:      StageAll,
			Fingerprint: "chrome",
			Timeouts: Timeouts{
				Resolve: 5 * time.Second,
				TCP:     5 * time.Second,
				TLS:     6 * time.Second,
				HTTP:    8 * time.Second,
				Bulk:    10 * time.Second,
			},
			Bulk:    BulkOptions{Mode: "download", Size: 64 << 10, StallMin: 12 << 10, StallMax: 36 << 10},
			SNISwap: true,
			Repeats: 2,
		},
		DoH:             []string{"https://1.1.1.1/dns-query", "https://8.8.8.8/dns-query"},
		ControlDomains:  []string{"www.cloudflare.com", "www.wikipedia.org"},
		CanaryDomains:   []string{"www.instagram.com", "rutracker.org"},
		BypassProcesses: []string{"winws", "nfqws", "goodbyedpi", "ciadpi", "byedpi", "sing-box", "xray", "v2ray", "mihomo", "clash", "zapret"},
		NeutralSNI:      []string{"www.microsoft.com", "www.apple.com"},
	}
}

// ParseConfig reads a YAML document over DefaultConfig (absent keys keep their defaults),
// normalizes the domain lists and validates the result. Unknown keys are rejected.
func ParseConfig(r io.Reader) (Config, error) {
	c := DefaultConfig()
	dec := yaml.NewDecoder(r)
	dec.KnownFields(true)
	if err := dec.Decode(&c); err != nil && !errors.Is(err, io.EOF) {
		return Config{}, fmt.Errorf("analyzer config: %w", err)
	}
	if err := c.Validate(); err != nil {
		return Config{}, err
	}
	return c, nil
}

// Validate checks c and normalizes its domain lists in place.
func (c *Config) Validate() error {
	fail := func(f string, a ...any) error { return fmt.Errorf("analyzer config: "+f, a...) }
	if c.Concurrency < 1 {
		return fail("concurrency must be >= 1")
	}
	if c.PerIPConcurrency < 1 {
		return fail("per_ip_concurrency must be >= 1")
	}
	if c.Jitter[0] < 0 || c.Jitter[1] < c.Jitter[0] {
		return fail("jitter must be [min, max] with 0 <= min <= max")
	}
	if err := c.Options.validate(); err != nil {
		return err
	}
	for _, e := range c.DoH {
		u, err := url.Parse(e)
		if err != nil || u.Scheme != "https" || u.Host == "" {
			return fail("doh endpoint %q must be an https URL", e)
		}
	}
	if c.PublicIPEndpoint != "" {
		if u, err := url.Parse(c.PublicIPEndpoint); err != nil || u.Scheme != "https" || u.Host == "" {
			return fail("public_ip_endpoint %q must be an https URL", c.PublicIPEndpoint)
		}
	}
	lists := []struct {
		name string
		list []string
	}{{"control_domains", c.ControlDomains}, {"canary_domains", c.CanaryDomains}, {"neutral_sni", c.NeutralSNI}}
	for _, l := range lists {
		for i, d := range l.list {
			n, err := route.Normalize(d)
			if err != nil {
				return fail("%s: %v", l.name, err)
			}
			l.list[i] = n
		}
	}
	return nil
}

func (o Options) validate() error {
	fail := func(f string, a ...any) error { return fmt.Errorf("analyzer options: "+f, a...) }
	if o.Stages == 0 || o.Stages&^StageAll != 0 {
		return fail("stages must be a non-empty subset of tcp,tls,http,bulk")
	}
	switch o.Fingerprint {
	case "chrome", "firefox", "safari", "go":
	default:
		return fail("unknown fingerprint %q", o.Fingerprint)
	}
	if o.Repeats < 1 {
		return fail("repeats must be >= 1")
	}
	t := o.Timeouts
	for _, d := range []time.Duration{t.Resolve, t.TCP, t.TLS, t.HTTP, t.Bulk} {
		if d <= 0 {
			return fail("timeouts must be positive")
		}
	}
	b := o.Bulk
	if b.Mode != "download" && b.Mode != "upload" {
		return fail("bulk mode must be download or upload")
	}
	if b.StallMin <= 0 || b.StallMax <= b.StallMin || int64(b.Size) <= b.StallMax {
		return fail("bulk needs 0 < stall_min < stall_max < size")
	}
	return nil
}
