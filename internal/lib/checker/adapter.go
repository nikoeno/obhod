package checker

import (
	"context"
	"sync"

	"github.com/nikoejento/obhod/internal/lib/checker/netwatch"
)

// netwatchSource adapts a netwatch.Watcher to NetworkSource.
type netwatchSource struct {
	w       *netwatch.Watcher
	changes chan Network
}

func newNetwatchSource(w *netwatch.Watcher) *netwatchSource {
	return &netwatchSource{w: w, changes: make(chan Network, 1)}
}

func convert(n netwatch.Network) Network {
	return Network{ID: string(n.ID), ASN: n.ASN, ASName: n.ASName, Known: n.Known}
}

func (s *netwatchSource) Current() Network { return convert(s.w.Current()) }

func (s *netwatchSource) Changes() <-chan Network { return s.changes }

// Run runs the watcher and forwards its changes (latest-wins) until ctx is done.
func (s *netwatchSource) Run(ctx context.Context) error {
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		s.w.Run(ctx)
	}()
	defer wg.Wait()
	for {
		select {
		case <-ctx.Done():
			return nil
		case n := <-s.w.Changes():
			select {
			case <-s.changes:
			default:
			}
			s.changes <- convert(n) // sole sender, just drained: never blocks
		}
	}
}
