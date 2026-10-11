package nfqws

import (
	"fmt"
	"net/netip"
	"sort"
	"strconv"
	"strings"
)

// nftRules returns the shell commands that create the "inet splitter" table.
// They follow the zapret2 README example:
//
//   - a postrouting chain (priority 101) sends the first packets (ct original
//     packets 1-12) of TCP connections to the given ports, leaving through
//     uplink and not carrying our mark, to the nfqws2 queue;
//   - an output chain (priority -401) marks nothing but disables conntrack
//     for packets that carry the mark, so nfqws2's own packets do not loop.
//
// Excluded destination addresses and source ports are skipped by an early
// "return" in the postrouting chain. A match such as "ip daddr != X" would
// silently drop every IPv6 packet from the rule, so excludes are not part of
// the queue rule. Each command is a single "nft '<statement>'" so that braces
// and semicolons survive the shell.
func nftRules(uplink string, qnum uint16, mark uint32, ports []uint16, excludeIPs []netip.Prefix, excludePorts [][2]uint16) []string {
	var v4, v6 []string
	for _, p := range excludeIPs {
		p = p.Masked()
		if p.Addr().Is4() {
			v4 = append(v4, p.String())
		} else {
			v6 = append(v6, p.String())
		}
	}
	sort.Strings(v4)
	sort.Strings(v6)
	var sports []string
	for _, r := range excludePorts {
		if r[0] == r[1] {
			sports = append(sports, strconv.Itoa(int(r[0])))
		} else {
			sports = append(sports, fmt.Sprintf("%d-%d", r[0], r[1]))
		}
	}
	sort.Strings(sports)
	dports := make([]string, len(ports))
	for i, p := range ports {
		dports[i] = strconv.Itoa(int(p))
	}

	cmds := []string{
		"nft add table inet " + table,
		stmt("add chain inet %s postrouting { type filter hook postrouting priority 101; }", table),
	}
	if len(v4) > 0 {
		cmds = append(cmds, stmt("add rule inet %s postrouting ip daddr { %s } return", table, strings.Join(v4, ", ")))
	}
	if len(v6) > 0 {
		cmds = append(cmds, stmt("add rule inet %s postrouting ip6 daddr { %s } return", table, strings.Join(v6, ", ")))
	}
	if len(sports) > 0 {
		cmds = append(cmds, stmt("add rule inet %s postrouting tcp sport { %s } return", table, strings.Join(sports, ", ")))
	}
	cmds = append(cmds,
		stmt(`add rule inet %s postrouting oifname "%s" tcp dport { %s } meta mark and 0x%08x == 0 ct original packets 1-12 queue num %d bypass`,
			table, uplink, strings.Join(dports, ", "), mark, qnum),
		stmt("add chain inet %s output { type filter hook output priority -401; }", table),
		stmt("add rule inet %s output meta mark and 0x%08x != 0 notrack", table, mark),
	)
	return cmds
}

// nftDown returns the command that removes the table and nothing else.
func nftDown() []string { return []string{"nft delete table inet " + table} }

// stmt formats an nft statement and wraps it for the shell.
func stmt(format string, a ...any) string {
	return "nft '" + fmt.Sprintf(format, a...) + "'"
}
