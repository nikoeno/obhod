package analyzer

import (
	"bytes"
	"reflect"
	"strings"
	"testing"
	"time"

	"gopkg.in/yaml.v3"
)

func TestConfigRoundTrip(t *testing.T) {
	want := DefaultConfig()
	if err := want.Validate(); err != nil {
		t.Fatal(err)
	}
	b, err := yaml.Marshal(want)
	if err != nil {
		t.Fatal(err)
	}
	got, err := ParseConfig(bytes.NewReader(b))
	if err != nil {
		t.Fatalf("%v\n%s", err, b)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("round trip differs\n got %+v\nwant %+v", got, want)
	}
}

func TestParseConfigOverridesAndDefaults(t *testing.T) {
	c, err := ParseConfig(strings.NewReader(`
concurrency: 3
jitter: [0s, 50ms]
options:
  stages: tcp,tls
  fingerprint: firefox
  timeouts: {tcp: 2s}
control_domains: [WWW.Example.COM.]
`))
	if err != nil {
		t.Fatal(err)
	}
	if c.Concurrency != 3 || c.PerIPConcurrency != 1 || c.Jitter[1] != 50*time.Millisecond {
		t.Errorf("%+v", c)
	}
	if c.Options.Stages != StageTCP|StageTLS || c.Options.Fingerprint != "firefox" || c.Options.Timeouts.TCP != 2*time.Second || c.Options.Timeouts.TLS == 0 {
		t.Errorf("%+v", c.Options)
	}
	if !c.Options.SNISwap || c.ControlDomains[0] != "www.example.com" {
		t.Errorf("%+v", c)
	}
}

func TestParseConfigEmptyIsDefault(t *testing.T) {
	c, err := ParseConfig(strings.NewReader(""))
	if err != nil || !reflect.DeepEqual(c, DefaultConfig()) {
		t.Fatalf("%v", err)
	}
}

func TestParseConfigErrors(t *testing.T) {
	for name, doc := range map[string]string{
		"unknown key": "bogus: 1",
		"bad conc":    "concurrency: 0",
		"bad jitter":  "jitter: [2s, 1s]",
		"bad fp":      "options: {fingerprint: opera}",
		"bad stage":   "options: {stages: udp}",
		"bad doh":     "doh: [http://x]",
		"bad domain":  "canary_domains: ['1.2.3.4']",
		"bad bulk":    "options: {bulk: {stall_min: 100, stall_max: 50}}",
		"bad repeats": "options: {repeats: 0}",
	} {
		if _, err := ParseConfig(strings.NewReader(doc)); err == nil {
			t.Errorf("%s: want error", name)
		}
	}
}

func TestStageSet(t *testing.T) {
	s, err := ParseStageSet("bulk")
	if err != nil || s != StageAll {
		t.Fatalf("%v %v", s, err)
	}
	if s.String() != "tcp,tls,http,bulk" {
		t.Fatal(s.String())
	}
	if _, err := ParseStageSet("x"); err == nil {
		t.Fatal("want error")
	}
}

func TestTargetNormalized(t *testing.T) {
	tg, err := Target{Domain: "Example.com."}.Normalized()
	if err != nil || tg.Domain != "example.com" || tg.Port != 443 || tg.Path != "/" {
		t.Fatalf("%+v %v", tg, err)
	}
	if _, err := (Target{Domain: "https://x"}).Normalized(); err == nil {
		t.Fatal("want error")
	}
}
