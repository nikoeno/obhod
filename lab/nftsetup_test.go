package lab

import (
	"slices"
	"strconv"
	"strings"
	"testing"
)

const nftMessage = `splitter: the nftables table "inet splitter" does not exist. Apply these commands as root, then start again:
  nft add table inet splitter
  nft flush table inet splitter
  nft 'add chain inet splitter postrouting { type filter hook postrouting priority 101; }'
  nft 'add rule inet splitter postrouting ip daddr { 203.0.113.250/32 } return'
  nft 'add rule inet splitter postrouting oifname "eth0" tcp dport { 80, 443 } meta mark and 0x40000000 == 0 ct original packets 1-12 queue num 200 bypass'
  ip rule add fwmark 0x40000000/0x40000000 lookup main priority 8999
Remove the rules later with:
  nft delete table inet splitter
  ip rule del fwmark 0x40000000/0x40000000 lookup main priority 8999
`

func TestParseNFTSetup(t *testing.T) {
	cmds, err := parseNFTSetup(nftMessage)
	if err != nil {
		t.Fatal(err)
	}
	if len(cmds) != 6 {
		t.Fatalf("got %d commands: %q", len(cmds), cmds)
	}
	if !slices.Equal(cmds[0], []string{"nft", "add", "table", "inet", "splitter"}) {
		t.Errorf("bare command: %q", cmds[0])
	}
	want := `add chain inet splitter postrouting { type filter hook postrouting priority 101; }`
	if !slices.Equal(cmds[2], []string{"nft", want}) {
		t.Errorf("quoted command: %q, want one argument %q", cmds[2], want)
	}
	if got := cmds[4][1]; !strings.Contains(got, `oifname "eth0"`) || !strings.HasSuffix(got, "queue num 200 bypass") {
		t.Errorf("rule with double quotes inside single quotes: %q", got)
	}
	if want := strings.Fields("ip rule add fwmark 0x40000000/0x40000000 lookup main priority 8999"); !slices.Equal(cmds[5], want) {
		t.Errorf("ip rule command: %q", cmds[5])
	}
	for _, c := range cmds {
		if slices.Contains(c, "delete") || slices.Contains(c, "del") {
			t.Errorf("a Teardown command was returned: %q", c)
		}
	}
}

func TestParseNFTSetupRejects(t *testing.T) {
	hdr := "x: Apply these commands as root, then start again:\n"
	for name, body := range map[string]string{
		"not nft":        "  rm -rf /tmp/x\n",
		"other table":    "  nft add table inet other\n",
		"delete verb":    "  nft delete table inet splitter\n",
		"shell chain":    "  nft add table inet splitter; id\n",
		"substitution":   "  nft add table inet splitter $(id)\n",
		"double quote":   "  nft \"add table inet splitter\"\n",
		"unterminated":   "  nft 'add table inet splitter\n",
		"not indented":   "nft add table inet splitter\n",
		"no commands":    "",
		"pipe in a word": "  nft add table inet splitter | sh\n",
		"ip rule del":    "  ip rule del fwmark 0x40000000/0x40000000 lookup main priority 8999\n",
		"ip other mark":  "  ip rule add fwmark 0x1/0x1 lookup main priority 8999\n",
		"ip other table": "  ip rule add fwmark 0x40000000/0x40000000 lookup 2022 priority 8999\n",
		"ip no prio":     "  ip rule add fwmark 0x40000000/0x40000000 lookup main\n",
		"ip text prio":   "  ip rule add fwmark 0x40000000/0x40000000 lookup main priority x\n",
		"ip signed prio": "  ip rule add fwmark 0x40000000/0x40000000 lookup main priority +5\n",
		"ip extra word":  "  ip rule add fwmark 0x40000000/0x40000000 lookup main priority 8999 iif lo\n",
		"ip -6":          "  ip -6 rule add fwmark 0x40000000/0x40000000 lookup main priority 8999\n",
		"ip route":       "  ip route add default dev lo\n",
		"ip chained":     "  ip rule add fwmark 0x40000000/0x40000000 lookup main priority 8999; id\n",
	} {
		if cmds, err := parseNFTSetup(hdr + body); err == nil {
			t.Errorf("%s: accepted %q", name, cmds)
		}
	}
	if _, err := parseNFTSetup("no header\n  nft add table inet splitter\n"); err == nil {
		t.Error("accepted output without the Setup header")
	}
}

func TestLogErrValue(t *testing.T) {
	errText := "the nftables table \"inet splitter\" does not exist. Apply these commands as root, then start again:\n  nft add table inet splitter\nRemove the rules later with:\n  nft delete table inet splitter"
	log := "time=1 level=ERROR msg=\"reload: desync engine not (re)started\" err=" + strconv.Quote(errText) + "\ntime=2 level=INFO msg=other\n"
	msg, err := logErrValue(log, "desync engine not (re)started")
	if err != nil {
		t.Fatal(err)
	}
	if msg != errText {
		t.Fatalf("got %q", msg)
	}
	cmds, err := parseNFTSetup(msg)
	if err != nil || len(cmds) != 1 {
		t.Fatalf("%v %q", err, cmds)
	}
	if _, err := logErrValue(log, "absent"); err == nil {
		t.Error("found an absent marker")
	}
}
