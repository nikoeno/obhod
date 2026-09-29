package clock

import (
	"context"
	"testing"
	"time"
)

var t0 = time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

func recv(t *testing.T, ch <-chan time.Time) (time.Time, bool) {
	t.Helper()
	select {
	case v := <-ch:
		return v, true
	default:
		return time.Time{}, false
	}
}

func TestTimersFireInOrder(t *testing.T) {
	f := NewFake(t0)
	a, b := f.NewTimer(2*time.Second), f.NewTimer(time.Second)
	if f.Waiters() != 2 {
		t.Fatal("waiters")
	}
	f.Advance(time.Second)
	if _, ok := recv(t, a.C()); ok {
		t.Fatal("a fired early")
	}
	if v, ok := recv(t, b.C()); !ok || !v.Equal(t0.Add(time.Second)) {
		t.Fatal("b")
	}
	f.Advance(time.Second)
	if _, ok := recv(t, a.C()); !ok {
		t.Fatal("a")
	}
	if f.Waiters() != 0 || f.Since(t0) != 2*time.Second {
		t.Fatal("state")
	}
}

func TestStopReset(t *testing.T) {
	f := NewFake(t0)
	tm := f.NewTimer(time.Second)
	if !tm.Stop() || tm.Stop() {
		t.Fatal("Stop result")
	}
	f.Advance(time.Hour)
	if _, ok := recv(t, tm.C()); ok {
		t.Fatal("stopped timer fired")
	}
	if tm.Reset(time.Second) {
		t.Fatal("Reset of stopped timer must report false")
	}
	f.Advance(time.Second)
	if _, ok := recv(t, tm.C()); !ok {
		t.Fatal("reset timer did not fire")
	}
}

func TestTickerOncePerAdvance(t *testing.T) {
	f := NewFake(t0)
	tk := f.NewTicker(time.Second)
	f.Advance(10 * time.Second)
	if _, ok := recv(t, tk.C()); !ok {
		t.Fatal("no tick")
	}
	if _, ok := recv(t, tk.C()); ok {
		t.Fatal("burst")
	}
	f.Advance(time.Second)
	if _, ok := recv(t, tk.C()); !ok {
		t.Fatal("next tick")
	}
	tk.Stop()
	f.Advance(time.Second)
	if _, ok := recv(t, tk.C()); ok {
		t.Fatal("stopped ticker fired")
	}
}

func TestSleep(t *testing.T) {
	f := NewFake(t0)
	done := make(chan error, 1)
	go func() { done <- f.Sleep(context.Background(), time.Minute) }()
	if err := f.BlockUntil(context.Background(), 1); err != nil {
		t.Fatal(err)
	}
	f.Advance(time.Minute)
	if err := <-done; err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	go func() { done <- f.Sleep(ctx, time.Minute) }()
	if err := f.BlockUntil(context.Background(), 1); err != nil {
		t.Fatal(err)
	}
	cancel()
	if err := <-done; err != context.Canceled {
		t.Fatalf("got %v", err)
	}
}

func TestSetBackwardsPanics(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Fatal("want panic")
		}
	}()
	NewFake(t0).Set(t0.Add(-time.Second))
}

func TestReal(t *testing.T) {
	c := Real()
	if c.Since(c.Now()) < 0 {
		t.Fatal("since")
	}
	if err := c.Sleep(context.Background(), time.Millisecond); err != nil {
		t.Fatal(err)
	}
}
