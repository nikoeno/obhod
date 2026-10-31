package desktop

import (
	"context"

	"github.com/nikoejento/obhod/internal/pc"
	"github.com/nikoejento/obhod/internal/pc/status"
)

// engine is what the control API needs from the app. *pc.App satisfies it; the
// tests use a fake. Every method is safe for concurrent use.
type engine interface {
	Run(ctx context.Context) error
	Ready() <-chan struct{}

	Connect(ctx context.Context) error
	Disconnect(ctx context.Context) error
	State() pc.State
	Snapshot() status.Snapshot
	Routes() []pc.RouteInfo
	Networks() []pc.NetworkInfo
	Checking() bool
	CheckNow(domains ...string) error
	LastError() *pc.OpError
	Logs(name string) ([]string, error)
	Subscribe() (<-chan pc.Event, func())
}

var _ engine = (*pc.App)(nil)
