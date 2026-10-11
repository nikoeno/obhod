package winws

import (
	"fmt"
	"net/netip"
	"strings"
)

// Filter builds the WinDivert filter for --wf-raw-filter. winws2 ANDs it with
// its own interception filter (outbound TCP to the --wf-tcp-out ports), so it
// only holds what restricts that: the uplink, the excluded destination
// addresses and the excluded source ports.
//
// WinDivert has no CIDR syntax, so every prefix becomes an address range. A
// clause on ip.DstAddr is false for IPv6 packets and the other way round, so
// "not (...)" lets the other family through.
func Filter(ifIndex int, excludeIPs []netip.Prefix, excludePorts [][2]uint16) (string, error) {
	if ifIndex <= 0 {
		return "", fmt.Errorf("%w: IfIndex must be positive", ErrInvalid)
	}
	clauses := []string{ifClause(ifIndex)}

	var v4, v6 []string
	for _, p := range excludeIPs {
		if !p.IsValid() {
			return "", fmt.Errorf("%w: invalid exclude prefix", ErrInvalid)
		}
		p = p.Masked()
		if p.Addr().Is4() {
			v4 = append(v4, addrClause("ip.DstAddr", p))
		} else {
			v6 = append(v6, addrClause("ipv6.DstAddr", p))
		}
	}
	if c := notAny(v4); c != "" {
		clauses = append(clauses, c)
	}
	if c := notAny(v6); c != "" {
		clauses = append(clauses, c)
	}

	var ports []string
	for _, r := range excludePorts {
		c, err := portClause("tcp.SrcPort", r)
		if err != nil {
			return "", err
		}
		ports = append(ports, c)
	}
	if c := notAny(ports); c != "" {
		clauses = append(clauses, c)
	}
	return strings.Join(clauses, " and "), nil
}

func ifClause(idx int) string { return fmt.Sprintf("ifIdx == %d", idx) }

// notAny returns "not (a or b ...)" or "" for an empty list.
func notAny(clauses []string) string {
	if len(clauses) == 0 {
		return ""
	}
	return "not (" + strings.Join(clauses, " or ") + ")"
}

// addrClause matches the addresses of p: a single comparison for a host
// prefix, otherwise an inclusive range.
func addrClause(field string, p netip.Prefix) string {
	first, last := p.Addr(), lastAddr(p)
	if first == last {
		return fmt.Sprintf("%s == %s", field, first)
	}
	return fmt.Sprintf("(%s >= %s and %s <= %s)", field, first, field, last)
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

func portClause(field string, r [2]uint16) (string, error) {
	if r[0] == 0 || r[0] > r[1] {
		return "", fmt.Errorf("%w: port range %d-%d", ErrInvalid, r[0], r[1])
	}
	if r[0] == r[1] {
		return fmt.Sprintf("%s == %d", field, r[0]), nil
	}
	return fmt.Sprintf("(%s >= %d and %s <= %d)", field, r[0], field, r[1]), nil
}
