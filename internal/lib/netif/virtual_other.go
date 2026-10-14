//go:build !linux && !windows

package netif

import "net"

// osVirtual has no OS source here; the name heuristic decides.
func osVirtual(net.Interface) (virtual, known bool) { return false, false }
