package desktop

// First-run regression tests (see "First-run regression tests" in API.md): the real `run --ui` code
// path from an empty machine, with only the engine replaced where it would need admin rights.

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/nikoejento/obhod/internal/lib/checker/lists"
	"github.com/nikoejento/obhod/internal/lib/paths"
	"github.com/nikoejento/obhod/internal/pc"
)

// The whole first start from empty config and data directories: the default app.yaml and the starter lists
// appear, the engine is built from a config whose lists load with the real lists package (at least 100
// candidates, or the Sites page is empty), and a real pc.App (real checker, real lists, real store) can be
// built from that very config. The App is not run: that needs administrator rights.
func TestFirstRunEndToEnd(t *testing.T) {
	h, cfgFile := firstStart(t)
	var (
		got  pc.Config
		real *pc.App
	)
	h.cli.newEngine = func(c pc.Config, p paths.Paths, opts ...pc.Option) (engine, error) {
		got = c
		app, err := pc.New(c, p, opts...) // what the production newEngine does
		if err != nil {
			return nil, err
		}
		real = app
		return h.eng, nil
	}
	if _, err := os.Stat(h.p.Config); err == nil {
		t.Fatal("the config directory exists before the first start")
	}
	if code := h.run("run", "--ui", "--token-file", h.tokenArg); code != 0 {
		t.Fatalf("exit %d: %s", code, h.errOut.String())
	}
	if real == nil {
		t.Fatal("the engine was not built")
	}
	if _, err := os.Stat(cfgFile); err != nil {
		t.Fatalf("default app.yaml not created: %v", err)
	}
	for _, list := range [][]string{got.Checker.Lists.Candidates, got.Checker.Lists.Proxy, got.Checker.Lists.Block} {
		if len(list) == 0 {
			t.Fatalf("the default config does not reference the starter lists: %+v", got.Checker.Lists)
		}
		for _, f := range list {
			if !filepath.IsAbs(f) {
				t.Errorf("list path %q is not resolved against the config folder", f)
			}
			if fi, err := os.Stat(f); err != nil || fi.IsDir() {
				t.Errorf("listed file %q was not created: %v", f, err)
			}
		}
	}
	l, err := lists.Load(got.Checker.Lists)
	if err != nil {
		t.Fatalf("the engine's checker lists do not load: %v", err)
	}
	if len(l.Candidates) < 100 {
		t.Fatalf("%d candidates from the default lists, want >= 100", len(l.Candidates))
	}
	if len(l.Warnings) != 0 {
		t.Errorf("list warnings: %v", l.Warnings)
	}
	// Servers are managed from the app: nothing in a fresh install names one.
	if _, err := os.Stat(filepath.Join(h.p.Secrets, got.Secrets.Tunnel)); err == nil {
		t.Error("a tunnel file exists on a first start")
	}
}
