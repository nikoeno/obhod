package procman

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nikoejento/obhod/internal/lib/clock"
	"github.com/nikoejento/obhod/internal/lib/proc"
)

func ctxT(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	t.Cleanup(cancel)
	return ctx
}

func TestStartStop(t *testing.T) {
	m := newManager(t, Config{}, clock.Real())
	ctx := ctxT(t)
	if err := m.Start(ctx, helperSpec("a", "sleeper")); err != nil {
		t.Fatal(err)
	}
	st := statusOf(m, "a")
	if st.State != StateRunning || st.PID == 0 || st.Started.IsZero() {
		t.Fatalf("status = %+v", st)
	}
	if err := m.Start(ctx, helperSpec("a", "sleeper")); !errors.Is(err, ErrAlreadyRunning) {
		t.Fatalf("second start: %v", err)
	}
	if err := m.Stop(ctx, "a"); err != nil {
		t.Fatal(err)
	}
	if st := statusOf(m, "a"); st.State != StateStopped || st.PID != 0 {
		t.Fatalf("after stop: %+v", st)
	}
	if err := m.Stop(ctx, "a"); err != nil {
		t.Fatalf("stopping a stopped process: %v", err)
	}
	if err := m.Stop(ctx, "nope"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("unknown name: %v", err)
	}
	// A stopped process can be started again.
	if err := m.Start(ctx, helperSpec("a", "sleeper")); err != nil {
		t.Fatal(err)
	}
}

func TestStartErrors(t *testing.T) {
	m := newManager(t, Config{}, clock.Real())
	ctx := ctxT(t)
	if err := m.Start(ctx, Spec{Name: "x", Path: filepath.Join(t.TempDir(), "missing")}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing binary: %v", err)
	}
	if st := statusOf(m, "x"); st.State != StateFailed {
		t.Fatalf("status = %+v", st)
	}
	if err := m.Start(ctx, Spec{Name: "", Path: "x"}); err == nil {
		t.Fatal("empty name accepted")
	}
	if err := m.Start(ctx, Spec{Name: "y", Path: os.Args[0], Restart: "sometimes"}); err == nil {
		t.Fatal("bad restart policy accepted")
	}
}

type adminPlatform struct{ platform }

func (adminPlatform) start(*exec.Cmd) error { return errors.New("access denied") }
func (adminPlatform) needsAdmin(error) bool { return true }

func TestNeedsAdmin(t *testing.T) {
	m := newManager(t, Config{}, clock.Real())
	m.plat = adminPlatform{m.plat}
	err := m.Start(ctxT(t), helperSpec("a", "sleeper"))
	if !errors.Is(err, ErrNeedsAdmin) {
		t.Fatalf("err = %v", err)
	}
}

func TestLogsRing(t *testing.T) {
	m := newManager(t, Config{LogLines: 3}, clock.Real())
	if err := m.Start(ctxT(t), helperSpec("c", "chatty", "HELPER_N=10")); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "last line", func() bool {
		l := m.Logs("c")
		return len(l) == 3 && l[2] == "line 10"
	})
	if got := strings.Join(m.Logs("c"), "|"); got != "line 8|line 9|line 10" {
		t.Fatalf("logs = %q", got)
	}
	if m.Logs("unknown") != nil {
		t.Fatal("logs of an unknown process")
	}
}

func TestRing(t *testing.T) {
	r := newRing(2)
	_, _ = r.Write([]byte("a\r\nb\nc"))
	// The unterminated "c" is shown as the newest line; the window stays 2.
	if got := strings.Join(r.Lines(), "|"); got != "b|c" {
		t.Fatalf("lines = %q", got)
	}
	_, _ = r.Write([]byte("d\n"))
	if got := strings.Join(r.Lines(), "|"); got != "b|cd" {
		t.Fatalf("lines = %q", got)
	}
	_, _ = r.Write([]byte(strings.Repeat("x", maxPartial+1)))
	if l := r.Lines(); len(l) != 2 || len(l[1]) != maxPartial+1 {
		t.Fatal("oversized partial line was not flushed")
	}
}

func TestReadyAfterN(t *testing.T) {
	m := newManager(t, Config{}, clock.Real())
	var calls atomic.Int32
	s := helperSpec("r", "sleeper")
	s.Ready = func(context.Context) error {
		if calls.Add(1) < 3 {
			return errors.New("not yet")
		}
		return nil
	}
	if err := m.Start(ctxT(t), s); err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 3 || statusOf(m, "r").State != StateRunning {
		t.Fatalf("calls = %d, status = %+v", calls.Load(), statusOf(m, "r"))
	}
}

func TestReadyTimeoutStopsProcess(t *testing.T) {
	m := newManager(t, Config{}, clock.Real())
	s := helperSpec("r", "sleeper")
	s.Ready = func(context.Context) error { return errors.New("never") }
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	err := m.Start(ctx, s)
	if err == nil || !errors.Is(err, context.DeadlineExceeded) || !strings.Contains(err.Error(), "never") {
		t.Fatalf("err = %v", err)
	}
	if st := statusOf(m, "r"); st.State != StateStopped {
		t.Fatalf("process not stopped after readiness failure: %+v", st)
	}
}

func TestReadyProcessDies(t *testing.T) {
	m := newManager(t, Config{}, clock.Real())
	s := helperSpec("r", "crasher")
	s.Restart = RestartNever
	s.Ready = func(context.Context) error { return errors.New("down") }
	err := m.Start(ctxT(t), s)
	if err == nil || !strings.Contains(err.Error(), "exited before it was ready") {
		t.Fatalf("err = %v", err)
	}
}

// With the default policy a child that dies before it is ready must fail
// Start at once, with its exit status, instead of being restarted until the
// caller's context runs out.
func TestReadyProcessDiesDefaultPolicy(t *testing.T) {
	m := newManager(t, Config{}, clock.Real())
	s := helperSpec("r", "crasher")
	s.Ready = func(context.Context) error { return errors.New("down") }
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	start := time.Now()
	err := m.Start(ctx, s)
	if err == nil || !strings.Contains(err.Error(), "exited before it was ready") || !strings.Contains(err.Error(), "exit status 3") {
		t.Fatalf("err = %v", err)
	}
	if d := time.Since(start); d > 5*time.Second {
		t.Fatalf("Start took %v: the crash was restarted instead of reported", d)
	}
	if st := statusOf(m, "r"); st.State != StateFailed || st.Restarts != 0 {
		t.Fatalf("status = %+v", st)
	}
}

func TestRingKeepsUnterminatedLastLine(t *testing.T) {
	r := newRing(3)
	_, _ = r.Write([]byte("one\npanic: boom\r"))
	if got := strings.Join(r.Lines(), "|"); got != "one|panic: boom" {
		t.Fatalf("lines = %q", got)
	}
}

func TestRestartBackoffSchedule(t *testing.T) {
	fc := clock.NewFake(time.Unix(1_000_000, 0))
	m := newManager(t, Config{MaxRestarts: 2}, fc)
	ctx := ctxT(t)
	if err := m.Start(ctx, helperSpec("c", "crasher")); err != nil {
		t.Fatal(err)
	}

	// First crash: restart after 1s.
	if err := fc.BlockUntil(ctx, 1); err != nil {
		t.Fatal(err)
	}
	fc.Advance(999 * time.Millisecond)
	time.Sleep(100 * time.Millisecond)
	if r := statusOf(m, "c").Restarts; r != 0 {
		t.Fatalf("restarted before the 1s backoff: %d", r)
	}
	fc.Advance(time.Millisecond)
	waitFor(t, "first restart", func() bool { return statusOf(m, "c").Restarts == 1 })

	// Second crash: restart after 2s.
	if err := fc.BlockUntil(ctx, 1); err != nil {
		t.Fatal(err)
	}
	fc.Advance(time.Second)
	time.Sleep(100 * time.Millisecond)
	if r := statusOf(m, "c").Restarts; r != 1 {
		t.Fatalf("restarted before the 2s backoff: %d", r)
	}
	fc.Advance(time.Second)
	waitFor(t, "second restart", func() bool { return statusOf(m, "c").Restarts == 2 })

	// Third crash: MaxRestarts reached, the manager gives up.
	waitFor(t, "failed state", func() bool { return statusOf(m, "c").State == StateFailed })
	if st := statusOf(m, "c"); !strings.Contains(st.LastErr, "gave up") || st.Restarts != 2 {
		t.Fatalf("status = %+v", st)
	}
	if err := m.Stop(ctx, "c"); err != nil {
		t.Fatalf("stopping a failed process: %v", err)
	}
}

func TestBackoffValues(t *testing.T) {
	want := []time.Duration{1, 2, 4, 8, 16, 30, 30}
	for i, w := range want {
		if got := backoff(i); got != w*time.Second {
			t.Errorf("backoff(%d) = %v, want %v", i, got, w*time.Second)
		}
	}
}

func TestRestartNever(t *testing.T) {
	m := newManager(t, Config{}, clock.Real())
	s := helperSpec("c", "crasher")
	s.Restart = RestartNever
	if err := m.Start(ctxT(t), s); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "failed", func() bool { return statusOf(m, "c").State == StateFailed })
	if st := statusOf(m, "c"); st.Restarts != 0 || !strings.Contains(st.LastErr, "exit status 3") {
		t.Fatalf("status = %+v", st)
	}
	waitFor(t, "crash output", func() bool { return len(m.Logs("c")) == 1 })
}

func TestStopWhileBackingOff(t *testing.T) {
	fc := clock.NewFake(time.Unix(1_000_000, 0))
	m := newManager(t, Config{}, fc)
	ctx := ctxT(t)
	if err := m.Start(ctx, helperSpec("c", "crasher")); err != nil {
		t.Fatal(err)
	}
	if err := fc.BlockUntil(ctx, 1); err != nil {
		t.Fatal(err)
	}
	if err := m.Stop(ctx, "c"); err != nil {
		t.Fatal(err)
	}
	if st := statusOf(m, "c"); st.State != StateStopped {
		t.Fatalf("status = %+v", st)
	}
}

func TestStopTimeoutKills(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("graceful stop exists only on Linux; Windows terminates at once")
	}
	fc := clock.NewFake(time.Unix(1_000_000, 0))
	m := newManager(t, Config{}, fc)
	ctx := ctxT(t)
	s := helperSpec("s", "slow-stopper")
	s.StopTimeout = 5 * time.Second
	if err := m.Start(ctx, s); err != nil {
		t.Fatal(err)
	}
	// The helper must have swallowed SIGTERM before Stop sends it; otherwise the
	// default action kills it, Stop returns at once and the stop timer is gone.
	waitFor(t, "helper to handle SIGTERM", func() bool {
		for _, l := range m.Logs("s") {
			if strings.Contains(l, "HELPER ready") {
				return true
			}
		}
		return false
	})
	stopped := make(chan error, 1)
	go func() {
		stopped <- m.Stop(ctx, "s")
	}()
	if err := fc.BlockUntil(ctx, 1); err != nil { // the stop timer
		t.Fatal(err)
	}
	select {
	case err := <-stopped:
		t.Fatalf("Stop returned before the timeout: %v", err)
	case <-time.After(200 * time.Millisecond):
	}
	fc.Advance(5 * time.Second)
	if err := <-stopped; err != nil {
		t.Fatal(err)
	}
	if st := statusOf(m, "s"); st.State != StateStopped {
		t.Fatalf("status = %+v", st)
	}
}

func TestStopAllReverseOrder(t *testing.T) {
	m := newManager(t, Config{}, clock.Real())
	ctx := ctxT(t)
	for _, n := range []string{"one", "two", "three"} {
		if err := m.Start(ctx, helperSpec(n, "sleeper")); err != nil {
			t.Fatal(err)
		}
	}
	// A restarted process keeps its slot in the order.
	if err := m.Restart(ctx, "one", helperSpec("one", "sleeper")); err != nil {
		t.Fatal(err)
	}
	for len(m.events) > 0 {
		<-m.events
	}
	if err := m.StopAll(ctx); err != nil {
		t.Fatal(err)
	}
	var order []string
	for len(m.events) > 0 {
		if ev := <-m.events; ev.State == StateStopping {
			order = append(order, ev.Name)
		}
	}
	if got := strings.Join(order, ","); got != "three,two,one" {
		t.Fatalf("stop order = %s", got)
	}
	if err := m.Restart(ctx, "one", helperSpec("other", "sleeper")); err == nil {
		t.Fatal("restart accepted a spec with another name")
	}
}

func TestEventsDropOldest(t *testing.T) {
	m := newManager(t, Config{}, clock.Real())
	for i := range eventBuffer + 10 {
		m.emit("p", State(strings.Repeat("s", i%3+1)), nil)
	}
	if len(m.events) != eventBuffer {
		t.Fatalf("buffered %d events, want %d", len(m.events), eventBuffer)
	}
}

func TestStatusEventsFlow(t *testing.T) {
	m := newManager(t, Config{}, clock.Real())
	ctx := ctxT(t)
	if err := m.Start(ctx, helperSpec("a", "sleeper")); err != nil {
		t.Fatal(err)
	}
	if err := m.Stop(ctx, "a"); err != nil {
		t.Fatal(err)
	}
	var got []State
	for len(m.Events()) > 0 {
		got = append(got, (<-m.Events()).State)
	}
	want := []State{StateStarting, StateRunning, StateStopping, StateStopped}
	if len(got) != len(want) {
		t.Fatalf("events = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("events = %v, want %v", got, want)
		}
	}
}

func TestCleanupStale(t *testing.T) {
	binDir := filepath.Dir(os.Args[0])
	start := func(t *testing.T) *exec.Cmd {
		t.Helper()
		c := exec.Command(os.Args[0], "-test.run=^TestHelperProcess$")
		c.Env = append(os.Environ(), helperEnv+"=sleeper")
		if err := c.Start(); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = c.Process.Kill(); _ = c.Wait() })
		return c
	}
	stale, foreign, unreadable := start(t), start(t), start(t)

	lister := proc.NewFake(
		proc.Process{PID: stale.Process.Pid, Name: "winws2", Path: os.Args[0]},
		proc.Process{PID: foreign.Process.Pid, Name: "winws2", Path: filepath.Join(t.TempDir(), "winws2.exe")},
		proc.Process{PID: unreadable.Process.Pid, Name: "winws2"},
		proc.Process{PID: os.Getpid(), Name: "winws2", Path: os.Args[0]},
	)

	if err := New(Config{}, clock.Real(), lister).CleanupStale(context.Background(), "winws2"); err == nil {
		t.Fatal("CleanupStale without BinDir must refuse")
	}
	m := New(Config{BinDir: binDir}, clock.Real(), lister)
	if err := m.CleanupStale(context.Background(), "winws2"); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- stale.Wait() }()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("stale process still running")
	}
	for name, c := range map[string]*exec.Cmd{"foreign": foreign, "unreadable": unreadable} {
		if !pidAlive(t, c.Process.Pid) {
			t.Errorf("%s process was killed", name)
		}
	}
}

func TestIsUnder(t *testing.T) {
	dir := filepath.Join(os.TempDir(), "bin")
	for path, want := range map[string]bool{
		filepath.Join(dir, "sing-box"):          true,
		filepath.Join(dir, "sub", "sing-box"):   true,
		dir:                                     false,
		dir + "-other":                          false,
		filepath.Join(dir, "..", "sing-box"):    false,
		filepath.Join(os.TempDir(), "sing-box"): false,
	} {
		if got := isUnder(path, dir); got != want {
			t.Errorf("isUnder(%q) = %v, want %v", path, got, want)
		}
	}
	if runtime.GOOS == "windows" {
		// NTFS paths are case-insensitive: a different case is the same directory.
		if !isUnder(strings.ToUpper(filepath.Join(dir, "sing-box.exe")), dir) {
			t.Error("isUnder is case-sensitive on Windows")
		}
	}
}

// The child must die when the process that manages it is killed.
func TestKillOnParentExit(t *testing.T) {
	parent, childPID := startHelperSubprocess(t)
	if !pidAlive(t, childPID) {
		t.Fatal("child is not running")
	}
	if err := parent.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	_, _ = parent.Process.Wait()
	waitFor(t, "child to die with its parent", func() bool { return !pidAlive(t, childPID) })
}
