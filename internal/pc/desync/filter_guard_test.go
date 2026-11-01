package desync

import (
	"net/netip"
	"strings"
	"testing"
)

// winwsRenders renders every preset (and the custom strategy form) for the
// WinDivert engine with exclusions of both families, returning the arguments.
func winwsRenders(t *testing.T) map[string][]string {
	t.Helper()
	tb := desyncTable(t)
	out := map[string][]string{}
	add := func(name string, cfg Config) {
		cfg.ExcludeIPs = append(cfg.ExcludeIPs, netip.MustParsePrefix("fd00::1/128"), netip.MustParsePrefix("10.0.0.0/8"), netip.MustParsePrefix("2001:db8::/32"))
		plan, err := Build(cfg, uplink(), tb)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		out[name] = plan.Args
	}
	for _, p := range Presets() {
		add(p.ID, presetCfg(WinWS, p.ID))
	}
	c := cfg(WinWS)
	c.ExcludePorts = []PortRange{{50000, 50099}, {8080, 8080}}
	add("custom", c)
	return out
}

// TestWinWSFilterNeverNegatesGroup: WinDivert cannot parse "not (...)", and
// winws2 then exits with status 87, so no rendered argument may hold it.
func TestWinWSFilterNeverNegatesGroup(t *testing.T) {
	for name, args := range winwsRenders(t) {
		for _, a := range args {
			for _, bad := range []string{"not (", "not(", "!(", "!  ("} {
				if strings.Contains(a, bad) {
					t.Errorf("%s: argument holds %q: %s", name, bad, a)
				}
			}
		}
	}
}
