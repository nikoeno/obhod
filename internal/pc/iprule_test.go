package pc

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/nikoejento/obhod/internal/lib/netif"
	"github.com/nikoejento/obhod/internal/pc/desync"
)

// engineRuleListing is `ip rule show` with the engine's exemption and the rules sing-box adds.
const engineRuleListing = "0:\tfrom all lookup local\n" +
	"8999:\tfrom all fwmark 0x40000000/0x40000000 lookup main\n" +
	"9000:\tfrom all to 198.19.0.0/30 lookup 2022\n" +
	"9003:\tnot from all iif lo lookup 2022\n" +
	"32766:\tfrom all lookup main\n"

// nftListingEth0 satisfies the nft check of the harness plan.
const nftListingEth0 = "table inet splitter {\n ip daddr 203.0.113.7 return\n oifname \"eth0\" tcp dport { 80, 443 } queue\n}"

const wantIPRule = "ip rule add fwmark 0x40000000/0x40000000 lookup main priority 8999"

func TestIPRuleMissing(t *testing.T) {
	setup := []string{"nft add table inet splitter", wantIPRule}
	listing := func(line string) string {
		return "0:\tfrom all lookup local\n" + line + "\n9003:\tnot from all iif lo lookup 2022\n"
	}
	cases := []struct {
		name, line string
		missing    bool
	}{
		{"exact", "8999:\tfrom all fwmark 0x40000000/0x40000000 lookup main", false},
		{"other spacing", "  8999:   from  all   fwmark   0x40000000/0x40000000   lookup   main  ", false},
		{"padded number", "8999: from all fwmark 0x040000000/0x40000000 lookup main", false},
		{"lookup by id", "8999: from all fwmark 0x40000000/0x40000000 lookup 254", false},
		{"no from all", "8999: fwmark 0x40000000/0x40000000 lookup main", false},
		{"absent", "", true},
		{"other priority", "8998: from all fwmark 0x40000000/0x40000000 lookup main", true},
		{"priority prefix", "18999: from all fwmark 0x40000000/0x40000000 lookup main", true},
		{"other mark", "8999: from all fwmark 0x20000000/0x20000000 lookup main", true},
		{"full mask", "8999: from all fwmark 0x40000000 lookup main", true},
		{"other table", "8999: from all fwmark 0x40000000/0x40000000 lookup 2022", true},
		{"table name prefix", "8999: from all fwmark 0x40000000/0x40000000 lookup mainx", true},
		{"extra selector", "8999: from all iif eth0 fwmark 0x40000000/0x40000000 lookup main", true},
		{"negated", "8999: not from all fwmark 0x40000000/0x40000000 lookup main", true},
		{"suppressed", "8999: from all fwmark 0x40000000/0x40000000 lookup main suppress_prefixlength 0", true},
		{"other source", "8999: from 10.0.0.0/8 fwmark 0x40000000/0x40000000 lookup main", true},
		{"longer mark", "8999: from all fwmark 0x400000000/0x40000000 lookup main", true},
	}
	for _, c := range cases {
		m := ipRuleMissing(listing(c.line), setup)
		if c.missing && (len(m) != 1 || m[0] != wantIPRule) || !c.missing && len(m) != 0 {
			t.Errorf("%s: missing = %q", c.name, m)
		}
	}
	if m := ipRuleMissing(engineRuleListing, setup); len(m) != 0 {
		t.Errorf("full listing: missing = %q", m)
	}
	if m := ipRuleMissing("", []string{"nft add table inet splitter", "ip rule del fwmark 0x1 lookup main priority 5"}); len(m) != 0 {
		t.Errorf("non-add commands are ignored: %q", m)
	}
	// A custom mark.
	custom := []string{"ip rule add fwmark 0x20000000/0x20000000 lookup main priority 1"}
	if m := ipRuleMissing("1: from all fwmark 0x20000000/0x20000000 lookup main", custom); len(m) != 0 {
		t.Errorf("custom mark: %q", m)
	}
}

func TestMissingIPRuleRefusesConnect(t *testing.T) {
	h := newHarness(t, baseTable(t))
	h.app.cfg.Desync.Engine = desync.NFQWS
	h.nftList = nftListingEth0
	h.ipRules = "0:\tfrom all lookup local\n9003:\tnot from all iif lo lookup 2022\n"
	err := h.app.Run(context.Background())
	if exitCode(err) != exitManual {
		t.Fatalf("err = %v, code %d", err, exitCode(err))
	}
	msg := err.Error()
	for _, want := range []string{"Apply these commands as root", "  " + wantIPRule, "Remove the rules later with:", "  ip rule del fwmark 0x40000000/0x40000000 lookup main priority 8999"} {
		if !strings.Contains(msg, want) {
			t.Errorf("message lacks %q:\n%s", want, msg)
		}
	}
	// The nft table is fine, so its commands are not printed again.
	if strings.Contains(msg, "nft add table") {
		t.Errorf("message repeats the nft commands:\n%s", msg)
	}
	if h.log.index("start nfqws2") >= 0 || h.log.index("start sing-box") >= 0 {
		t.Fatalf("started despite the missing ip rule: %s", h.ops())
	}
}

func TestMissingNFTAndIPRulePrintsBoth(t *testing.T) {
	h := newHarness(t, baseTable(t))
	h.app.cfg.Desync.Engine = desync.NFQWS
	h.nftList = nftListingEth0
	h.nft, h.ipRules = false, ""
	err := h.app.Run(context.Background())
	if exitCode(err) != exitManual {
		t.Fatalf("err = %v, code %d", err, exitCode(err))
	}
	if msg := err.Error(); !strings.Contains(msg, "nft add table inet splitter") || !strings.Contains(msg, "  "+wantIPRule) {
		t.Errorf("message:\n%s", msg)
	}
}

func TestIPRuleCheckErrors(t *testing.T) {
	h := newHarness(t, baseTable(t))
	h.app.cfg.Desync.Engine = desync.NFQWS
	h.nftList = nftListingEth0
	h.ipErr = errors.New("ip: not found")
	if err := h.app.Run(context.Background()); exitCode(err) != exitManual || !strings.Contains(err.Error(), "cannot check the ip rules") {
		t.Fatalf("err = %v", err)
	}
	// An unsupported system skips the check.
	h = newHarness(t, baseTable(t))
	h.app.cfg.Desync.Engine = desync.NFQWS
	h.nftList = nftListingEth0
	h.ipRules, h.ipErr = "", errors.ErrUnsupported
	h.start()
	h.waitOp("start nfqws2")
}

func TestIPRulePresentStartsEngine(t *testing.T) {
	h := newHarness(t, baseTable(t))
	h.app.cfg.Desync.Engine = desync.NFQWS
	h.nftList = nftListingEth0
	h.start()
	h.waitOp("start nfqws2")
}

func TestReloadWithoutIPRuleKeepsEngineDown(t *testing.T) {
	buf := &syncBuffer{}
	h := newHarness(t, baseTable(t), withLog(buf))
	h.app.cfg.Desync.Engine = desync.NFQWS
	h.nftList = nftListingEth0
	h.start()
	h.waitOp("start nfqws2")
	h.waitOp("status.start")
	h.fc.Advance(time.Minute)
	h.log.clear()
	h.mu.Lock()
	h.uplink = netif.Interface{Name: "wlan0", Index: 8}
	h.nftList = strings.ReplaceAll(nftListingEth0, "eth0", "wlan0")
	h.ipRules = ""
	h.mu.Unlock()
	h.netEv <- netif.Event{Kind: netif.UplinkChanged}
	waitUntil(t, "debounce timer", func() bool { return h.fc.Waiters() >= 1 })
	h.fc.Advance(reloadDebounce)
	waitUntil(t, "logged commands", func() bool { return strings.Contains(buf.String(), "priority 8999") })
	if h.log.index("restart nfqws2") >= 0 {
		t.Fatalf("engine restarted without the ip rule: %s", h.ops())
	}
}
