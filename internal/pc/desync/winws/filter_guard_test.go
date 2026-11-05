package winws

import (
	"net/netip"
	"strings"
)

// filterCase is one input of the filter regression tests (shared by the
// pure-Go guard and the Windows-only WinDivert compile test).
type filterCase struct {
	name  string
	idx   int
	ips   []netip.Prefix
	ports [][2]uint16
}

func filterCases() []filterCase {
	return []filterCase{
		{"none", 7, nil, nil},
		{"v4 host", 7, []netip.Prefix{pfx("203.0.113.7/32")}, nil},
		{"v4 net", 7, []netip.Prefix{pfx("10.0.0.0/8")}, nil},
		{"v4 two", 7, []netip.Prefix{pfx("10.0.0.0/8"), pfx("192.168.0.0/16"), pfx("198.51.100.1/32")}, nil},
		{"v6 host", 7, []netip.Prefix{pfx("2001:db8::1/128")}, nil},
		{"v6 net", 7, []netip.Prefix{pfx("fd00::/8")}, nil},
		{"v6 two", 7, []netip.Prefix{pfx("fd00::/8"), pfx("2001:db8::/32"), pfx("2001:db8::7/128")}, nil},
		{"zero prefixes", 7, []netip.Prefix{pfx("0.0.0.0/0"), pfx("::/0")}, nil},
		{"one port range", 7, nil, [][2]uint16{{50000, 50099}}},
		{"single port", 7, nil, [][2]uint16{{8080, 8080}}},
		{"several ports", 7, nil, [][2]uint16{{50000, 50099}, {8080, 8080}, {1, 65535}}},
		{"everything", 9, []netip.Prefix{pfx("203.0.113.7/32"), pfx("10.0.0.0/8"), pfx("fd00::1/128"), pfx("2001:db8::/32")},
			[][2]uint16{{50000, 50099}, {8080, 8080}}},
	}
}

// invalidNegations are the constructs WinDivert's filter parser rejects
// ("Filter expression parse error"; winws2 then exits with status 87).
var invalidNegations = []string{"not (", "not(", "!(", "!  ("}

func negatesGroup(filter string) (string, bool) {
	for _, bad := range invalidNegations {
		if strings.Contains(filter, bad) {
			return bad, true
		}
	}
	return "", false
}
