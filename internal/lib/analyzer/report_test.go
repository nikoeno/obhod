package analyzer

import (
	"bytes"
	"flag"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/nikoejento/obhod/internal/lib/analyzer/lib/verdict"
	"github.com/nikoejento/obhod/internal/lib/route"
	"net/netip"
)

var update = flag.Bool("update", false, "rewrite golden files")

func sampleReport() Report {
	at := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	return Report{
		Version:   "v0.1.0",
		StartedAt: at,
		Network: NetworkInfo{
			PublicIP: netip.MustParseAddr("203.0.113.5"),
			ASN:      ASN{Number: 64500, Name: "EXAMPLE-NET, ZZ", Country: "ZZ", Prefix: netip.MustParsePrefix("203.0.113.0/24")},
		},
		Calibration: Calibration{Internet: CheckOK, DPIPresent: CheckYes, Warnings: []string{"foreign bypass tool running: winws (pid 4242)"}},
		Results: []ProbeResult{
			{
				Target: Target{Domain: "example.com", Port: 443, Path: "/"}, Route: route.Direct, IP: netip.MustParseAddr("93.184.216.34"),
				Verdict: verdict.OK, Blocking: verdict.BlockingNone, Stage: verdict.StageBulk,
				Evidence: Evidence{TLSVersion: "TLS 1.3", CertSubject: "CN=example.com", HTTPStatus: 200, RxBytes: 70000, TxBytes: 900,
					RTT: 12 * time.Millisecond, StageTimes: map[Stage]time.Duration{verdict.StageTCP: 12 * time.Millisecond}},
				Suggestion: route.Direct, StartedAt: at, Duration: 250 * time.Millisecond,
			},
			{
				Target: Target{Domain: "blocked.example", Port: 443, Path: "/"}, Route: route.Direct, IP: netip.MustParseAddr("198.51.100.7"),
				Verdict: verdict.TLSRST, Blocking: verdict.BlockingConfirmed, Stage: verdict.StageTLS,
				Evidence: Evidence{Err: "read tcp: connection reset by peer [syscall.Errno]", TxBytes: 517,
					SNISwap: &verdict.SNISwapResult{SNI: "www.microsoft.com", Verdict: verdict.OK}},
				Suggestion: route.Desync, SuggestionNote: "only the SNI is blocked", StartedAt: at, Duration: 40 * time.Millisecond,
			},
			{
				Target: Target{Domain: "nx.example", Port: 443, Path: "/"}, Route: route.Direct,
				Verdict: verdict.DNSFail, Blocking: verdict.BlockingUnknown, Stage: verdict.StageResolve,
				Evidence: Evidence{Err: "lookup nx.example: no such domain"}, StartedAt: at, Duration: time.Millisecond,
			},
		},
		Warnings: []string{"example warning"},
	}
}

func TestReportGolden(t *testing.T) {
	var buf bytes.Buffer
	if err := EncodeReport(&buf, sampleReport()); err != nil {
		t.Fatal(err)
	}
	golden := filepath.Join("testdata", "report.golden.json")
	if *update {
		if err := os.WriteFile(golden, buf.Bytes(), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(golden)
	if err != nil {
		t.Fatalf("%v (run with -update to create it)", err)
	}
	if !bytes.Equal(bytes.ReplaceAll(want, []byte("\r\n"), []byte("\n")), buf.Bytes()) {
		t.Fatalf("report differs from %s (run with -update after checking the change)\n%s", golden, buf.String())
	}
}

func TestReportRoundTrip(t *testing.T) {
	var buf bytes.Buffer
	in := sampleReport()
	if err := EncodeReport(&buf, in); err != nil {
		t.Fatal(err)
	}
	out, err := DecodeReport(&buf)
	if err != nil {
		t.Fatal(err)
	}
	in.Schema = ReportSchema
	if !reflect.DeepEqual(in, out) {
		t.Fatalf("round trip differs\n in %+v\nout %+v", in, out)
	}
}

func TestReportEmpty(t *testing.T) {
	var buf bytes.Buffer
	if err := EncodeReport(&buf, Report{}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(buf.String(), `"results": []`) {
		t.Fatalf("empty results must encode as []:\n%s", buf.String())
	}
	if strings.Contains(buf.String(), "public_ip") || strings.Contains(buf.String(), `"asn"`) && strings.Contains(buf.String(), "number") {
		t.Fatalf("empty network info must be omitted:\n%s", buf.String())
	}
}

func TestDecodeReportRejects(t *testing.T) {
	for name, doc := range map[string]string{
		"garbage":         "not json",
		"wrong schema":    `{"schema": 2, "results": []}`,
		"missing schema":  `{"results": []}`,
		"unknown verdict": `{"schema": 1, "results": [{"verdict": "BOGUS", "blocking": "none"}]}`,
		"unknown level":   `{"schema": 1, "results": [{"verdict": "OK", "blocking": "maybe"}]}`,
	} {
		if _, err := DecodeReport(strings.NewReader(doc)); err == nil {
			t.Errorf("%s: want error", name)
		}
	}
}

func TestSuggestRoute(t *testing.T) {
	swap := func(v Verdict) *verdict.SNISwapResult { return &verdict.SNISwapResult{SNI: "n", Verdict: v} }
	cases := []struct {
		name string
		r    ProbeResult
		want route.Route
		note string
	}{
		{"ok", ProbeResult{Verdict: verdict.OK}, route.Direct, ""},
		{"redirect", ProbeResult{Verdict: verdict.OKRedirect}, route.Direct, ""},
		{"tls rst + swap ok", ProbeResult{Verdict: verdict.TLSRST, Evidence: Evidence{SNISwap: swap(verdict.OK)}}, route.Desync, "SNI"},
		{"tls timeout + swap ok", ProbeResult{Verdict: verdict.TLSTimeout, Evidence: Evidence{SNISwap: swap(verdict.OK)}}, route.Desync, ""},
		{"tls spoof + swap ok", ProbeResult{Verdict: verdict.TLSSpoof, Evidence: Evidence{SNISwap: swap(verdict.OK)}}, route.Desync, ""},
		{"tls alert + swap ok", ProbeResult{Verdict: verdict.TLSAlert, Evidence: Evidence{SNISwap: swap(verdict.OKRedirect)}}, route.Desync, ""},
		{"tls timeout + swap alert", ProbeResult{Verdict: verdict.TLSTimeout, Evidence: Evidence{SNISwap: swap(verdict.TLSAlert)}}, route.Desync, "SNI"},
		{"tls alert + swap alert", ProbeResult{Verdict: verdict.TLSAlert, Evidence: Evidence{SNISwap: swap(verdict.TLSAlert)}}, route.Tunnel, "swap"},
		{"tls rst, swap failed", ProbeResult{Verdict: verdict.TLSRST, Evidence: Evidence{SNISwap: swap(verdict.TLSRST)}}, route.Tunnel, "swap"},
		{"tls rst, no swap", ProbeResult{Verdict: verdict.TLSRST}, route.Tunnel, ""},
		{"tcp timeout", ProbeResult{Verdict: verdict.TCPTimeout}, route.Tunnel, ""},
		{"tcp refused", ProbeResult{Verdict: verdict.TCPRefused}, route.Tunnel, ""},
		{"stall", ProbeResult{Verdict: verdict.Stall1620}, route.Tunnel, ""},
		{"mitm", ProbeResult{Verdict: verdict.TLSMITM}, route.Tunnel, ""},
		{"stub", ProbeResult{Verdict: verdict.HTTPStub}, route.Tunnel, ""},
		{"poisoned", ProbeResult{Verdict: verdict.DNSPoisoned}, route.Direct, "DoH"},
		{"local", ProbeResult{Verdict: verdict.LocalError}, "", ""},
		{"unknown", ProbeResult{Verdict: verdict.Unknown}, "", ""},
		{"dns fail", ProbeResult{Verdict: verdict.DNSFail}, "", ""},
		{"read timeout", ProbeResult{Verdict: verdict.ReadTimeout}, "", ""},
		{"conn limited", ProbeResult{Verdict: verdict.ConnLimited}, "", ""},
	}
	for _, c := range cases {
		got, ok := SuggestRoute(c.r)
		if got != c.want || ok != (c.want != "") {
			t.Errorf("%s: got %q,%v want %q", c.name, got, ok, c.want)
		}
		if _, note := suggest(c.r); !strings.Contains(note, c.note) {
			t.Errorf("%s: note %q, want it to contain %q", c.name, note, c.note)
		}
	}
}

func TestApplySuggestionCalibration(t *testing.T) {
	tcp := ProbeResult{Verdict: verdict.TCPTimeout}
	applySuggestion(&tcp, Calibration{Internet: CheckOK})
	if tcp.Suggestion != route.Tunnel {
		t.Errorf("calibration ok: %+v", tcp)
	}
	tcp = ProbeResult{Verdict: verdict.TCPTimeout}
	applySuggestion(&tcp, Calibration{Internet: CheckUnknown})
	if tcp.Suggestion != "" {
		t.Errorf("tcp failure without a working internet check must not be suggested: %+v", tcp)
	}
	ok := ProbeResult{Verdict: verdict.OK}
	applySuggestion(&ok, Calibration{Internet: CheckFailed})
	if ok.Suggestion != "" {
		t.Errorf("failed calibration voids suggestions: %+v", ok)
	}
	ok = ProbeResult{Verdict: verdict.OK}
	applySuggestion(&ok, Calibration{Internet: CheckUnknown})
	if ok.Suggestion != route.Direct {
		t.Errorf("%+v", ok)
	}
}
