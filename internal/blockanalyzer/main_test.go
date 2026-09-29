package blockanalyzer

import (
	"bytes"
	"context"
	"errors"
	"flag"
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/nikoejento/obhod/internal/lib/analyzer"
)

var update = flag.Bool("update", false, "rewrite golden files")

func sample() analyzer.Report {
	at := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	return analyzer.Report{
		Schema: 1, Version: "dev", StartedAt: at,
		Network:     analyzer.NetworkInfo{PublicIP: netip.MustParseAddr("203.0.113.5"), ASN: analyzer.ASN{Number: 64500, Name: "EXAMPLE-NET, ZZ", Country: "ZZ"}},
		Calibration: analyzer.Calibration{Internet: analyzer.CheckOK, DPIPresent: analyzer.CheckYes, Warnings: []string{"foreign bypass tool running: winws (pid 4242)"}},
		Results: []analyzer.ProbeResult{
			{Target: analyzer.Target{Domain: "example.com"}, Route: "direct", IP: netip.MustParseAddr("93.184.216.34"), Verdict: "OK", Blocking: "none", Stage: "bulk",
				Evidence: analyzer.Evidence{TLSVersion: "TLS 1.3", HTTPStatus: 200, RxBytes: 70000, TxBytes: 900}, Suggestion: "direct"},
			{Target: analyzer.Target{Domain: "blocked.example"}, Route: "direct", IP: netip.MustParseAddr("198.51.100.7"), Verdict: "TLS_RST", Blocking: "confirmed", Stage: "tls",
				Evidence:   analyzer.Evidence{Err: "read tcp: connection reset by peer", TxBytes: 517, SNISwap: &analyzer.SNISwapResult{SNI: "www.microsoft.com", Verdict: "OK"}},
				Suggestion: "desync", SuggestionNote: "only the SNI is blocked"},
			{Target: analyzer.Target{Domain: "nx.example"}, Route: "direct", Verdict: "DNS_FAIL", Blocking: "unknown", Stage: "resolve",
				Evidence: analyzer.Evidence{Err: "lookup nx.example:\n no such domain"}},
		},
		Warnings: []string{"example warning"},
	}
}

type fakeRunner struct {
	rep   analyzer.Report
	err   error
	got   analyzer.Batch
	calls int
}

func (f *fakeRunner) Run(_ context.Context, b analyzer.Batch) (analyzer.Report, error) {
	f.calls++
	f.got = b
	return f.rep, f.err
}

type harness struct {
	app    app
	runner *fakeRunner
	cfg    analyzer.Config
}

func newHarness(rep analyzer.Report) *harness {
	h := &harness{runner: &fakeRunner{rep: rep}}
	h.app = app{
		newRunner: func(cfg analyzer.Config, _ analyzer.Deps) (analyzer.Runner, error) { h.cfg = cfg; return h.runner, nil },
		newDialer: func() (analyzer.Dialer, error) { return analyzer.NewFakeDialer(), nil },
	}
	return h
}

func (h *harness) run(args ...string) (int, string, string) {
	var out, errb bytes.Buffer
	code := h.app.run(context.Background(), args, &out, &errb)
	return code, out.String(), errb.String()
}

func golden(t *testing.T, name, got string) {
	t.Helper()
	path := filepath.Join("testdata", name)
	if *update {
		if err := os.MkdirAll("testdata", 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(got), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("%v (run with -update to create it)", err)
	}
	if strings.ReplaceAll(string(want), "\r\n", "\n") != got {
		t.Fatalf("output differs from %s (run with -update after checking the change)\n--- got ---\n%s", path, got)
	}
}

func TestTableGolden(t *testing.T) {
	var b bytes.Buffer
	render(&b, sample(), false)
	golden(t, "table.golden.txt", b.String())
	b.Reset()
	render(&b, sample(), true)
	golden(t, "table_verbose.golden.txt", b.String())
}

func TestRenderCalibrateOnly(t *testing.T) {
	var b bytes.Buffer
	render(&b, analyzer.Report{Calibration: analyzer.Calibration{Internet: analyzer.CheckFailed, DPIPresent: analyzer.CheckUnknown}}, false)
	if b.String() != "calibration: internet=failed dpi=unknown\n" {
		t.Fatalf("%q", b.String())
	}
}

func TestRunTableAndTargets(t *testing.T) {
	h := newHarness(sample())
	code, out, errs := h.run("Example.com", "blocked.example")
	if code != exitOK || errs != "" || !strings.Contains(out, "blocked.example") || !strings.Contains(out, "TLS_RST") {
		t.Fatalf("%d %q %q", code, out, errs)
	}
	if len(h.runner.got.Targets) != 2 || h.runner.got.Targets[0].Domain != "Example.com" || len(h.runner.got.Dialers) != 1 {
		t.Fatalf("%+v", h.runner.got)
	}
}

func TestRunFlagsOverrideConfig(t *testing.T) {
	h := newHarness(sample())
	code, _, errs := h.run("--stages", "tcp,tls", "--fingerprint", "firefox", "--repeats", "3", "--concurrency", "4", "a.test")
	if code != exitOK {
		t.Fatalf("%d %s", code, errs)
	}
	c := h.cfg
	if c.Options.Stages != analyzer.StageTCP|analyzer.StageTLS || c.Options.Fingerprint != "firefox" || c.Options.Repeats != 3 || c.Concurrency != 4 {
		t.Fatalf("%+v", c)
	}
	if h.runner.got.Options.Stages != c.Options.Stages {
		t.Fatalf("batch options not passed: %+v", h.runner.got.Options)
	}
}

func TestRunJSON(t *testing.T) {
	h := newHarness(sample())
	code, out, _ := h.run("--json", "-", "a.test")
	if code != exitOK || !strings.HasPrefix(out, "{\n  \"schema\": 1,") || strings.Contains(out, "DOMAIN") {
		t.Fatalf("%d %q", code, out)
	}
	if _, err := analyzer.DecodeReport(strings.NewReader(out)); err != nil {
		t.Fatalf("stdout JSON does not decode: %v", err)
	}

	path := filepath.Join(t.TempDir(), "r.json")
	code, out, _ = h.run("--json", path, "a.test")
	if code != exitOK || !strings.Contains(out, "DOMAIN") {
		t.Fatalf("%d %q", code, out)
	}
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if rep, err := analyzer.DecodeReport(bytes.NewReader(b)); err != nil || len(rep.Results) != 3 {
		t.Fatalf("%v", err)
	}
}

func TestRunDomainFileAndConfig(t *testing.T) {
	dir := t.TempDir()
	list := filepath.Join(dir, "domains.txt")
	os.WriteFile(list, []byte("# comment\nb.test\n\n  c.test  # trailing\n"), 0o644)
	cfgPath := filepath.Join(dir, "cfg.yaml")
	os.WriteFile(cfgPath, []byte("splitter:\n  anything: 1\nanalyzer:\n  concurrency: 2\n  options:\n    repeats: 5\n"), 0o644)

	h := newHarness(sample())
	code, _, errs := h.run("-f", list, "--config", cfgPath, "a.test")
	if code != exitOK {
		t.Fatalf("%d %s", code, errs)
	}
	var names []string
	for _, tg := range h.runner.got.Targets {
		names = append(names, tg.Domain)
	}
	if strings.Join(names, ",") != "a.test,b.test,c.test" {
		t.Fatalf("%v", names)
	}
	if h.cfg.Concurrency != 2 || h.cfg.Options.Repeats != 5 || h.cfg.Options.Fingerprint != "chrome" {
		t.Fatalf("config not applied over defaults: %+v", h.cfg)
	}
}

func TestRunCalibrateOnly(t *testing.T) {
	h := newHarness(analyzer.Report{StartedAt: time.Now(), Calibration: analyzer.Calibration{Internet: analyzer.CheckOK, DPIPresent: analyzer.CheckNo}})
	code, out, _ := h.run("--calibrate-only")
	if code != exitOK || len(h.runner.got.Targets) != 0 || !strings.Contains(out, "dpi=no") {
		t.Fatalf("%d %q %+v", code, out, h.runner.got)
	}
}

func TestExitCodes(t *testing.T) {
	failed := sample()
	failed.Calibration.Internet = analyzer.CheckFailed
	if code, _, _ := newHarness(failed).run("a.test"); code != exitCalibration {
		t.Errorf("calibration failed: exit %d", code)
	}
	for name, args := range map[string][]string{
		"no domains":      {},
		"bad flag":        {"--bogus"},
		"bad stage":       {"--stages", "udp", "a.test"},
		"bad fingerprint": {"--fingerprint", "opera", "a.test"},
		"missing file":    {"-f", filepath.Join(t.TempDir(), "nope.txt")},
		"missing config":  {"--config", filepath.Join(t.TempDir(), "nope.yaml"), "a.test"},
		"bad repeats":     {"--repeats", "-1", "a.test"},
	} {
		h := newHarness(sample())
		code, _, errs := h.run(args...)
		if code != exitUsage || errs == "" || h.runner.calls != 0 {
			t.Errorf("%s: exit %d, stderr %q, runner calls %d", name, code, errs, h.runner.calls)
		}
	}
	// A runner refusing the batch (nothing produced) is a usage/config error.
	h := newHarness(analyzer.Report{})
	h.runner.err = errors.New("invalid batch")
	if code, _, errs := h.run("a.test"); code != exitUsage || !strings.Contains(errs, "invalid batch") {
		t.Errorf("invalid batch: %d %q", code, errs)
	}
	// An interrupted run still prints the partial report.
	h = newHarness(sample())
	h.runner.err = context.Canceled
	if code, out, errs := h.run("a.test"); code != exitUsage || !strings.Contains(out, "example.com") || !strings.Contains(errs, "interrupted") {
		t.Errorf("interrupted: %d %q %q", code, out, errs)
	}
}

func TestVersionAndHelp(t *testing.T) {
	h := newHarness(sample())
	if code, out, _ := h.run("--version"); code != exitOK || !strings.HasPrefix(out, "blockanalyzer ") {
		t.Errorf("--version: %d %q", code, out)
	}
	for _, flagName := range []string{"--help", "-h"} {
		if code, out, _ := h.run(flagName); code != exitOK || !strings.Contains(out, "usage: blockanalyzer") {
			t.Errorf("%s: %d %q", flagName, code, out)
		}
	}
	if h.runner.calls != 0 {
		t.Error("runner started for --version/--help")
	}
}
