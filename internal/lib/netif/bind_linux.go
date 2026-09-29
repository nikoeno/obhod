package netif

import (
	"os"
	"syscall"
)

// BindControl returns a net.Dialer.Control func that pins the socket to ifc (SO_BINDTODEVICE).
func BindControl(ifc Interface) func(network, address string, c syscall.RawConn) error {
	return func(_, _ string, c syscall.RawConn) error {
		var serr error
		if err := c.Control(func(fd uintptr) { serr = syscall.BindToDevice(int(fd), ifc.Name) }); err != nil {
			return err
		}
		return mapPerm(serr)
	}
}

// MarkControl sets SO_MARK on the socket.
func MarkControl(mark uint32) func(network, address string, c syscall.RawConn) error {
	return func(_, _ string, c syscall.RawConn) error {
		var serr error
		if err := c.Control(func(fd uintptr) { serr = syscall.SetsockoptInt(int(fd), syscall.SOL_SOCKET, 36, int(mark)) }); err != nil {
			return err
		}
		return mapPerm(serr)
	}
}

func mapPerm(err error) error {
	if err == syscall.EPERM || err == syscall.EACCES || os.IsPermission(err) {
		return ErrPermission
	}
	return err
}
