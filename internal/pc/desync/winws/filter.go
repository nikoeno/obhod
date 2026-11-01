package winws

import (
	"fmt"
	"net/netip"
	"strings"
)

// Filter builds the WinDivert filter for --wf-raw-filter. winws2 ANDs it with
// its own interception filter (outbound TCP/UDP to the --wf-tcp-out and
// --wf-udp-out ports), so it only holds what restricts that: the uplink, the
// excluded destination addresses and the excluded source ports.
//
// WinDivert has no CIDR syntax, so every prefix becomes an address range. Its
// filter language cannot negate a parenthesised group (it rejects "not (",
// "not(" and "!("; winws2 then exits with "error opening filter", status 87),
// so every exclusion is written as a positive disjunction that is true outside
// the excluded set: "(ipv6 or ip.DstAddr < a or ip.DstAddr > b)". The other IP
// family (the "ipv6 or" / "ip or" guard) and, for ports, non-TCP packets
// ("not tcp or") are not excluded. Several exclusions are joined by "and".
func Filter(ifIndex int, excludeIPs []netip.Prefix, excludePorts [][2]uint16) (string, error) {
	if ifIndex <= 0 {
		return "", fmt.Errorf("%w: IfIndex must be positive", ErrInvalid)
	}
	clauses := []string{ifClause(ifIndex)}

	for _, p := range excludeIPs {
		if !p.IsValid() {
			return "", fmt.Errorf("%w: invalid exclude prefix", ErrInvalid)
		}
		p = p.Masked()
		if p.Addr().Is4() {
			clauses = append(clauses, outsideAddr("ipv6", "ip.DstAddr", p))
		} else {
			clauses = append(clauses, outsideAddr("ip", "ipv6.DstAddr", p))
		}
	}

	for _, r := range excludePorts {
		c, err := outsidePort("tcp.SrcPort", r)
		if err != nil {
			return "", err
		}
		clauses = append(clauses, c)
	}
	return strings.Join(clauses, " and "), nil
}

func ifClause(idx int) string { return fmt.Sprintf("ifIdx == %d", idx) }

// outsideAddr is true for every packet whose destination address (field) is
// not in p: packets of the other IP family (other, "ip" or "ipv6") pass.
func outsideAddr(other, field string, p netip.Prefix) string {
	first, last := p.Addr(), lastAddr(p)
	if first == last {
		return fmt.Sprintf("(%s or %s != %s)", other, field, first)
	}
	return fmt.Sprintf("(%s or %s < %s or %s > %s)", other, field, first, field, last)
}

// lastAddr returns the highest address of a masked prefix.
func lastAddr(p netip.Prefix) netip.Addr {
	b := p.Addr().AsSlice()
	for i := p.Bits(); i < len(b)*8; i++ {
		b[i/8] |= 0x80 >> (i % 8)
	}
	a, _ := netip.AddrFromSlice(b)
	return a
}

// outsidePort is true for every packet whose TCP source port is outside r;
// packets that are not TCP pass.
func outsidePort(field string, r [2]uint16) (string, error) {
	if r[0] == 0 || r[0] > r[1] {
		return "", fmt.Errorf("%w: port range %d-%d", ErrInvalid, r[0], r[1])
	}
	if r[0] == r[1] {
		return fmt.Sprintf("(not tcp or %s != %d)", field, r[0]), nil
	}
	return fmt.Sprintf("(not tcp or %s < %d or %s > %d)", field, r[0], field, r[1]), nil
}
