package clock

import (
	"context"
	"errors"
	"sort"
	"sync"
	"time"
)

// Clock is the injectable source of time.
type Clock interface {
	Now() time.Time
	Since(t time.Time) time.Duration
	NewTimer(d time.Duration) Timer
	NewTicker(d time.Duration) Ticker
	// Sleep waits for d or until ctx is done; returns ctx.Err() if cancelled.
	Sleep(ctx context.Context, d time.Duration) error
}

// Timer mirrors time.Timer.
type Timer interface {
	C() <-chan time.Time
	Stop() bool
	Reset(d time.Duration) bool
}

// Ticker mirrors time.Ticker.
type Ticker interface {
	C() <-chan time.Time
	Stop()
	Reset(d time.Duration)
}

// Real returns the clock backed by the time package.
func Real() Clock { return realClock{} }

type realClock struct{}

func (realClock) Now() time.Time                  { return time.Now() }
func (realClock) Since(t time.Time) time.Duration { return time.Since(t) }
func (realClock) NewTimer(d time.Duration) Timer  { return realTimer{time.NewTimer(d)} }
func (realClock) NewTicker(d time.Duration) Ticker {
	return realTicker{time.NewTicker(d)}
}
func (realClock) Sleep(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

type realTimer struct{ t *time.Timer }

func (r realTimer) C() <-chan time.Time        { return r.t.C }
func (r realTimer) Stop() bool                 { return r.t.Stop() }
func (r realTimer) Reset(d time.Duration) bool { return r.t.Reset(d) }

type realTicker struct{ t *time.Ticker }

func (r realTicker) C() <-chan time.Time   { return r.t.C }
func (r realTicker) Stop()                 { r.t.Stop() }
func (r realTicker) Reset(d time.Duration) { r.t.Reset(d) }

// Fake is a manually advanced clock for tests.
type Fake struct {
	mu      sync.Mutex
	now     time.Time
	waiters []*fakeWaiter
}

type fakeWaiter struct {
	f      *Fake
	ch     chan time.Time
	when   time.Time
	period time.Duration // > 0 for tickers
	active bool
}

// NewFake returns a fake clock starting at start.
func NewFake(start time.Time) *Fake { return &Fake{now: start} }

// Now returns the fake time.
func (f *Fake) Now() time.Time {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.now
}

// Since returns the fake time elapsed since t.
func (f *Fake) Since(t time.Time) time.Duration { return f.Now().Sub(t) }

func (f *Fake) add(d, period time.Duration) *fakeWaiter {
	f.mu.Lock()
	defer f.mu.Unlock()
	w := &fakeWaiter{f: f, ch: make(chan time.Time, 1), when: f.now.Add(d), period: period, active: true}
	f.waiters = append(f.waiters, w)
	return w
}

// NewTimer returns a timer that fires when the clock is advanced past d.
func (f *Fake) NewTimer(d time.Duration) Timer { return fakeTimer{f.add(d, 0)} }

// NewTicker returns a ticker that fires at most once per Advance call.
func (f *Fake) NewTicker(d time.Duration) Ticker {
	if d <= 0 {
		panic("clock: non-positive interval for NewTicker")
	}
	return fakeTicker{f.add(d, d)}
}

// Sleep waits until the clock is advanced by d or ctx is done.
func (f *Fake) Sleep(ctx context.Context, d time.Duration) error {
	t := f.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C():
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// Advance moves the clock forward by d, firing due timers and tickers in deadline order.
func (f *Fake) Advance(d time.Duration) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.advanceLocked(f.now.Add(d))
}

// Set moves the clock to t, which must not be earlier than the current time.
func (f *Fake) Set(t time.Time) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if t.Before(f.now) {
		panic("clock: Fake.Set moves time backwards")
	}
	f.advanceLocked(t)
}

func (f *Fake) advanceLocked(target time.Time) {
	var due []*fakeWaiter
	for _, w := range f.waiters {
		if w.active && !w.when.After(target) {
			due = append(due, w)
		}
	}
	sort.SliceStable(due, func(i, j int) bool { return due[i].when.Before(due[j].when) })
	for _, w := range due {
		select {
		case w.ch <- w.when:
		default:
		}
		if w.period > 0 {
			next := w.when.Add(w.period)
			for !next.After(target) {
				next = next.Add(w.period)
			}
			w.when = next
		} else {
			w.active = false
		}
	}
	f.now = target
	live := f.waiters[:0]
	for _, w := range f.waiters {
		if w.active {
			live = append(live, w)
		}
	}
	f.waiters = live
}

// Waiters returns the number of pending timers, tickers and sleeps.
func (f *Fake) Waiters() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := 0
	for _, w := range f.waiters {
		if w.active {
			n++
		}
	}
	return n
}

// BlockUntil waits (real time, bounded by ctx and 10 s) until at least n waiters exist.
func (f *Fake) BlockUntil(ctx context.Context, n int) error {
	deadline := time.Now().Add(10 * time.Second)
	for f.Waiters() < n {
		if err := ctx.Err(); err != nil {
			return err
		}
		if time.Now().After(deadline) {
			return errors.New("clock: BlockUntil timed out")
		}
		time.Sleep(time.Millisecond)
	}
	return nil
}

type fakeTimer struct{ w *fakeWaiter }

func (t fakeTimer) C() <-chan time.Time { return t.w.ch }

func (t fakeTimer) Stop() bool {
	t.w.f.mu.Lock()
	defer t.w.f.mu.Unlock()
	was := t.w.active
	t.w.active = false
	return was
}

func (t fakeTimer) Reset(d time.Duration) bool {
	f := t.w.f
	f.mu.Lock()
	defer f.mu.Unlock()
	was := t.w.active
	t.w.when = f.now.Add(d)
	if !was {
		t.w.active = true
		f.waiters = append(f.waiters, t.w)
	}
	return was
}

type fakeTicker struct{ w *fakeWaiter }

func (t fakeTicker) C() <-chan time.Time { return t.w.ch }

func (t fakeTicker) Stop() {
	t.w.f.mu.Lock()
	defer t.w.f.mu.Unlock()
	t.w.active = false
}

func (t fakeTicker) Reset(d time.Duration) {
	f := t.w.f
	f.mu.Lock()
	defer f.mu.Unlock()
	t.w.period = d
	t.w.when = f.now.Add(d)
	if !t.w.active {
		t.w.active = true
		f.waiters = append(f.waiters, t.w)
	}
}
