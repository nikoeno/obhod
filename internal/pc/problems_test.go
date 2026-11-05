package pc

import (
	"context"
	"errors"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/nikoejento/obhod/internal/lib/route"
	"github.com/nikoejento/obhod/internal/pc/procman"
	"github.com/nikoejento/obhod/internal/pc/status"
)

func TestSanitizeLine(t *testing.T) {
	long := strings.Repeat("abc ", 100)
	tests := []struct {
		name, in, want string
	}{
		{"plain", "exit status 87", "exit status 87"},
		{"windows path", `open C:\Users\nikoe\AppData\Local\obhod\run\winws2.exe: access denied`, `open winws2.exe: access denied`},
		{"windows path with spaces", `start C:\Program Files\Obhod\bin\winws2.exe failed`, `start winws2.exe failed`},
		{"windows slash path", `C:/Users/x/lists/hosts.txt missing`, `hosts.txt missing`},
		{"unc path", `\\server\share\dir\winws2.exe`, `winws2.exe`},
		{"unix path", `cannot open /home/user/.local/share/obhod/run/nfqws2.conf now`, `cannot open nfqws2.conf now`},
		{"unix path in quotes", `--hostlist="/var/lib/obhod/hosts.txt"`, `--hostlist="hosts.txt"`},
		{"short unix word", `use a/b and c/d`, `use a/b and c/d`},
		{"url scheme untouched", `see http://x`, `see http://x`},
		{"ipv4", `connect to 203.0.113.7 failed`, `connect to <addr> failed`},
		{"ipv4 port", `dial tcp 203.0.113.7:443: timeout`, `dial tcp <addr>: timeout`},
		{"ipv4 cidr", `rule 10.0.0.0/8`, `rule <addr>/8`},
		{"ipv6", `dial 2001:db8::1 failed`, `dial <addr> failed`},
		{"ipv6 full", `from 2001:0db8:0000:0000:0000:0000:0000:0001 now`, `from <addr> now`},
		{"ipv6 bracket port", `dial tcp [2001:db8::1]:443: refused`, `dial tcp <addr>: refused`},
		{"ipv6 loopback", `listen [::1]:9090`, `listen <addr>`},
		{"ipv4 mapped", `peer ::ffff:192.0.2.1 gone`, `peer <addr> gone`},
		{"time stays", `at 12:05:00 something`, `at 12:05:00 something`},
		{"host", `lookup vpn.example.org: no such host`, `lookup <host>: no such host`},
		{"host port", `dial vpn.example.org:8443 refused`, `dial <host> refused`},
		{"localhost port", `clash at localhost:9090`, `clash at <addr>`},
		{"file name stays", `winws2.exe exited, see sing-box.json`, `winws2.exe exited, see sing-box.json`},
		{"version stays", `winws2 v1.2.3`, `winws2 v1.2.3`},
		{"uuid", `uuid 123e4567-e89b-12d3-a456-426614174000 bad`, `uuid <secret> bad`},
		{"long token", `key=AbCdEf0123456789AbCdEf0123456789xyz`, `key=<secret>`},
		{"ansi", "\x1b[31mERROR\x1b[0m bad", `ERROR bad`},
		{"control chars", "a\x00b\x07c\rd\te", `abcd e`},
		{"invalid utf8", "ab\xffcd", `abcd`},
		{"spaces trimmed", "   x   y  ", `x y`},
		{"long line", long, strings.TrimSpace(long)[:199] + "…"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := sanitizeLine(tc.in, nil)
			if got != tc.want {
				t.Errorf("sanitizeLine(%q)\n got %q\nwant %q", tc.in, got, tc.want)
			}
		})
	}
}

func TestSanitizeLineSecrets(t *testing.T) {
	got := sanitizeLine("clash secret=hunter2-secret rejected; hunter2-secret again", []string{"hunter2-secret", "ab"})
	if strings.Contains(got, "hunter2") || !strings.Contains(got, "<secret>") {
		t.Fatalf("secret leaked: %q", got)
	}
	if got := sanitizeLine("ab stays", []string{"ab"}); got != "ab stays" {
		t.Fatalf("short secret replaced: %q", got)
	}
}

func TestBuildDetail(t *testing.T) {
	t.Run("cause and output", func(t *testing.T) {
		got := buildDetail("gave up after 5 restarts: exit status 87", []string{"first", "", "second 203.0.113.7"})
		want := "Exit: gave up after 5 restarts: exit status 87\nOutput:\nfirst\nsecond <addr>"
		if got != want {
			t.Fatalf("got %q want %q", got, want)
		}
	})
	t.Run("no output", func(t *testing.T) {
		if got := buildDetail("boom", nil); got != "Exit: boom" {
			t.Fatalf("got %q", got)
		}
	})
	t.Run("nothing", func(t *testing.T) {
		if got := buildDetail("", nil); got != "" {
			t.Fatalf("got %q", got)
		}
	})
	t.Run("size cap keeps the newest lines", func(t *testing.T) {
		var out []string
		for i := 0; i < 200; i++ {
			out = append(out, strings.Repeat("x", 150)+" line "+string(rune('A'+i%26)))
		}
		out = append(out, "THE LAST LINE")
		got := buildDetail("exit status 1", out)
		if len(got) > maxProblemDetail {
			t.Fatalf("len = %d", len(got))
		}
		if !strings.HasSuffix(got, "THE LAST LINE") || !strings.HasPrefix(got, "Exit: exit status 1\nOutput:\n") {
			t.Fatalf("got %q", got)
		}
	})
	t.Run("hard cap on a huge cause stays on a rune boundary", func(t *testing.T) {
		got := buildDetail(strings.Repeat("é", 5000), nil)
		if len(got) > maxProblemDetail || !strings.HasPrefix(got, "Exit: é") {
			t.Fatalf("len = %d", len(got))
		}
		for _, r := range got {
			if r == '\uFFFD' {
				t.Fatal("cut inside a rune")
			}
		}
	})
	t.Run("at most procLogTail lines", func(t *testing.T) {
		var out []string
		for i := 0; i < 50; i++ {
			out = append(out, "l"+string(rune('a'+i%26)))
		}
		got := buildDetail("", out)
		if n := strings.Count(got, "\n"); n != procLogTail { // "Output:" plus procLogTail lines
			t.Fatalf("newlines = %d", n)
		}
	})
}

var leak = regexp.MustCompile(`203\.0\.113\.\d+|2001:db8|[A-Za-z]:\\|/home/|/Users/|test-secret|vpn\.example`)

func mustNotLeak(t *testing.T, ps []status.Problem) {
	t.Helper()
	for _, p := range ps {
		for _, s := range []string{p.Message, p.Detail} {
			if m := leak.FindString(s); m != "" {
				t.Errorf("problem %s leaks %q in %q", p.Code, m, s)
			}
		}
		if len(p.Detail) > maxProblemDetail {
			t.Errorf("detail is %d bytes", len(p.Detail))
		}
	}
}

func problemCodes(ps []status.Problem) []string {
	out := []string{}
	for _, p := range ps {
		out = append(out, p.Code)
	}
	return out
}

func waitProblems(t *testing.T, h *harness, n int) []status.Problem {
	t.Helper()
	waitUntil(t, "problems", func() bool { return len(h.app.Problems()) == n })
	return h.app.Problems()
}

// setLogs scripts the output the fake process manager hands out for name.
func (f *fakeProcs) setLogs(name string, lines ...string) {
	f.mu.Lock()
	f.logs[name] = lines
	f.mu.Unlock()
}

func failEvent(f *fakeProcs, name, cause string) {
	f.mu.Lock()
	delete(f.running, name)
	f.mu.Unlock()
	f.events <- procman.Event{Name: name, State: procman.StateFailed, Err: errors.New(cause)}
}

func TestEngineGiveUpIsAProblemNotADisconnect(t *testing.T) {
	h := newHarness(t, baseTable(t))
	h.start()
	h.waitRunning()
	h.fc.Advance(time.Minute)
	if got := h.app.Problems(); got == nil || len(got) != 0 {
		t.Fatalf("Problems = %#v, want empty and non-nil", got)
	}
	if got := h.app.Snapshot().Problems; got == nil || len(got) != 0 {
		t.Fatalf("Snapshot.Problems = %#v, want empty and non-nil", got)
	}

	evs, unsub := h.app.Subscribe()
	defer unsub()

	h.procs.setLogs("winws2",
		`2026-11-01 winws2: open C:\Users\nikoe\AppData\Local\obhod\run\winws2.exe: access denied`,
		`WinDivert: filter error at 203.0.113.7:443 and 2001:db8::1 token test-secret`,
		"\x1b[31mexit status 87\x1b[0m")
	failEvent(h.procs, "winws2", "gave up after 5 restarts: exit status 87")

	ps := waitProblems(t, h, 1)
	p := ps[0]
	if p.Code != status.ProblemDesyncFailed || p.Message != msgDesyncFailed {
		t.Fatalf("problem = %+v", p)
	}
	for _, want := range []string{"Exit: gave up after 5 restarts: exit status 87", "winws2.exe: access denied", "filter error at <addr>", "<secret>"} {
		if !strings.Contains(p.Detail, want) {
			t.Errorf("detail lacks %q:\n%s", want, p.Detail)
		}
	}
	mustNotLeak(t, ps)
	if h.app.State() != StateConnected {
		t.Fatalf("state = %s, the desync failure must not end the connection", h.app.State())
	}
	if h.app.LastError() != nil {
		t.Fatalf("LastError = %+v", h.app.LastError())
	}
	if got := h.app.Snapshot().Problems; len(got) != 1 || got[0] != p {
		t.Fatalf("Snapshot.Problems = %+v", got)
	}
	sawState := false
	for !sawState {
		select {
		case ev := <-evs:
			sawState = ev.Kind == EventState
		case <-time.After(5 * time.Second):
			t.Fatal("no state event for the new problem")
		}
	}

	// A second failure replaces the entry of the same code.
	h.procs.setLogs("winws2", "second failure output")
	failEvent(h.procs, "winws2", "gave up after 5 restarts: exit status 1")
	waitUntil(t, "replaced", func() bool {
		ps := h.app.Problems()
		return len(ps) == 1 && strings.Contains(ps[0].Detail, "second failure output")
	})
	if got := problemCodes(h.app.Problems()); len(got) != 1 {
		t.Fatalf("codes = %v", got)
	}

	// The engine is started again by the next reload: the problem is gone.
	h.log.clear()
	h.publishAndFire(tbl(t, ent("yt.example", route.Desync), ent("blocked.example", route.Direct)))
	h.waitOp("start winws2")
	waitProblems(t, h, 0)
	if h.app.State() != StateConnected {
		t.Fatalf("state = %s", h.app.State())
	}
}

func TestEngineStartErrorInReloadIsAProblem(t *testing.T) {
	h := newHarness(t, baseTable(t))
	h.start()
	h.waitRunning()
	h.fc.Advance(time.Minute)
	failEvent(h.procs, "winws2", "gave up after 5 restarts: exit status 87")
	waitProblems(t, h, 1)

	h.procs.mu.Lock()
	h.procs.startErr["winws2"] = errors.New(`fork/exec C:\Users\nikoe\bin\winws2.exe: Access denied (peer 203.0.113.7:443, vpn.example.org)`)
	h.procs.mu.Unlock()
	h.publishAndFire(tbl(t, ent("yt.example", route.Desync), ent("blocked.example", route.Direct)))
	h.waitOp("start-failed winws2")
	waitUntil(t, "problem with the start error", func() bool {
		ps := h.app.Problems()
		return len(ps) == 1 && strings.Contains(ps[0].Detail, "Access denied")
	})
	ps := h.app.Problems()
	if !strings.Contains(ps[0].Detail, "winws2.exe") {
		t.Errorf("detail = %q", ps[0].Detail)
	}
	mustNotLeak(t, ps)
	if h.app.State() != StateConnected {
		t.Fatalf("state = %s", h.app.State())
	}

	// Once the start works, the problem is cleared.
	h.procs.mu.Lock()
	delete(h.procs.startErr, "winws2")
	h.procs.mu.Unlock()
	h.publishAndFire(tbl(t, ent("yt.example", route.Desync), ent("other.example", route.Desync)))
	waitProblems(t, h, 0)
}

func TestEnginePlanErrorInReloadIsAProblem(t *testing.T) {
	h := newHarness(t, tbl(t, ent("plain.example", route.Direct)))
	h.start()
	h.waitRunning()
	h.fc.Advance(time.Minute)
	if len(h.app.Problems()) != 0 {
		t.Fatalf("problems = %+v", h.app.Problems())
	}
	// The engine binary is not there when a desync site appears.
	h.d.findBinary = func(name, _ string) (string, error) {
		if name == "sing-box" {
			return "/bin/sing-box", nil
		}
		return "", errors.New("not found")
	}
	h.app.mu.Lock()
	h.app.binaries.desync = ""
	h.app.mu.Unlock()
	h.publishAndFire(tbl(t, ent("yt.example", route.Desync), ent("plain.example", route.Direct)))
	ps := waitProblems(t, h, 1)
	if ps[0].Code != status.ProblemDesyncFailed || h.app.State() != StateConnected {
		t.Fatalf("problems = %+v, state %s", ps, h.app.State())
	}
	mustNotLeak(t, ps)

	// The site goes away: nothing needs the engine, the problem is cleared.
	h.publishAndFire(tbl(t, ent("plain.example", route.Direct)))
	waitProblems(t, h, 0)
}

func TestProblemsClearedOnDisconnectAndConnect(t *testing.T) {
	h := newHarness(t, baseTable(t), WithUI(true))
	h.start()
	h.waitRunning()
	ctx := context.Background()
	if err := h.app.Connect(ctx); err != nil {
		t.Fatal(err)
	}
	failEvent(h.procs, "winws2", "gave up after 5 restarts: exit status 87")
	waitProblems(t, h, 1)
	if err := h.app.Disconnect(ctx); err != nil {
		t.Fatal(err)
	}
	if got := h.app.Problems(); len(got) != 0 {
		t.Fatalf("problems after Disconnect: %+v", got)
	}
	// A stale failure event while disconnected is not recorded.
	failEvent(h.procs, "winws2", "gave up after 5 restarts: exit status 87")
	time.Sleep(100 * time.Millisecond)
	if got := h.app.Problems(); len(got) != 0 {
		t.Fatalf("stale event recorded: %+v", got)
	}
	if err := h.app.Connect(ctx); err != nil {
		t.Fatal(err)
	}
	if got := h.app.Problems(); len(got) != 0 {
		t.Fatalf("problems after Connect: %+v", got)
	}
}

func TestSingBoxLostLeavesAProblemForTheUser(t *testing.T) {
	h := newHarness(t, baseTable(t), WithUI(true))
	h.start()
	h.waitRunning()
	ctx := context.Background()
	if err := h.app.Connect(ctx); err != nil {
		t.Fatal(err)
	}
	h.procs.setLogs(nameSingBox, "FATAL start service: listen tcp 127.0.0.1:9090: bind: address already in use", `config C:\Users\x\run\sing-box.json`)
	h.procs.crash(nameSingBox)
	waitUntil(t, "disconnected", func() bool { return h.app.State() == StateDisconnected })
	ps := waitProblems(t, h, 1)
	if ps[0].Code != status.ProblemTunnelEngineFailed || ps[0].Message != msgTunnelEngineFailed {
		t.Fatalf("problems = %+v", ps)
	}
	if !strings.Contains(ps[0].Detail, "sing-box.json") || !strings.Contains(ps[0].Detail, "gave up after 5 restarts") {
		t.Errorf("detail = %q", ps[0].Detail)
	}
	mustNotLeak(t, ps)
	if le := h.app.LastError(); le == nil || le.Code != CodeCrashed {
		t.Fatalf("LastError = %+v", le)
	}
	// The next connect starts clean.
	if err := h.app.Connect(ctx); err != nil {
		t.Fatal(err)
	}
	if got := h.app.Problems(); len(got) != 0 {
		t.Fatalf("problems after Connect: %+v", got)
	}
}
