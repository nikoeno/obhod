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

	// Server management (ADR 0004 milestone 4). The gateway reaches the
	// servers types only through the pc.Server alias.
	Servers() ([]pc.Server, error)
	AddServer(ctx context.Context, input, name string) (pc.Server, error)
	ActivateServer(ctx context.Context, id string) (reloaded bool, err error)
	DeleteServer(ctx context.Context, id string) error
	TestServer(ctx context.Context, id, input string) (protocol string, err error)

	// Per-site rules (user-approved 2026-11-01). The gateway reaches the rule type
	// only through the pc.SiteRule alias; pc validates, stores and applies.
	Sites() ([]pc.SiteRule, error)
	SetSite(domain, action string) (rule pc.SiteRule, created bool, err error)
	DeleteSite(domain string) error
}

var _ engine = (*pc.App)(nil)
