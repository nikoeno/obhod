package netif

import (
	"context"
	"net"
	"net/netip"
	"syscall"
	"unsafe"
)

// fillDetails sets Gateway and GatewayMAC from GetAdaptersInfo (IPv4 only).
// SSID stays empty: the WLAN API is not available in the syscall package.
func fillDetails(_ context.Context, ifc *Interface) {
	var size uint32 = 16 * 1024
	var buf []syscall.IpAdapterInfo
	for i := 0; i < 4; i++ {
		n := int(size)/int(unsafe.Sizeof(syscall.IpAdapterInfo{})) + 1
		buf = make([]syscall.IpAdapterInfo, n)
		size = uint32(n) * uint32(unsafe.Sizeof(syscall.IpAdapterInfo{}))
		err := syscall.GetAdaptersInfo(&buf[0], &size)
		if err == nil {
			break
		}
		if err != syscall.ERROR_BUFFER_OVERFLOW || i == 3 {
			return
		}
	}
	for ai := &buf[0]; ai != nil; ai = ai.Next {
		if int(ai.Index) != ifc.Index {
			continue
		}
		for g := &ai.GatewayList; g != nil; g = g.Next {
			if a, err := netip.ParseAddr(cstr(g.IpAddress.String[:])); err == nil && !a.IsUnspecified() {
				ifc.Gateway = a
				break
			}
		}
		// GetAdaptersInfo reports the adapter's own MAC, not the gateway's; the gateway MAC
		// would need GetIpNetTable/SendARP, so it is left unknown.
		return
	}
}

func cstr(b []byte) string {
	for i, c := range b {
		if c == 0 {
			return string(b[:i])
		}
	}
	return string(b)
}

// linkUp reports whether the interface is administratively up.
func linkUp(ni net.Interface) bool { return ni.Flags&net.FlagUp != 0 }
