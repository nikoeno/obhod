package dialer

import (
	"context"
	"errors"
	"net"
	"syscall"

	"github.com/nikoejento/obhod/internal/lib/analyzer/lib/verdict"
	"github.com/nikoejento/obhod/internal/lib/netif"
	"github.com/nikoejento/obhod/internal/lib/route"
)

// Dialer opens probe connections for one route.
type Dialer interface {
	Route() route.Route
	DialContext(ctx context.Context, network, addr string) (net.Conn, error)
}

// DirectOptions configure the direct dialer.
type DirectOptions struct {
	Uplink    netif.Interface // zero value → netif.Uplink() at construction (only when BindIface is set)
	Mark      uint32          // Linux: optional SO_MARK
	BindIface bool            // pin sockets to the uplink (bypasses a TUN default route)
}

type direct struct{ d net.Dialer }

// NewDirect returns a dialer that connects straight from the physical uplink.
func NewDirect(o DirectOptions) (Dialer, error) {
	var controls []func(network, address string, c syscall.RawConn) error
	if o.BindIface {
		ifc := o.Uplink
		if ifc.Name == "" {
			var err error
			if ifc, err = netif.Uplink(context.Background()); err != nil {
				return nil, verdict.Local(err)
			}
		}
		controls = append(controls, netif.BindControl(ifc))
	}
	if o.Mark != 0 {
		controls = append(controls, netif.MarkControl(o.Mark))
	}
	dd := direct{}
	if len(controls) > 0 {
		dd.d.Control = func(network, address string, c syscall.RawConn) error {
			for _, f := range controls {
				if err := f(network, address, c); err != nil {
					if errors.Is(err, netif.ErrPermission) || errors.Is(err, netif.ErrUnsupported) {
						return verdict.Local(err)
					}
					return err
				}
			}
			return nil
		}
	}
	return dd, nil
}

func (direct) Route() route.Route { return route.Direct }

func (d direct) DialContext(ctx context.Context, network, addr string) (net.Conn, error) {
	return d.d.DialContext(ctx, network, addr)
}
