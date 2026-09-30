//go:build !linux && !windows

package netif

import (
	"context"
	"net"
)

// fillDetails leaves Gateway, GatewayMAC and SSID empty on this OS.
func fillDetails(context.Context, *Interface) {}

// linkUp reports whether the interface is administratively up.
func linkUp(ni net.Interface) bool { return ni.Flags&net.FlagUp != 0 }
