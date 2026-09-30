package store

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nikoejento/obhod/internal/lib/checker/lib/model"
	"github.com/nikoejento/obhod/internal/lib/clock"
)

var t0 = time.Date(2026, 3, 1, 10, 0, 0, 0, time.UTC)

const netA model.NetworkID = "asn:12389"

// dec builds a decision through JSON, which keeps this package's tests free of route/analyzer imports
// (they are not among store's declared libs).
func dec(domain, rt string) model.Decision {
	var d model.Decision
	doc := fmt.Sprintf(`{"domain":%q,"route":%q,"source":"checked","decided_at":%q,"checked_at":%q,"expires_at":%q,
		"pending":{"route":"tunnel","count":1,"since":%q},"last":{"direct":"TLS_RST"}}`,
		domain, rt, t0.Format(time.RFC3339), t0.Format(time.RFC3339), t0.Add(time.Hour).Format(time.RFC3339), t0.Format(time.RFC3339))
	if err := json.Unmarshal([]byte(doc), &d); err != nil {
		panic(err)
	}
	return d
}

func open(t *testing.T, file string, interval time.Duration, clk clock.Clock) *store {
	t.Helper()
	s, err := Open(Config{File: file, FlushInterval: interval}, clk)
	if err != nil {
		t.Fatal(err)
	}
	return s.(*store)
}

func TestRoundTrip(t *testing.T) {
	file := filepath.Join(t.TempDir(), "sub", "decisions.json")
	clk := clock.NewFake(t0)
	s := open(t, file, 0, clk)
	if err := s.Touch(netA, "Rostelecom"); err != nil {
		t.Fatal(err)
	}
	if err := s.Put(netA, dec("a.com", "direct"), dec("b.com", "tunnel")); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}

	s2 := open(t, file, 0, clk)
	defer s2.Close()
	got, ok := s2.Get(netA, "b.com")
	if !ok || got.Route != "tunnel" || got.Pending == nil || got.Last["direct"] != "TLS_RST" || !got.CheckedAt.Equal(t0) {
		t.Errorf("Get = %+v %v", got, ok)
	}
	if all := s2.All(netA); len(all) != 2 || all[0].Domain != "a.com" {
		t.Errorf("All = %+v", all)
	}
	nets := s2.Networks()
	if len(nets) != 1 || nets[0].Label != "Rostelecom" || nets[0].Count != 2 {
		t.Errorf("Networks = %+v", nets)
	}
	if _, ok := s2.Get("asn:1", "a.com"); ok {
		t.Error("unknown network returned a decision")
	}
	if len(s2.Warnings()) != 0 {
		t.Errorf("warnings: %v", s2.Warnings())
	}
}

func TestGetReturnsCopies(t *testing.T) {
	s := NewMemory()
	if err := s.Put(netA, dec("a.com", "direct")); err != nil {
		t.Fatal(err)
	}
	d, _ := s.Get(netA, "a.com")
	d.Pending.Count = 42
	d.Last["direct"] = "OK"
	d2 := s.All(netA)[0]
	if d2.Pending.Count != 1 || d2.Last["direct"] != "TLS_RST" {
		t.Error("store state was mutated through a returned copy")
	}
}

func TestPutValidation(t *testing.T) {
	s := NewMemory()
	if err := s.Put("bogus", dec("a.com", "direct")); err == nil {
		t.Error("invalid network accepted")
	}
	if err := s.Put(netA, model.Decision{}); err == nil {
		t.Error("empty domain accepted")
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	if err := s.Put(netA, dec("a.com", "direct")); err != ErrClosed {
		t.Errorf("Put after Close = %v", err)
	}
}

func TestLeftoverTempIgnoredAndRemoved(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "decisions.json")
	s := open(t, file, 0, clock.NewFake(t0))
	if err := s.Put(netA, dec("a.com", "direct")); err != nil {
		t.Fatal(err)
	}
	s.Close()
	tmp := filepath.Join(dir, "decisions-123.tmp")
	if err := os.WriteFile(tmp, []byte("{half a wri"), 0o600); err != nil {
		t.Fatal(err)
	}
	s2 := open(t, file, 0, clock.NewFake(t0))
	defer s2.Close()
	if _, err := os.Stat(tmp); !os.IsNotExist(err) {
		t.Error("leftover temp file not removed")
	}
	if _, ok := s2.Get(netA, "a.com"); !ok {
		t.Error("committed data lost")
	}
}

func TestCorruptFileRecovery(t *testing.T) {
	for name, body := range map[string]string{
		"bad json":     "{not json",
		"other schema": `{"schema":2,"networks":{}}`,
		"no schema":    `{}`,
	} {
		dir := t.TempDir()
		file := filepath.Join(dir, "decisions.json")
		if err := os.WriteFile(file, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
		s := open(t, file, 0, clock.NewFake(t0))
		if len(s.Networks()) != 0 || len(s.Warnings()) != 1 {
			t.Errorf("%s: nets=%v warnings=%v", name, s.Networks(), s.Warnings())
		}
		aside, _ := filepath.Glob(file + ".corrupt-*")
		if len(aside) != 1 {
			t.Errorf("%s: aside files = %v", name, aside)
		} else if b, _ := os.ReadFile(aside[0]); string(b) != body {
			t.Errorf("%s: aside content changed", name)
		}
		if err := s.Put(netA, dec("a.com", "direct")); err != nil {
			t.Errorf("%s: %v", name, err)
		}
		s.Close()
	}
}

func TestUnchangedPutIsNotAWrite(t *testing.T) {
	var writes atomic.Int32
	s := open(t, filepath.Join(t.TempDir(), "d.json"), 0, clock.NewFake(t0))
	s.onWrite = func() { writes.Add(1) }
	d := dec("a.com", "direct")
	for i := 0; i < 3; i++ {
		if err := s.Put(netA, d); err != nil {
			t.Fatal(err)
		}
	}
	if writes.Load() != 1 {
		t.Errorf("writes = %d, want 1", writes.Load())
	}
	d.Route = "tunnel"
	if err := s.Put(netA, d); err != nil {
		t.Fatal(err)
	}
	if writes.Load() != 2 {
		t.Errorf("writes = %d, want 2", writes.Load())
	}
	if err := s.Touch(netA, ""); err != nil { // LastSeen only
		t.Fatal(err)
	}
	if writes.Load() != 2 {
		t.Error("Touch without a label change wrote")
	}
	s.Close()
}

func TestCoalescedFlush(t *testing.T) {
	clk := clock.NewFake(t0)
	file := filepath.Join(t.TempDir(), "d.json")
	written := make(chan struct{}, 10)
	var writes atomic.Int32
	s := open(t, file, 5*time.Minute, clk)
	s.onWrite = func() { writes.Add(1); written <- struct{}{} }

	for i := 0; i < 5; i++ {
		if err := s.Put(netA, dec(fmt.Sprintf("d%d.com", i), "direct")); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := os.Stat(file); !os.IsNotExist(err) {
		t.Fatal("file written before the flush interval")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := clk.BlockUntil(ctx, 1); err != nil {
		t.Fatal(err)
	}
	clk.Advance(5 * time.Minute)
	select {
	case <-written:
	case <-time.After(5 * time.Second):
		t.Fatal("no flush after the interval")
	}
	if writes.Load() != 1 {
		t.Errorf("writes = %d, want 1", writes.Load())
	}

	// A later mutation schedules a new flush; Close flushes it right away.
	if err := s.Put(netA, dec("late.com", "tunnel")); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	if writes.Load() != 2 {
		t.Errorf("writes after Close = %d, want 2", writes.Load())
	}
	s2 := open(t, file, 0, clk)
	defer s2.Close()
	if len(s2.All(netA)) != 6 {
		t.Errorf("reloaded %d decisions, want 6", len(s2.All(netA)))
	}
}

func TestPrune(t *testing.T) {
	clk := clock.NewFake(t0)
	s := open(t, filepath.Join(t.TempDir(), "d.json"), 0, clk)
	defer s.Close()
	const old model.NetworkID = "asn:1"
	s.Put(old, dec("a.com", "direct"))
	clk.Advance(48 * time.Hour)
	s.Put(netA, dec("a.com", "direct"), dec("gone.com", "direct"))

	if err := s.Prune(t0.Add(time.Hour), []string{"a.com"}); err != nil {
		t.Fatal(err)
	}
	nets := s.Networks()
	if len(nets) != 1 || nets[0].ID != netA || nets[0].Count != 1 {
		t.Errorf("Networks = %+v", nets)
	}
	if _, ok := s.Get(netA, "gone.com"); ok {
		t.Error("decision outside keep survived")
	}
	// nil keep only prunes networks.
	if err := s.Prune(time.Time{}, nil); err != nil || s.Networks()[0].Count != 1 {
		t.Errorf("nil keep pruned decisions: %+v %v", s.Networks(), err)
	}
}

func TestOpenRequiresFile(t *testing.T) {
	if _, err := Open(Config{}, nil); err == nil || !strings.Contains(err.Error(), "File") {
		t.Errorf("err = %v", err)
	}
}

func TestConcurrent(t *testing.T) {
	s := open(t, filepath.Join(t.TempDir(), "d.json"), 0, clock.Real())
	defer s.Close()
	var wg sync.WaitGroup
	for g := 0; g < 4; g++ {
		wg.Add(1)
		go func(g int) {
			defer wg.Done()
			for i := 0; i < 25; i++ {
				dom := fmt.Sprintf("d%d.com", i%5)
				if g%2 == 0 {
					_ = s.Put(netA, dec(dom, "direct"))
				} else {
					s.Get(netA, dom)
					s.All(netA)
					s.Networks()
				}
			}
		}(g)
	}
	wg.Wait()
}
