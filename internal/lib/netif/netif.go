package netif

import (
	"context"
	"errors"
	"net"
	"net/netip"
	"strings"
)

// Errors returned by this package.
var (
	ErrNoUplink    = errors.New("netif: no physical uplink found")
	ErrPermission  = errors.New("netif: permission denied")
	ErrUnsupported = errors.New("netif: unsupported on this OS")
)

// Interface describes one network interface.
type Interface struct {
	Name       string
	Index      int
	Addrs      []netip.Prefix
	Gateway    netip.Addr       // invalid if unknown
	GatewayMAC net.HardwareAddr // nil if unknown
	SSID       string           // "" if not Wi-Fi or unknown
	Virtual    bool             // TUN/TAP/wintun/loopback
}

var virtualHints = []string{"tun", "tap", "wintun", "wg", "sing", "utun", "meta", "clash", "vpn", "veth", "docker", "vethernet", "loopback", "virtual"}

// isVirtualName applies the name heuristics for virtual adapters.
func isVirtualName(name string) bool {
	n := strings.ToLower(name)
	for _, h := range virtualHints {
		if strings.Contains(n, h) {
			return true
		}
	}
	return false
}

func fromNet(ni net.Interface) Interface {
	ifc := Interface{Name: ni.Name, Index: ni.Index, Virtual: ni.Flags&net.FlagLoopback != 0 || isVirtualName(ni.Name)}
	addrs, _ := ni.Addrs()
	for _, a := range addrs {
		if p, err := netip.ParsePrefix(a.String()); err == nil {
			ifc.Addrs = append(ifc.Addrs, p)
		}
	}
	return ifc
}

// List returns all interfaces that are up.
func List(ctx context.Context) ([]Interface, error) {
	nis, err := net.Interfaces()
	if err != nil {
		return nil, err
	}
	var out []Interface
	for _, ni := range nis {
		if ni.Flags&net.FlagUp == 0 {
			continue
		}
		out = append(out, fromNet(ni))
	}
	return out, ctx.Err()
}

// Uplink returns the physical interface that carries IPv4 traffic to the internet, skipping virtual ones.
// The kernel's choice is learned with a connected UDP socket (no packet is sent); when that choice is a
// virtual adapter (a TUN client owns the default route), the first physical interface with an IPv4 address is used.
func Uplink(ctx context.Context) (Interface, error) {
	all, err := List(ctx)
	if err != nil {
		return Interface{}, err
	}
	var d net.Dialer
	if c, err := d.DialContext(ctx, "udp4", "192.0.2.1:9"); err == nil {
		local, _ := netip.ParseAddrPort(c.LocalAddr().String())
		c.Close()
		for _, ifc := range all {
			for _, p := range ifc.Addrs {
				if p.Addr() == local.Addr() && !ifc.Virtual {
					return ifc, nil
				}
			}
		}
	}
	for _, ifc := range all {
		if ifc.Virtual {
			continue
		}
		for _, p := range ifc.Addrs {
			if p.Addr().Is4() && !p.Addr().IsLinkLocalUnicast() {
				return ifc, nil
			}
		}
	}
	return Interface{}, ErrNoUplink
}
