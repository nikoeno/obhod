//go:build !linux && !windows

package netif

import "syscall"

// BindControl is not supported on this OS.
func BindControl(ifc Interface) func(network, address string, c syscall.RawConn) error {
	return func(string, string, syscall.RawConn) error { return ErrUnsupported }
}

// MarkControl is not supported on this OS.
func MarkControl(mark uint32) func(network, address string, c syscall.RawConn) error {
	return func(string, string, syscall.RawConn) error { return ErrUnsupported }
}
