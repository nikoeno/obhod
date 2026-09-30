// Package store persists decisions per network and domain with atomic writes.
package store

import (
	"errors"
	"fmt"
	"reflect"
	"sort"
	"sync"
	"time"

	"github.com/nikoejento/obhod/internal/lib/checker/lib/model"
	"github.com/nikoejento/obhod/internal/lib/clock"
)

// ErrClosed is returned by writes after Close.
var ErrClosed = errors.New("store: closed")

// Store keeps decisions per network. All methods are goroutine-safe.
type Store interface {
	// Get returns a copy of the decision for domain on net.
	Get(net model.NetworkID, domain string) (model.Decision, bool)
	// All returns copies of every decision on net, sorted by domain.
	All(net model.NetworkID) []model.Decision
	// Put stores decisions for net and marks the network as seen now. Storing a decision equal to the
	// stored one is not a write.
	Put(net model.NetworkID, ds ...model.Decision) error
	// Touch marks net as seen now and records its label (e.g. the ASN name) without writing decisions.
	Touch(net model.NetworkID, label string) error
	// Networks lists the known networks, sorted by ID.
	Networks() []NetworkInfo
	// Prune removes networks not seen since before and, unless keep is nil, decisions for domains not in
	// keep (an empty non-nil keep drops every decision).
	Prune(before time.Time, keep []string) error
	// Warnings returns problems found by Open (e.g. a corrupt file that was set aside).
	Warnings() []string
	// Close flushes pending writes. The store rejects writes afterwards.
	Close() error
}

// NetworkInfo describes one stored network.
type NetworkInfo struct {
	ID       model.NetworkID
	Label    string // e.g. ASN name, for status pages
	LastSeen time.Time
	Count    int
}

// Config configures Open.
type Config struct {
	File          string        // default "<DataDir>/decisions.json" (filled in by the checker)
	FlushInterval time.Duration // coalesce writes; default 0 (immediate), routers: 5m
}

// network is one entry of the file format.
type network struct {
	Label     string                    `json:"label,omitempty"`
	LastSeen  time.Time                 `json:"last_seen"`
	Decisions map[string]model.Decision `json:"decisions"`
}

// store is the single implementation; path == "" means memory only.
type store struct {
	clk      clock.Clock
	path     string
	interval time.Duration
	onWrite  func() // test seam: called after every successful file write

	mu       sync.RWMutex
	nets     map[model.NetworkID]*network
	dirty    bool
	closed   bool
	writeErr error // coalesced-mode failure, reported by the next Put/Touch/Close
	warnings []string

	flushing bool // a coalescing timer goroutine is pending
	done     chan struct{}
	wg       sync.WaitGroup
}

// NewMemory returns a store that never touches the disk (for tests).
func NewMemory() Store {
	return &store{clk: clock.Real(), nets: map[model.NetworkID]*network{}, done: make(chan struct{})}
}

func (s *store) Get(net model.NetworkID, domain string) (model.Decision, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if n := s.nets[net]; n != nil {
		if d, ok := n.Decisions[domain]; ok {
			return d.Clone(), true
		}
	}
	return model.Decision{}, false
}

func (s *store) All(net model.NetworkID) []model.Decision {
	s.mu.RLock()
	defer s.mu.RUnlock()
	n := s.nets[net]
	if n == nil {
		return nil
	}
	out := make([]model.Decision, 0, len(n.Decisions))
	for _, d := range n.Decisions {
		out = append(out, d.Clone())
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Domain < out[j].Domain })
	return out
}

func (s *store) Put(net model.NetworkID, ds ...model.Decision) error {
	if !net.Valid() {
		return fmt.Errorf("store: invalid network id %q", net)
	}
	for _, d := range ds {
		if d.Domain == "" {
			return errors.New("store: decision without domain")
		}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return ErrClosed
	}
	n := s.nets[net]
	if n == nil {
		n = &network{Decisions: map[string]model.Decision{}}
		s.nets[net] = n
		s.dirty = true
	}
	n.LastSeen = s.clk.Now()
	for _, d := range ds {
		if old, ok := n.Decisions[d.Domain]; ok && reflect.DeepEqual(old, d) {
			continue
		}
		n.Decisions[d.Domain] = d.Clone()
		s.dirty = true
	}
	return s.persistLocked()
}

func (s *store) Touch(net model.NetworkID, label string) error {
	if !net.Valid() {
		return fmt.Errorf("store: invalid network id %q", net)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return ErrClosed
	}
	n := s.nets[net]
	if n == nil {
		n = &network{Decisions: map[string]model.Decision{}}
		s.nets[net] = n
		s.dirty = true
	}
	n.LastSeen = s.clk.Now()
	if label != "" && n.Label != label {
		n.Label = label
		s.dirty = true
	}
	return s.persistLocked()
}

func (s *store) Networks() []NetworkInfo {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]NetworkInfo, 0, len(s.nets))
	for id, n := range s.nets {
		out = append(out, NetworkInfo{ID: id, Label: n.Label, LastSeen: n.LastSeen, Count: len(n.Decisions)})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

func (s *store) Prune(before time.Time, keep []string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return ErrClosed
	}
	var keepSet map[string]bool
	if keep != nil {
		keepSet = make(map[string]bool, len(keep))
		for _, d := range keep {
			keepSet[d] = true
		}
	}
	for id, n := range s.nets {
		if n.LastSeen.Before(before) {
			delete(s.nets, id)
			s.dirty = true
			continue
		}
		if keepSet == nil {
			continue
		}
		for dom := range n.Decisions {
			if !keepSet[dom] {
				delete(n.Decisions, dom)
				s.dirty = true
			}
		}
	}
	return s.persistLocked()
}

func (s *store) Warnings() []string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return append([]string(nil), s.warnings...)
}

func (s *store) Close() error {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return nil
	}
	s.closed = true
	close(s.done)
	s.mu.Unlock()
	s.wg.Wait()

	s.mu.Lock()
	defer s.mu.Unlock()
	err := s.writeErr
	s.writeErr = nil
	if s.dirty {
		if werr := s.writeLocked(); werr != nil {
			err = werr
		}
	}
	return err
}

// persistLocked applies the flush policy after a mutation. It returns a pending write error, if any.
func (s *store) persistLocked() error {
	if s.path == "" || !s.dirty {
		return s.takeErrLocked()
	}
	if s.interval <= 0 {
		return s.writeLocked()
	}
	if !s.flushing {
		s.flushing = true
		t := s.clk.NewTimer(s.interval)
		s.wg.Add(1)
		go s.flushAfter(t)
	}
	return s.takeErrLocked()
}

func (s *store) takeErrLocked() error {
	err := s.writeErr
	s.writeErr = nil
	return err
}

// flushAfter waits for the coalescing timer and writes the file once.
func (s *store) flushAfter(t clock.Timer) {
	defer s.wg.Done()
	select {
	case <-t.C():
	case <-s.done:
		t.Stop()
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.flushing = false
	if s.dirty && !s.closed {
		if err := s.writeLocked(); err != nil {
			s.writeErr = err
			// Retry on the next mutation; keep dirty.
		}
	}
}
