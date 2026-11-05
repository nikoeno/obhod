package winws

import "testing"

// TestFilterNeverNegatesGroup guards against the regression where the filter
// held "not (...)": WinDivert cannot parse a negated group, so winws2 exited
// with status 87 and desync never ran.
func TestFilterNeverNegatesGroup(t *testing.T) {
	for _, c := range filterCases() {
		t.Run(c.name, func(t *testing.T) {
			got, err := Filter(c.idx, c.ips, c.ports)
			if err != nil {
				t.Fatal(err)
			}
			if bad, found := negatesGroup(got); found {
				t.Fatalf("filter holds %q, which WinDivert cannot parse: %s", bad, got)
			}
		})
	}
	for _, in := range []Input{baseInput(), profilesInput()} {
		out, err := Render(in)
		if err != nil {
			t.Fatal(err)
		}
		for _, a := range out.Args {
			if bad, found := negatesGroup(a); found {
				t.Fatalf("argument holds %q: %s", bad, a)
			}
		}
	}
}
