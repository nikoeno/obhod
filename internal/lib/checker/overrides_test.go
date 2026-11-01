package checker

import (
	"testing"

	"github.com/nikoejento/obhod/internal/lib/route"
)

func TestOverridesBeatListsAndKeepDecisions(t *testing.T) {
	x := setup(t, nA, Config{}) // a.com, b.com candidates; p.com proxy; x.com block
	x.start(t)
	x.waitRoute(t, "a.com", route.Direct, route.SourceChecked)
	x.waitRoute(t, "b.com", route.Direct, route.SourceChecked)
	x.call(t)

	sub, cancel := x.c.Subscribe()
	defer cancel()
	err := x.c.SetOverrides([]route.Entry{
		{Domain: "A.com.", Route: route.Block, Source: route.SourceChecked}, // candidate; source is forced to user
		{Domain: "p.com", Route: route.Direct},                              // proxy list entry
		{Domain: "x.com", Route: route.Tunnel},                              // block list entry
		{Domain: "new.example", Route: route.Desync},                        // in no list
	})
	if err != nil {
		t.Fatal(err)
	}
	if tab := <-sub; tab.Equal(route.Table{}) {
		t.Fatal("no table published")
	}
	for dom, want := range map[string]route.Route{"a.com": route.Block, "www.a.com": route.Block, "p.com": route.Direct, "x.com": route.Tunnel, "new.example": route.Desync, "sub.new.example": route.Desync} {
		if r, s := x.routeOf(dom); r != want || s != route.SourceUser {
			t.Errorf("%s = %s/%s, want %s/user", dom, r, s, want)
		}
	}
	if r, s := x.routeOf("b.com"); r != route.Direct || s != route.SourceChecked {
		t.Errorf("b.com = %s/%s, must be untouched", r, s)
	}

	// Removal: the candidate goes back to its stored decision, the static ones to the list, the unlisted one disappears.
	if err := x.c.SetOverrides(nil); err != nil {
		t.Fatal(err)
	}
	for dom, w := range map[string]struct {
		r route.Route
		s route.Source
	}{"a.com": {route.Direct, route.SourceChecked}, "p.com": {route.Proxy, route.SourceStatic}, "x.com": {route.Block, route.SourceStatic}} {
		if r, s := x.routeOf(dom); r != w.r || s != w.s {
			t.Errorf("after removal %s = %s/%s, want %s/%s", dom, r, s, w.r, w.s)
		}
	}
	if _, ok := x.c.Table().Lookup("new.example"); ok {
		t.Error("new.example still in the table after its rule was removed")
	}
}

func TestOverridesMoreSpecificListEntryStillWins(t *testing.T) {
	x := setup(t, nA, Config{})
	// x.com is a block entry; a user rule for its parent must not unblock it.
	if err := x.c.SetOverrides([]route.Entry{{Domain: "com", Route: route.Direct}}); err != nil {
		t.Fatal(err)
	}
	if r, s := x.routeOf("x.com"); r != route.Block || s != route.SourceStatic {
		t.Errorf("x.com = %s/%s", r, s)
	}
	if r, s := x.routeOf("other.com"); r != route.Direct || s != route.SourceUser {
		t.Errorf("other.com = %s/%s", r, s)
	}
}

func TestOverridesValidationChangesNothing(t *testing.T) {
	x := setup(t, nA, Config{})
	if err := x.c.SetOverrides([]route.Entry{{Domain: "ok.example", Route: route.Direct}}); err != nil {
		t.Fatal(err)
	}
	for _, bad := range []route.Entry{
		{Domain: "https://x.example", Route: route.Direct},
		{Domain: "1.2.3.4", Route: route.Direct},
		{Domain: "ok2.example", Route: "teleport"},
	} {
		if err := x.c.SetOverrides([]route.Entry{{Domain: "ok2.example", Route: route.Block}, bad}); err == nil {
			t.Errorf("%+v accepted", bad)
		}
	}
	if r, s := x.routeOf("ok.example"); r != route.Direct || s != route.SourceUser {
		t.Errorf("ok.example = %s/%s after rejected calls", r, s)
	}
	if _, ok := x.c.Table().Lookup("ok2.example"); ok {
		t.Error("a rejected call changed the table")
	}
}

func TestOverriddenCandidatesAreNotProbedAndFailuresIgnored(t *testing.T) {
	x := setup(t, nA, Config{})
	if err := x.c.SetOverrides([]route.Entry{{Domain: "a.com", Route: route.Tunnel}}); err != nil {
		t.Fatal(err)
	}
	x.start(t)
	if ds := domains(x.call(t)); len(ds) != 1 || ds[0] != "b.com" {
		t.Errorf("first batch = %v, want [b.com] (a.com has a user rule)", ds)
	}
	x.c.CheckNow("a.com")
	for i := 0; i < 5; i++ {
		x.c.ReportFailure(Failure{Domain: "a.com", Route: route.Tunnel, Verdict: "TLS_RST", At: x.clk.Now()})
	}
	x.noCall(t)
	if r, s := x.routeOf("a.com"); r != route.Tunnel || s != route.SourceUser {
		t.Errorf("a.com = %s/%s", r, s)
	}
}

func TestOverridesOnUnknownNetwork(t *testing.T) {
	x := setup(t, Network{}, Config{})
	if err := x.c.SetOverrides([]route.Entry{{Domain: "a.com", Route: route.Direct}}); err != nil {
		t.Fatal(err)
	}
	if r, s := x.routeOf("a.com"); r != route.Direct || s != route.SourceUser {
		t.Errorf("a.com = %s/%s", r, s)
	}
	if r, s := x.routeOf("b.com"); r != route.Tunnel || s != route.SourceDefault {
		t.Errorf("b.com = %s/%s", r, s)
	}
}
