package analyzer

import (
	"fmt"
	"net/netip"
	"strings"
	"time"

	"github.com/nikoejento/obhod/internal/lib/analyzer/lib/verdict"
	"github.com/nikoejento/obhod/internal/lib/route"
)

// Re-exported from lib/verdict (children can't import this root package).
type (
	// Verdict is the typed outcome of a probe.
	Verdict = verdict.Verdict
	// Stage is the probe stage at which an outcome was observed.
	Stage = verdict.Stage
	// Blocking says how sure we are that the target is blocked.
	Blocking = verdict.Blocking
	// Evidence is the raw material behind a verdict.
	Evidence = verdict.Evidence
	// SNISwapResult is the outcome of repeating the handshake with a neutral SNI.
	SNISwapResult = verdict.SNISwapResult
)

// Target is what to probe.
type Target struct {
	Domain   string     `json:"domain"`              // normalized via route.Normalize
	Port     uint16     `json:"port,omitempty"`      // default 443
	Path     string     `json:"path,omitempty"`      // default "/"
	BulkPath string     `json:"bulk_path,omitempty"` // optional large object for download-mode bulk test
	IP       netip.Addr `json:"ip,omitzero"`         // optional: skip resolve
}

// Normalized returns t with Domain normalized and defaults applied.
func (t Target) Normalized() (Target, error) {
	d, err := route.Normalize(t.Domain)
	if err != nil {
		return t, err
	}
	t.Domain = d
	if t.Port == 0 {
		t.Port = 443
	}
	if t.Path == "" {
		t.Path = "/"
	}
	return t, nil
}

// StageSet is a bit set of optional probe stages. Resolve and verify always run.
type StageSet uint8

// The optional stages.
const (
	StageTCP StageSet = 1 << iota
	StageTLS
	StageHTTP
	StageBulk

	// StageAll is every optional stage.
	StageAll = StageTCP | StageTLS | StageHTTP | StageBulk
)

var stageNames = []struct {
	bit  StageSet
	name string
}{{StageTCP, "tcp"}, {StageTLS, "tls"}, {StageHTTP, "http"}, {StageBulk, "bulk"}}

// Has reports whether every stage of o is in s.
func (s StageSet) Has(o StageSet) bool { return s&o == o }

// String returns the comma-separated stage list, e.g. "tcp,tls".
func (s StageSet) String() string {
	var out []string
	for _, n := range stageNames {
		if s.Has(n.bit) {
			out = append(out, n.name)
		}
	}
	return strings.Join(out, ",")
}

// ParseStageSet parses a comma-separated stage list. A later stage implies the earlier ones it needs
// (tls needs tcp, http needs tls, bulk needs http), so "bulk" alone means all four.
func ParseStageSet(s string) (StageSet, error) {
	var set StageSet
	for _, p := range strings.Split(s, ",") {
		p = strings.ToLower(strings.TrimSpace(p))
		if p == "" {
			continue
		}
		ok := false
		for _, n := range stageNames {
			if n.name == p {
				set |= n.bit
				ok = true
			}
		}
		if !ok {
			return 0, fmt.Errorf("unknown stage %q", p)
		}
	}
	for i := len(stageNames) - 1; i > 0; i-- {
		if set.Has(stageNames[i].bit) {
			set |= stageNames[i-1].bit
		}
	}
	return set, nil
}

// MarshalText implements encoding.TextMarshaler (JSON and YAML use the list form).
func (s StageSet) MarshalText() ([]byte, error) { return []byte(s.String()), nil }

// UnmarshalText implements encoding.TextUnmarshaler.
func (s *StageSet) UnmarshalText(b []byte) error {
	v, err := ParseStageSet(string(b))
	*s = v
	return err
}

// Timeouts are per-stage deadlines.
type Timeouts struct {
	Resolve time.Duration `yaml:"resolve" json:"resolve"`
	TCP     time.Duration `yaml:"tcp" json:"tcp"`
	TLS     time.Duration `yaml:"tls" json:"tls"`
	HTTP    time.Duration `yaml:"http" json:"http"`
	Bulk    time.Duration `yaml:"bulk" json:"bulk"`
}

// BulkOptions tune the 16–20 KB test (DESIGN §4.1).
type BulkOptions struct {
	Mode     string `yaml:"mode" json:"mode"`           // "download" (default) | "upload"
	Size     int    `yaml:"size" json:"size"`           // bytes to transfer, default 65536
	StallMin int64  `yaml:"stall_min" json:"stall_min"` // stall window start in bytes, default 12288
	StallMax int64  `yaml:"stall_max" json:"stall_max"` // stall window end in bytes, default 36864
}

// Options are the per-probe settings.
type Options struct {
	Stages      StageSet    `yaml:"stages" json:"stages"`
	Fingerprint string      `yaml:"fingerprint" json:"fingerprint"` // "chrome" (default) | "firefox" | "safari"; "go" only in tests
	Timeouts    Timeouts    `yaml:"timeouts" json:"timeouts"`
	Bulk        BulkOptions `yaml:"bulk" json:"bulk"`
	SNISwap     bool        `yaml:"sni_swap" json:"sni_swap"` // default true
	Repeats     int         `yaml:"repeats" json:"repeats"`   // default 2; majority vote
}

// ProbeResult is the outcome of probing one target through one dialer.
type ProbeResult struct {
	Target         Target        `json:"target"`
	Route          route.Route   `json:"route"`
	IP             netip.Addr    `json:"ip,omitzero"`
	Verdict        Verdict       `json:"verdict"`
	Blocking       Blocking      `json:"blocking"`
	Stage          Stage         `json:"stage"`
	Evidence       Evidence      `json:"evidence"`
	Suggestion     route.Route   `json:"suggestion,omitempty"` // a hint, filled by the Runner
	SuggestionNote string        `json:"suggestion_note,omitempty"`
	StartedAt      time.Time     `json:"started_at"`
	Duration       time.Duration `json:"duration"`
}
