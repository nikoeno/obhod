//go:build linux && lab

package lab

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net/netip"
	"os/exec"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/nikoejento/obhod/internal/lib/analyzer"
	"github.com/nikoejento/obhod/internal/lib/faultserver"
	"github.com/nikoejento/obhod/internal/lib/route"
	"github.com/nikoejento/obhod/lab/fakedns"
	"github.com/nikoejento/obhod/lab/fakedpi"
)

// baWant is what one blockanalyzer result must show; empty fields are not checked.
type baWant struct {
	verdict    analyzer.Verdict
	stage      analyzer.Stage
	blocking   analyzer.Blocking
	suggestion route.Route
	note       string           // substring of suggestion_note
	swap       analyzer.Verdict // evidence.sni_swap.verdict
	errHas     string           // substring of evidence.err
}

// TestBlockanalyzer is L6: the real blockanalyzer binary in the client namespace, against lab sites,
// the fake TSPU and the poisoning ISP resolver. Each case checks the verdict, stage, blocking and the
// suggested route of the JSON report (schema 1); the last cases check that a dead uplink fails the
// calibration. The root namespace must be unchanged afterwards (rootSnapshot is in
// fakedpi_queue_test.go).
func TestBlockanalyzer(t *testing.T) {
	if err := Preflight(); err != nil {
		t.Skip(err)
	}
	before := rootSnapshot(t)
	t.Cleanup(func() { before.compare(t, rootSnapshot(t)) }) // registered first: runs after the lab's teardown

	l := Up(t)
	const isp = "ispa"
	if _, ok := l.isp(isp).p.Poisoned["poisoned.test"]; !ok {
		t.Fatalf("profile %s does not poison poisoned.test: %v", isp, l.isp(isp).p.Poisoned)
	}

	// Calibration: two controls that are never blocked, one canary that every case blocks by SNI
	// (so dpi_present is "yes"), and a neutral SNI for the swap (any OK lab name: a faultserver
	// accepts every SNI, and a certificate for another name still counts as a reached handshake).
	ok := faultserver.Spec{Behavior: faultserver.OK, Bytes: 1024}
	for _, n := range []string{"control-a.test", "control-b.test", "canary.test", "neutral.test", "site-ok.test", "sni-block.test", "ip-block.test", "poisoned.test", "uplink.test"} {
		l.AddSite(n, ok)
	}
	// The bulk test reads 64 KiB; the object must be larger so the stall, not its end, stops it.
	l.AddSite("stall.test", faultserver.Spec{Behavior: faultserver.OK, Bytes: 256 << 10})
	l.AddSite("mitm.test", faultserver.Spec{Behavior: faultserver.WrongCert})
	l.AddSite("alert.test", faultserver.Spec{Behavior: faultserver.TLSAlert})
	canary := fakedpi.Rule{Kind: fakedpi.NaiveSNI, Domains: []string{"canary.test"}}
	host32 := func(name string) netip.Prefix {
		a := l.Site(name).Addr.Addr()
		return netip.PrefixFrom(a, a.BitLen())
	}

	cfgFile, cfgErr := l.blockanalyzerConfig()
	if cfgErr != nil {
		t.Fatal(cfgErr)
	}
	bin := l.Binary("blockanalyzer")

	// analyze runs blockanalyzer on domains and decodes its JSON report; code is the exit code.
	analyze := func(domains ...string) (rep analyzer.Report, code int, err error) {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
		defer cancel()
		argv := append([]string{bin, "--config", cfgFile, "--json", "-", "--repeats", "2", "--concurrency", "1"}, domains...)
		stdout, stderr, runErr := l.Exec(ctx, argv...)
		if runErr != nil {
			var ee *exec.ExitError
			if !errors.As(runErr, &ee) {
				return rep, -1, runErr
			}
			code = ee.ExitCode()
		}
		rep, err = analyzer.DecodeReport(bytes.NewReader(stdout))
		if err != nil {
			return rep, code, fmt.Errorf("exit %d: %v; stderr: %s", code, err, strings.TrimSpace(string(stderr)))
		}
		return rep, code, nil
	}
	report := func(t *testing.T, scenario string, errs []string) {
		t.Helper()
		for _, e := range errs {
			t.Error(e)
		}
		note := "ok"
		if len(errs) > 0 {
			note = strings.Join(errs, "; ")
		}
		l.Report("blockanalyzer: "+scenario, len(errs) == 0, note)
	}
	checkResult := func(res analyzer.ProbeResult, w baWant) []string {
		var errs []string
		bad := func(f string, a ...any) { errs = append(errs, fmt.Sprintf(f, a...)) }
		if w.verdict != "" && res.Verdict != w.verdict {
			bad("verdict %s, want %s (stage %s, evidence %+v)", res.Verdict, w.verdict, res.Stage, res.Evidence)
		}
		if w.stage != "" && res.Stage != w.stage {
			bad("stage %s, want %s", res.Stage, w.stage)
		}
		if w.blocking != "" && res.Blocking != w.blocking {
			bad("blocking %s, want %s", res.Blocking, w.blocking)
		}
		if res.Suggestion != w.suggestion {
			bad("suggestion %q, want %q (note %q)", res.Suggestion, w.suggestion, res.SuggestionNote)
		}
		if w.note != "" && !strings.Contains(res.SuggestionNote, w.note) {
			bad("suggestion_note %q, want it to contain %q", res.SuggestionNote, w.note)
		}
		if w.swap != "" {
			if sw := res.Evidence.SNISwap; sw == nil || sw.Verdict != w.swap {
				bad("sni_swap %+v, want verdict %s", sw, w.swap)
			}
		}
		if w.errHas != "" && !strings.Contains(res.Evidence.Err, w.errHas) {
			bad("evidence.err %q, want it to contain %q", res.Evidence.Err, w.errHas)
		}
		return errs
	}

	cases := []struct {
		name   string
		domain string
		rules  []fakedpi.Rule
		want   baWant
		extra  func() []string // facts observed by the fake endpoints
	}{{
		name:   "OK site",
		domain: "site-ok.test",
		want:   baWant{verdict: "OK", blocking: "none", suggestion: route.Direct},
		extra: func() []string {
			if !slices.ContainsFunc(l.Site("site-ok.test").Server.Conns(), func(c faultserver.Conn) bool {
				return c.Remote.Addr() == l.ClientAddr(isp) && c.SNI == "site-ok.test"
			}) {
				return []string{fmt.Sprintf("site-ok.test saw no connection from %v", l.ClientAddr(isp))}
			}
			return nil
		},
	}, {
		name:   "naive-sni: TLS_RST confirmed by the SNI swap",
		domain: "sni-block.test",
		rules:  []fakedpi.Rule{{Kind: fakedpi.NaiveSNI, Domains: []string{"sni-block.test"}}},
		want:   baWant{verdict: "TLS_RST", stage: "tls", blocking: "confirmed", suggestion: route.Desync, swap: "OK"},
		extra: func() []string {
			if !slices.ContainsFunc(l.DPI(isp).Seen(), func(f fakedpi.Flow) bool {
				return f.SNI == "sni-block.test" && f.Verdict == fakedpi.Reset && f.Rule == fakedpi.NaiveSNI
			}) {
				return []string{"fakedpi has no reset flow for sni-block.test"}
			}
			return nil
		},
	}, {
		name:   "ip-block: TCP_TIMEOUT",
		domain: "ip-block.test",
		rules:  []fakedpi.Rule{{Kind: fakedpi.IPBlock, Prefix: host32("ip-block.test")}},
		want:   baWant{verdict: "TCP_TIMEOUT", stage: "tcp", blocking: "likely", suggestion: route.Tunnel},
		extra: func() []string {
			if n := len(l.Site("ip-block.test").Server.Conns()); n != 0 {
				return []string{fmt.Sprintf("ip-block.test accepted %d connections through an IP block", n)}
			}
			return nil
		},
	}, {
		// Stall counts server→client payload bytes; 16000 plus the client's ~1 KB lands in the
		// 12-36 KiB window of the 64 KiB download.
		name:   "stall after 16000 bytes: STALL_16_20 in bulk",
		domain: "stall.test",
		rules:  []fakedpi.Rule{{Kind: fakedpi.Stall, Prefix: host32("stall.test"), Bytes: 16000}},
		want:   baWant{verdict: "STALL_16_20", stage: "bulk", blocking: "confirmed", suggestion: route.Tunnel},
	}, {
		name:   "wrong certificate: TLS_MITM",
		domain: "mitm.test",
		want:   baWant{verdict: "TLS_MITM", stage: "verify", blocking: "confirmed", suggestion: route.Tunnel},
	}, {
		// The server alerts every SNI, so the swap fails too: not confirmed, and the suggestion is
		// tunnel (DESIGN §5 Confirm; AGENTS "Suggested starting route": desync only with swap success).
		name:   "TLS alert: TLS_ALERT",
		domain: "alert.test",
		want:   baWant{verdict: "TLS_ALERT", stage: "tls", blocking: "likely", suggestion: route.Tunnel, swap: "TLS_ALERT"},
	}, {
		// The client's system resolver is ispa's, which answers its stub; DoH has the real address.
		// The stub is TEST-NET-1 (not a bogon for dnscheck), so the verdict comes from stub learning:
		// the stub answers plain HTTP with a 302 to block.ispa.test (the harness's ISP block page).
		name:   "poisoning ISP resolver: DNS_POISONED",
		domain: "poisoned.test",
		want:   baWant{verdict: "DNS_POISONED", stage: "resolve", blocking: "likely", suggestion: route.Direct, note: "needs DoH", errHas: "stub"},
		extra: func() []string {
			if !slices.ContainsFunc(l.DoH().Queries(), func(q fakedns.Query) bool {
				return q.Name == "poisoned.test" && q.Via == "doh"
			}) {
				return []string{"the DoH server was never asked for poisoned.test"}
			}
			if n := len(l.Site("poisoned.test").Server.Conns()); n != 0 {
				return []string{fmt.Sprintf("poisoned.test was probed (%d connections) although DNS is poisoned", n)}
			}
			return nil
		},
	}}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			l.SetRules(isp, append(slices.Clone(c.rules), canary)...)
			defer l.SetRules(isp)
			rep, code, err := analyze(c.domain)
			if err != nil {
				report(t, c.name, []string{err.Error()})
				return
			}
			var errs []string
			if code != 0 {
				errs = append(errs, fmt.Sprintf("exit code %d, want 0", code))
			}
			if rep.Calibration.Internet != analyzer.CheckOK || rep.Calibration.DPIPresent != analyzer.CheckYes {
				errs = append(errs, fmt.Sprintf("calibration %+v, want internet ok, dpi_present yes", rep.Calibration))
			}
			if len(rep.Results) != 1 || rep.Results[0].Target.Domain != c.domain {
				errs = append(errs, fmt.Sprintf("results %+v, want one for %s", rep.Results, c.domain))
			} else {
				errs = append(errs, checkResult(rep.Results[0], c.want)...)
			}
			if c.extra != nil {
				errs = append(errs, c.extra()...)
			}
			report(t, c.name, errs)
		})
	}

	// calibrationFailed checks a report taken with a dead uplink: exit 2, internet failed, no
	// suggestion at all (DESIGN §6; blockanalyzer API exit codes).
	calibrationFailed := func(t *testing.T, scenario string) {
		rep, code, err := analyze("uplink.test")
		if err != nil {
			report(t, scenario, []string{err.Error()})
			return
		}
		var errs []string
		if code != 2 {
			errs = append(errs, fmt.Sprintf("exit code %d, want 2", code))
		}
		if rep.Calibration.Internet != analyzer.CheckFailed {
			errs = append(errs, fmt.Sprintf("calibration.internet %q, want failed (%+v)", rep.Calibration.Internet, rep.Calibration))
		}
		for _, r := range rep.Results {
			if r.Suggestion != "" {
				errs = append(errs, fmt.Sprintf("%s: suggestion %q after a failed calibration", r.Target.Domain, r.Suggestion))
			}
		}
		report(t, scenario, errs)
	}

	// The ISP's side of the link down (a dead modem): the client keeps eth0, its address and its
	// routes, so the analyzer finds its uplink and every probe times out.
	l.SetISPLink(isp, false)
	t.Run("calibration fails: ISP side of the link down", func(t *testing.T) {
		calibrationFailed(t, "calibration fails: ISP side of the link down")
	})
	l.SetISPLink(isp, true)
	l.UseISP(isp)

	// The client's own link down, inside the client namespace (ip netns exec <cli>). If no
	// interface is up the direct dialer cannot find an uplink; the CLI contract still says a dead
	// network is exit 2 with a calibration-failed report, so anything else is reported as a failure.
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	_, _, err := l.Exec(ctx, "ip", "link", "set", "eth0", "down")
	cancel()
	if err != nil {
		t.Fatalf("client link down: %v", err)
	}
	t.Run("calibration fails: client link down", func(t *testing.T) {
		calibrationFailed(t, "calibration fails: client link down")
	})
	l.UseISP(isp) // link up and the default route back

	t.Run("network back after the calibration cases", func(t *testing.T) {
		l.SetRules(isp, canary)
		defer l.SetRules(isp)
		rep, _, err := analyze("site-ok.test")
		var errs []string
		switch {
		case err != nil:
			errs = append(errs, err.Error())
		case rep.Calibration.Internet != analyzer.CheckOK || len(rep.Results) != 1:
			errs = append(errs, fmt.Sprintf("calibration %+v, results %d", rep.Calibration, len(rep.Results)))
		default:
			errs = checkResult(rep.Results[0], baWant{verdict: "OK", suggestion: route.Direct})
		}
		report(t, "network back after the calibration cases", errs)
	})
}
