package checker

import (
	"context"
	"sync"
)

// FakeNetwork is a scripted NetworkSource for tests of the checker and of apps that embed it.
type FakeNetwork struct {
	mu  sync.Mutex
	cur Network
	ch  chan Network
}

// NewFakeNetwork returns a source that starts on initial.
func NewFakeNetwork(initial Network) *FakeNetwork {
	return &FakeNetwork{cur: initial, ch: make(chan Network, 1)}
}

// Set switches the network and announces it on Changes (latest-wins).
func (f *FakeNetwork) Set(n Network) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.cur = n
	select {
	case <-f.ch:
	default:
	}
	f.ch <- n
}

// Current returns the network last set.
func (f *FakeNetwork) Current() Network {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.cur
}

// Changes delivers every Set.
func (f *FakeNetwork) Changes() <-chan Network { return f.ch }

// Run blocks until ctx is done.
func (f *FakeNetwork) Run(ctx context.Context) error {
	<-ctx.Done()
	return nil
}
