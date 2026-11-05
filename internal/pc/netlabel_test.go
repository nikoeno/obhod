package pc

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/nikoejento/obhod/internal/lib/analyzer"
	"github.com/nikoejento/obhod/internal/lib/checker"
	"github.com/nikoejento/obhod/internal/lib/clock"
	"github.com/nikoejento/obhod/internal/lib/route"
)

func TestNetworkLabel(t *testing.T) {
	for _, c := range []struct {
		n    checker.Network
		want string
	}{
		{checker.Network{ASN: 64500, ASName: "ispa"}, "ispa"},
		{checker.Network{ASN: 64500}, "AS64500"},
		{checker.Network{}, "unknown network"},
		{checker.Network{ID: "gw:abc"}, "unknown network"},
		{checker.Network{ID: "local:0123456789abcdef", Known: true}, "This connection"},
	} {
		if got := networkLabel(c.n); got != c.want {
			t.Errorf("%+v: got %q, want %q", c.n, got, c.want)
		}
	}
}

// countRunner counts probe batches and answers every target as reachable.
type countRunner struct{ calls chan analyzer.Batch }

func (r countRunner) Run(_ context.Context, b analyzer.Batch) (analyzer.Report, error) {
	r.calls <- b
	rep := analyzer.Report{Calibration: analyzer.Calibration{Internet: analyzer.CheckOK}}
	for _, t := range b.Targets {
		rep.Results = append(rep.Results, analyzer.ProbeResult{Target: t, Route: route.Direct, Verdict: analyzer.Verdict("OK")})
	}
	return rep, nil
}

// A network known only by a local ID (Windows without gateway MAC/SSID, no echo endpoints) is a known
// network: the real checker probes, and the label shown for it holds no address.
func TestCheckRunsOnLocalNetwork(t *testing.T) {
	h := newHarness(t, baseTable(t))
	runner := countRunner{calls: make(chan analyzer.Batch, 10)}
	dir := t.TempDir()
	p := filepath.Join(dir, "c.txt")
	if err := os.WriteFile(p, []byte("listed.example\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg := checker.Config{Lists: checker.ListsConfig{Candidates: []string{p}}}
	cfg.Schedule.Interval = 1000 * time.Hour
	nw := checker.Network{ID: "local:0123456789abcdef", Known: true}
	ck, err := checker.New(cfg, checker.Deps{
		Runner: runner, Clock: clock.NewFake(time.Unix(1_000_000, 0)), DataDir: t.TempDir(),
		Network: checker.NewFakeNetwork(nw),
	})
	if err != nil {
		t.Fatal(err)
	}
	h.d.checker = ck
	h.start()
	h.waitRunning()
	select {
	case b := <-runner.calls:
		if len(b.Targets) == 0 || b.Targets[0].Domain != "listed.example" {
			t.Errorf("targets = %+v", b.Targets)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("no check ran on a local-ID network")
	}
	if got := networkLabel(ck.Status().Network); got != "This connection" {
		t.Errorf("label = %q", got)
	}
}
