package netif

import (
	"syscall"
)

const ipUnicastIf = 31 // IP_UNICAST_IF and IPV6_UNICAST_IF

// BindControl returns a net.Dialer.Control func that pins the socket to ifc (IP_UNICAST_IF / IPV6_UNICAST_IF).
func BindControl(ifc Interface) func(network, address string, c syscall.RawConn) error {
	return func(network, _ string, c syscall.RawConn) error {
		idx := uint32(ifc.Index)
		var serr error
		err := c.Control(func(fd uintptr) {
			h := syscall.Handle(fd)
			if len(network) > 0 && network[len(network)-1] == '6' {
				serr = syscall.SetsockoptInt(h, syscall.IPPROTO_IPV6, ipUnicastIf, int(idx))
				return
			}
			// IPv4 takes the index in network byte order.
			be := idx<<24 | (idx&0xff00)<<8 | (idx>>8)&0xff00 | idx>>24
			serr = syscall.SetsockoptInt(h, syscall.IPPROTO_IP, ipUnicastIf, int(int32(be)))
		})
		if err != nil {
			return err
		}
		return serr
	}
}

// MarkControl is not supported on Windows.
func MarkControl(mark uint32) func(network, address string, c syscall.RawConn) error {
	return func(string, string, syscall.RawConn) error { return ErrUnsupported }
}
