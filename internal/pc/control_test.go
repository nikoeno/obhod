package pc

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/nikoejento/obhod/internal/lib/route"
	"github.com/nikoejento/obhod/internal/pc/desync"
	"github.com/nikoejento/obhod/internal/pc/lib/clashapi"
	"github.com/nikoejento/obhod/internal/pc/monitor"
	"github.com/nikoejento/obhod/internal/pc/procman"
)

// next waits for the next event (any kind in kinds) and returns it.
func next(t *testing.T, ch <-chan Event, kinds ...EventKind) Event {
	t.Helper()
	deadline := time.After(10 * time.Second)
	for {
		select {
		case ev, ok := <-ch:
			if !ok {
				t.Fatal("event channel closed")
			}
			for _, k := range kinds {
				if ev.Kind == k {
					return ev
				}
			}
		case <-deadline:
			t.Fatalf("no %v event", kinds)
		}
	}
}

func drain(ch <-chan Event) {
	for {
		select {
		case <-ch:
		default:
			return
		}
	}
}

func TestReadyAfterPreflight(t *testing.T) {
	h := newHarness(t, baseTable(t), WithUI(true))
	select {
	case <-h.app.Ready():
		t.Fatal("Ready closed before Run")
	default:
	}
	h.start()
	select {
	case <-h.app.Ready():
	case <-time.After(10 * time.Second):
		t.Fatal("Ready not closed")
	}
	if h.app.State() != StateDisconnected {
		t.Fatalf("state at Ready = %s", h.app.State())
	}

	h = newHarness(t, baseTable(t))
	h.admin = false
	_ = h.app.Run(context.Background())
	select {
	case <-h.app.Ready():
		t.Fatal("Ready closed although the pre-flight failed")
	default:
	}
}

func TestSnapshotHasState(t *testing.T) {
	h := newHarness(t, baseTable(t), WithUI(true))
	if s := h.app.Snapshot(); s.State != string(StateDisconnected) || s.Version == "" {
		t.Fatalf("snapshot = %+v", s)
	}
}

func TestStateEventsFollowTransitions(t *testing.T) {
	h := newHarness(t, baseTable(t), WithUI(true))
	h.start()
	h.waitOp("status.start")
	ch, unsub := h.app.Subscribe()
	defer unsub()
	if err := h.app.Connect(context.Background()); err != nil {
		t.Fatal(err)
	}
	var seen []State
	for len(seen) < 2 {
		next(t, ch, EventState)
		seen = append(seen, h.app.State())
		if h.app.State() == StateConnected {
			break
		}
	}
	if h.app.State() != StateConnected {
		t.Fatalf("state = %s", h.app.State())
	}
	if err := h.app.Disconnect(context.Background()); err != nil {
		t.Fatal(err)
	}
	for h.app.State() != StateDisconnected {
		next(t, ch, EventState)
	}
}

func TestRoutesSinceHistoryAndEvents(t *testing.T) {
	h := newHarness(t, baseTable(t), WithUI(true))
	h.start()
	h.waitOp("status.start")
	start := h.fc.Now()
	ch, unsub := h.app.Subscribe()
	defer unsub()

	got := h.app.Routes()
	if len(got) == 0 {
		t.Fatal("no routes")
	}
	for _, r := range got {
		if !r.Since.Equal(start) || len(r.History) != 0 {
			t.Fatalf("first table entry: %+v (start %v)", r, start)
		}
	}
	h.fc.Advance(time.Minute)
	changed := h.fc.Now()
	h.chk.mu.Lock()
	h.chk.lastCheck = changed
	h.chk.mu.Unlock()
	h.publishAndFire(tbl(t, ent("yt.example", route.Direct), ent("blocked.example", route.Direct), ent("plain.example", route.Direct), ent("new.example", route.Tunnel)))
	ev := next(t, ch, EventRoutes)
	if len(ev.Changes) == 0 {
		t.Fatalf("routes event without changes: %+v", ev)
	}
	waitUntil(t, "history", func() bool {
		for _, r := range h.app.Routes() {
			if r.Domain == "yt.example" && len(r.History) == 1 {
				return true
			}
		}
		return false
	})
	for _, r := range h.app.Routes() {
		switch r.Domain {
		case "yt.example":
			if !r.Since.Equal(changed) || r.History[0].To != route.Direct || !r.LastCheck.Equal(changed) {
				t.Fatalf("changed entry: %+v", r)
			}
		case "new.example": // added: new since, no history
			if !r.Since.Equal(changed) || len(r.History) != 0 {
				t.Fatalf("added entry: %+v", r)
			}
		}
	}
}

func TestHistoryIsBounded(t *testing.T) {
	h := newHarness(t, baseTable(t), WithUI(true))
	for i := 0; i < 15; i++ {
		r := route.Direct
		if i%2 == 0 {
			r = route.Tunnel
		}
		h.app.setTable(tbl(t, ent("flip.example", r)))
	}
	rs := h.app.Routes()
	if len(rs) != 1 || len(rs[0].History) != maxHistory {
		t.Fatalf("routes = %+v", rs)
	}
	h.app.setTable(tbl(t))
	if len(h.app.Routes()) != 0 || len(h.app.history) != 0 || len(h.app.since) != 0 {
		t.Fatal("removed entries kept their data")
	}
}

func TestCheckNow(t *testing.T) {
	h := newHarness(t, tbl(t, ent("yt.example", route.Desync), ent("plain.example", route.Direct)), WithUI(true))
	h.start()
	h.waitOp("status.start")
	ch, unsub := h.app.Subscribe()
	defer unsub()

	if err := h.app.CheckNow("nope.example"); !errors.Is(err, ErrUnknownDomain) {
		t.Fatalf("unknown domain: %v", err)
	}
	if err := h.app.CheckNow("not a domain"); !errors.Is(err, route.ErrInvalidDomain) {
		t.Fatalf("invalid domain: %v", err)
	}
	if len(h.chk.checkNow) != 0 {
		t.Fatal("checker was called for a rejected request")
	}
	if err := h.app.CheckNow(" YT.example. ", "nope.example"); err != nil {
		t.Fatal(err)
	}
	if len(h.chk.checkNow) != 1 || strings.Join(h.chk.checkNow[0], ",") != "yt.example" {
		t.Fatalf("checker calls: %v", h.chk.checkNow)
	}
	ev := next(t, ch, EventCheck)
	if !ev.Check.Running || strings.Join(ev.Check.Domains, ",") != "yt.example,nope.example" {
		t.Fatalf("start event: %+v", ev.Check)
	}
	if err := h.app.CheckNow(); err != nil { // all candidates; a check is already announced
		t.Fatal(err)
	}

	// The checker reports Running, then not: the end event follows with the changes.
	h.chk.mu.Lock()
	h.chk.running = true
	h.chk.mu.Unlock()
	if !h.app.Checking() {
		t.Fatal("Checking is false")
	}
	h.ev.Advance(pollInterval)
	waitUntil(t, "seen", func() bool { h.app.chkMu.Lock(); defer h.app.chkMu.Unlock(); return h.app.chkSeen })
	h.app.setTable(tbl(t, ent("yt.example", route.Tunnel), ent("plain.example", route.Direct)))
	h.chk.mu.Lock()
	h.chk.running = false
	h.chk.mu.Unlock()
	h.ev.Advance(pollInterval)
	end := next(t, ch, EventCheck)
	if end.Check.Running || end.Check.Changed != 1 {
		t.Fatalf("end event: %+v", end.Check)
	}
	next(t, ch, EventState) // the end of a check refreshes the state
}

func TestCheckEventsForScheduledCheckAndGrace(t *testing.T) {
	h := newHarness(t, baseTable(t), WithUI(true))
	h.start()
	h.waitOp("status.start")
	ch, unsub := h.app.Subscribe()
	defer unsub()

	h.chk.mu.Lock()
	h.chk.running = true
	h.chk.mu.Unlock()
	h.ev.Advance(pollInterval)
	if ev := next(t, ch, EventCheck); !ev.Check.Running || ev.Check.Domains != nil {
		t.Fatalf("scheduled start: %+v", ev.Check)
	}
	h.chk.mu.Lock()
	h.chk.running = false
	h.chk.mu.Unlock()
	h.ev.Advance(pollInterval)
	if ev := next(t, ch, EventCheck); ev.Check.Running {
		t.Fatalf("scheduled end: %+v", ev.Check)
	}

	// A manual check that never shows as running still ends.
	if err := h.app.CheckNow(); err != nil {
		t.Fatal(err)
	}
	next(t, ch, EventCheck)
	deadline := time.Now().Add(10 * time.Second)
	for len(ch) == 0 { // one Advance fires a ticker once; wait for the poll to be handled
		h.ev.Advance(pollInterval)
		time.Sleep(5 * time.Millisecond)
		if time.Now().After(deadline) {
			t.Fatal("a check that never ran did not end")
		}
	}
	if ev := next(t, ch, EventCheck); ev.Check.Running {
		t.Fatalf("grace end: %+v", ev.Check)
	}
}

func TestTrafficTickWhileConnected(t *testing.T) {
	h := newHarness(t, baseTable(t), WithUI(true))
	h.d.newClash = func(string, string) clashapi.Client {
		return clashapi.NewFake(clashapi.Snapshot{UploadTotal: 7, DownloadTotal: 9, Connections: []clashapi.Connection{{ID: "1"}, {ID: "2"}}})
	}
	h.start()
	h.waitOp("status.start")
	ch, unsub := h.app.Subscribe()
	defer unsub()

	h.ev.Advance(trafficInterval)
	time.Sleep(30 * time.Millisecond)
	for len(ch) > 0 {
		if ev := <-ch; ev.Kind == EventTraffic {
			t.Fatal("traffic tick while disconnected")
		}
	}
	if err := h.app.Connect(context.Background()); err != nil {
		t.Fatal(err)
	}
	drain(ch)
	h.mon.stat = monitor.Stats{Bytes: map[route.Route]int64{route.Tunnel: 100, route.Direct: 300}}
	h.ev.Advance(trafficInterval)
	ev := next(t, ch, EventTraffic)
	tk := ev.Traffic
	if tk.Upload != 7 || tk.Download != 9 || tk.Connections != 2 || tk.Delta[route.Direct] != 300 || tk.Total[route.Tunnel] != 100 || tk.Savings != 0.75 || tk.At.IsZero() {
		t.Fatalf("tick = %+v", tk)
	}
	h.mon.stat = monitor.Stats{Bytes: map[route.Route]int64{route.Tunnel: 150, route.Direct: 300}}
	h.ev.Advance(trafficInterval)
	tk = next(t, ch, EventTraffic).Traffic
	if tk.Delta[route.Tunnel] != 50 || len(tk.Delta) != 1 {
		t.Fatalf("second tick delta = %v", tk.Delta)
	}
}

func TestSlowSubscriberIsDroppedAndOthersContinue(t *testing.T) {
	h := newHarness(t, baseTable(t), WithUI(true))
	slow, _ := h.app.Subscribe()
	fast, unsub := h.app.Subscribe()
	defer unsub()
	done := make(chan struct{})
	go func() {
		defer close(done)
		for i := 0; i < subscriberBuffer+10; i++ {
			h.app.publish(Event{Kind: EventState})
			<-fast
		}
	}()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("publish blocked")
	}
	n := 0
	for range slow { // closed after the buffer filled
		n++
	}
	if n != subscriberBuffer {
		t.Fatalf("slow subscriber got %d events", n)
	}
	unsub()
	unsub() // a second call is harmless
}

func TestLastErrorClassification(t *testing.T) {
	connect := func(t *testing.T, h *harness) error {
		t.Helper()
		h.start()
		h.waitOp("status.start")
		return h.app.Connect(context.Background())
	}

	t.Run("permission_denied", func(t *testing.T) {
		h := newHarness(t, baseTable(t), WithUI(true))
		h.procs.startErr[nameSingBox] = procman.ErrNeedsAdmin
		if err := connect(t, h); err == nil {
			t.Fatal("connect succeeded")
		}
		le := h.app.LastError()
		if le == nil || le.Code != CodePermissionDenied || len(le.Commands) != 0 {
			t.Fatalf("LastError = %+v", le)
		}
		if h.app.State() != StateDisconnected {
			t.Fatalf("state = %s", h.app.State())
		}
	})
	t.Run("needs_setup", func(t *testing.T) {
		h := newHarness(t, baseTable(t), WithUI(true))
		h.app.cfg.Desync.Engine = desync.NFQWS
		h.nft = false
		ch, unsub := h.app.Subscribe()
		defer unsub()
		if err := connect(t, h); err == nil {
			t.Fatal("connect succeeded")
		}
		le := h.app.LastError()
		if le == nil || le.Code != CodeNeedsSetup || len(le.Commands) == 0 || !strings.Contains(strings.Join(le.Commands, "\n"), "nft add table inet splitter") {
			t.Fatalf("LastError = %+v", le)
		}
		for _, c := range le.Commands {
			if strings.Contains(c, "delete") {
				t.Errorf("teardown command listed as setup: %s", c)
			}
		}
		// The final disconnected event already carries the error.
		var last Event
		for {
			ev := next(t, ch, EventState)
			last = ev
			if h.app.State() == StateDisconnected && len(ch) == 0 {
				break
			}
		}
		_ = last
		if h.app.LastError() == nil {
			t.Fatal("error lost")
		}
	})
	t.Run("tunnel_missing", func(t *testing.T) {
		h2 := newHarness(t, baseTable(t), WithUI(true)) // host-name tunnel that does not resolve
		h2.app.outbounds.Tunnel = []byte(`{"type":"vless","server":"vpn.example.org","server_port":443}`)
		h2.app.cfg.Desync.ExcludeIPs = nil
		if err := connect(t, h2); err == nil {
			t.Fatal("connect succeeded")
		}
		if le := h2.app.LastError(); le == nil || le.Code != CodeTunnelMissing {
			t.Fatalf("LastError = %+v", le)
		}
	})
	t.Run("connect_failed", func(t *testing.T) {
		h := newHarness(t, baseTable(t), WithUI(true))
		h.procs.startErr[nameSingBox] = errors.New("boom at 203.0.113.7")
		if err := connect(t, h); err == nil {
			t.Fatal("connect succeeded")
		}
		le := h.app.LastError()
		if le == nil || le.Code != CodeConnectFailed || strings.Contains(le.Message, "203.0.113.7") {
			t.Fatalf("LastError = %+v", le)
		}
		// The next Connect clears it.
		delete(h.procs.startErr, nameSingBox)
		if err := h.app.Connect(context.Background()); err != nil {
			t.Fatal(err)
		}
		if h.app.LastError() != nil {
			t.Fatal("error not cleared by Connect")
		}
	})
	t.Run("crashed", func(t *testing.T) {
		h := newHarness(t, baseTable(t), WithUI(true))
		if err := connect(t, h); err != nil {
			t.Fatal(err)
		}
		h.procs.crash(nameSingBox)
		waitUntil(t, "disconnected", func() bool { return h.app.State() == StateDisconnected })
		if le := h.app.LastError(); le == nil || le.Code != CodeCrashed {
			t.Fatalf("LastError = %+v", le)
		}
	})
	t.Run("cancelled connect is no error", func(t *testing.T) {
		h := newHarness(t, baseTable(t), WithUI(true))
		h.start()
		h.waitOp("status.start")
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		_ = h.app.Connect(ctx)
		if le := h.app.LastError(); le != nil {
			t.Fatalf("LastError = %+v", le)
		}
	})
}

func TestClassify(t *testing.T) {
	if Classify(nil) != nil {
		t.Fatal("nil")
	}
	cases := map[string]struct {
		err  error
		code string
	}{
		"admin":   {fail(ExitManual, "x: %w", procman.ErrNeedsAdmin), CodePermissionDenied},
		"manual":  {fail(ExitManual, "cannot check"), CodePermissionDenied},
		"setup":   {&exitError{code: ExitManual, err: errors.New("x"), commands: []string{"nft add table inet splitter"}}, CodeNeedsSetup},
		"tunnel":  {fail(ExitConfig, "%w", errTunnelUnresolved), CodeTunnelMissing},
		"no tun":  {fail(ExitConfig, "sing-box config: %w", errors.Join(errors.New("x"))), CodeConnectFailed},
		"runtime": {fail(ExitRuntime, "boom"), CodeConnectFailed},
		"plain":   {errors.New("boom"), CodeConnectFailed},
	}
	for name, c := range cases {
		if got := Classify(c.err); got == nil || got.Code != c.code || got.Message == "" {
			t.Errorf("%s: %+v, want %s", name, got, c.code)
		}
	}
}

func TestLogRing(t *testing.T) {
	r := NewLogRing(3)
	_, _ = r.Write([]byte("a\nb\r\nc"))
	if got := strings.Join(r.Lines(), "|"); got != "a|b" {
		t.Fatalf("lines = %q", got)
	}
	_, _ = r.Write([]byte("\nd\ne\n"))
	if got := strings.Join(r.Lines(), "|"); got != "c|d|e" {
		t.Fatalf("lines = %q", got)
	}
	if NewLogRing(0).max != 1 {
		t.Fatal("minimum size")
	}
}

func TestLogs(t *testing.T) {
	ring := NewLogRing(500)
	h := newHarness(t, baseTable(t), WithUI(true), WithLogRing(ring))
	for i := 0; i < 250; i++ {
		_, _ = ring.Write([]byte("line\n"))
	}
	h.procs.logs[nameSingBox] = []string{"sb1", "sb2"}
	h.procs.logs["winws2"] = []string{"ws1"}

	if l, err := h.app.Logs("splitter"); err != nil || len(l) != 200 {
		t.Fatalf("splitter: %d lines, %v", len(l), err)
	}
	if l, _ := h.app.Logs("sing-box"); strings.Join(l, ",") != "sb1,sb2" {
		t.Fatalf("sing-box: %v", l)
	}
	if l, _ := h.app.Logs("desync"); strings.Join(l, ",") != "ws1" {
		t.Fatalf("desync: %v", l)
	}
	h.procs.logs["winws2"] = nil
	if l, err := h.app.Logs("desync"); err != nil || l == nil || len(l) != 0 {
		t.Fatalf("empty desync: %#v, %v", l, err)
	}
	if _, err := h.app.Logs("nope"); !errors.Is(err, ErrUnknownLog) {
		t.Fatalf("unknown: %v", err)
	}
	// No ring: empty, not an error.
	h2 := newHarness(t, baseTable(t))
	if l, err := h2.app.Logs("splitter"); err != nil || l == nil || len(l) != 0 {
		t.Fatalf("no ring: %#v, %v", l, err)
	}
}

func TestNetworksSeenThisRun(t *testing.T) {
	h := newHarness(t, baseTable(t), WithUI(true))
	ns := h.app.Networks()
	if len(ns) != 1 || ns[0].Label != "AS64500 Example" || ns[0].Since.IsZero() {
		t.Fatalf("networks = %+v", ns)
	}
	if len(h.app.Networks()) != 1 {
		t.Fatal("the same network was recorded twice")
	}
	for i := 0; i < 12; i++ {
		h.app.noteNetwork(true, "asn:"+string(rune('a'+i)), "AS")
	}
	if got := h.app.Networks(); len(got) != maxNetworks {
		t.Fatalf("%d networks kept", len(got))
	}
}
