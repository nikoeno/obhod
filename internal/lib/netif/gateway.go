package netif

import (
	"bufio"
	"encoding/binary"
	"io"
	"net"
	"net/netip"
	"strconv"
	"strings"
)

// defaultRoute is one default IPv4 route from a Linux route table.
type defaultRoute struct {
	Iface   string
	Gateway netip.Addr
	Metric  int
}

const (
	rtfUp      = 0x1
	rtfGateway = 0x2
)

// parseRouteTable parses the /proc/net/route format and returns its default IPv4 routes
// (destination and mask 0.0.0.0, UP and GATEWAY flags set). Malformed lines are skipped.
func parseRouteTable(r io.Reader) []defaultRoute {
	var out []defaultRoute
	sc := bufio.NewScanner(r)
	for sc.Scan() {
		f := strings.Fields(sc.Text())
		if len(f) < 8 || f[0] == "Iface" {
			continue
		}
		dst, err1 := strconv.ParseUint(f[1], 16, 32)
		gw, err2 := strconv.ParseUint(f[2], 16, 32)
		flags, err3 := strconv.ParseUint(f[3], 16, 32)
		metric, err4 := strconv.Atoi(f[6])
		mask, err5 := strconv.ParseUint(f[7], 16, 32)
		if err1 != nil || err2 != nil || err3 != nil || err4 != nil || err5 != nil {
			continue
		}
		if dst != 0 || mask != 0 || flags&rtfUp == 0 || flags&rtfGateway == 0 || gw == 0 {
			continue
		}
		// The kernel prints the address as a host-order word of a little-endian dump.
		var b [4]byte
		binary.LittleEndian.PutUint32(b[:], uint32(gw))
		out = append(out, defaultRoute{Iface: f[0], Gateway: netip.AddrFrom4(b), Metric: metric})
	}
	return out
}

// pickDefaultRoute returns the lowest-metric default route of iface, or of any interface when iface is "".
func pickDefaultRoute(routes []defaultRoute, iface string) (defaultRoute, bool) {
	var best defaultRoute
	found := false
	for _, r := range routes {
		if iface != "" && r.Iface != iface {
			continue
		}
		if !found || r.Metric < best.Metric {
			best, found = r, true
		}
	}
	return best, found
}

// parseARPTable parses the /proc/net/arp format and returns the hardware address of ip on iface.
// Incomplete entries (flag 0x2 unset) and all-zero addresses are ignored.
func parseARPTable(r io.Reader, ip netip.Addr, iface string) net.HardwareAddr {
	sc := bufio.NewScanner(r)
	for sc.Scan() {
		f := strings.Fields(sc.Text())
		if len(f) < 6 || f[0] == "IP" {
			continue
		}
		if a, err := netip.ParseAddr(f[0]); err != nil || a != ip {
			continue
		}
		if iface != "" && f[5] != iface {
			continue
		}
		flags, err := strconv.ParseUint(f[2], 0, 32)
		if err != nil || flags&0x2 == 0 {
			continue
		}
		mac, err := net.ParseMAC(f[3])
		if err != nil || isZeroMAC(mac) {
			continue
		}
		return mac
	}
	return nil
}

func isZeroMAC(m net.HardwareAddr) bool {
	for _, b := range m {
		if b != 0 {
			return false
		}
	}
	return true
}
