package netif

import (
	"net"
	"os"
	"path/filepath"
	"strings"
)

// sysClassNet is the sysfs directory of network interfaces.
const sysClassNet = "/sys/class/net"

// ARP hardware types (linux/if_arp.h) of interfaces without a link layer.
const (
	arphrdNone     = "65534" // TUN (IFF_TUN), WireGuard
	arphrdLoopback = "772"
)

// osVirtual reads sysfs: a TUN/TAP device has a tun_flags file, and TUN or
// WireGuard devices have no link layer (type 65534). known is false when the
// interface has no sysfs entry. Bridges and VLANs are not judged here, so a
// bridge that carries the uplink still counts as physical.
func osVirtual(ni net.Interface) (virtual, known bool) {
	return osVirtualAt(sysClassNet, ni)
}

// osVirtualAt is osVirtual with the sysfs directory as a parameter (tests).
func osVirtualAt(root string, ni net.Interface) (virtual, known bool) {
	dir := filepath.Join(root, ni.Name)
	if _, err := os.Stat(dir); err != nil {
		return false, false
	}
	if _, err := os.Stat(filepath.Join(dir, "tun_flags")); err == nil {
		return true, true
	}
	if b, err := os.ReadFile(filepath.Join(dir, "type")); err == nil {
		switch strings.TrimSpace(string(b)) {
		case arphrdNone, arphrdLoopback:
			return true, true
		}
	}
	return false, true
}
