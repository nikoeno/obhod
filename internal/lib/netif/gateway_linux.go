package netif

import (
	"context"
	"net"
	"os"
)

// fillDetails sets Gateway and GatewayMAC from /proc/net/route and /proc/net/arp.
// SSID stays empty: reading it needs wireless ioctls or external tools.
func fillDetails(_ context.Context, ifc *Interface) {
	f, err := os.Open("/proc/net/route")
	if err != nil {
		return
	}
	routes := parseRouteTable(f)
	f.Close()
	rt, ok := pickDefaultRoute(routes, ifc.Name)
	if !ok {
		return
	}
	ifc.Gateway = rt.Gateway
	if a, err := os.Open("/proc/net/arp"); err == nil {
		ifc.GatewayMAC = parseARPTable(a, rt.Gateway, ifc.Name)
		a.Close()
	}
}

// linkUp reports whether the interface is administratively up with carrier.
func linkUp(ni net.Interface) bool {
	return ni.Flags&net.FlagUp != 0 && ni.Flags&net.FlagRunning != 0
}
