package dialer

import (
	"context"
	"errors"
	"fmt"
	"net"
	"sync"

	"github.com/nikoejento/obhod/internal/lib/analyzer/lib/verdict"
	"github.com/nikoejento/obhod/internal/lib/route"
)

// Step is one scripted dial.
type Step struct {
	Addr string   // if set, the dial must ask for this address
	Err  error    // returned from DialContext
	Conn net.Conn // e.g. one end of net.Pipe
}

type fake struct {
	r     route.Route
	mu    sync.Mutex
	steps []Step
}

// NewFake returns a dialer that serves the steps in order; running out of steps is a local error.
func NewFake(r route.Route, steps ...Step) Dialer { return &fake{r: r, steps: steps} }

func (f *fake) Route() route.Route { return f.r }

func (f *fake) DialContext(ctx context.Context, _, addr string) (net.Conn, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.steps) == 0 {
		return nil, verdict.Local(errors.New("fake dialer: no scripted step left for " + addr))
	}
	s := f.steps[0]
	f.steps = f.steps[1:]
	if s.Addr != "" && s.Addr != addr {
		return nil, verdict.Local(fmt.Errorf("fake dialer: dial %s, script expected %s", addr, s.Addr))
	}
	if s.Err != nil {
		return nil, s.Err
	}
	return s.Conn, nil
}
