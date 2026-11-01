package pc

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/nikoejento/obhod/internal/lib/route"
	"github.com/nikoejento/obhod/internal/pc/servers"
	"github.com/nikoejento/obhod/internal/pc/singbox"
	"github.com/nikoejento/obhod/internal/pc/status"
)

// noTunnelOutbounds makes pre-flight read the secrets directory again (the
// harness normally injects a tunnel outbound).
func noTunnelOutbounds(a *App) { a.outbounds, a.haveOutbounds = singbox.Outbounds{}, false }

func linkFor(i int, secret string) string {
	return "trojan://" + secret + "@192.0.2." + string(rune('0'+i)) + ":443#S" + string(rune('0'+i))
}

func (h *harness) tunnelFile() string { return filepath.Join(h.p.Secrets, h.cfg.Secrets.Tunnel) }

func (h *harness) readTunnel() string {
	h.t.Helper()
	b, err := os.ReadFile(h.tunnelFile())
	if err != nil {
		h.t.Fatal(err)
	}
	return string(b)
}

func (h *harness) addServer(i int) servers.Server {
	h.t.Helper()
	s, err := h.app.AddServer(context.Background(), linkFor(i, "fake-password-"+string(rune('0'+i))), "")
	if err != nil {
		h.t.Fatal(err)
	}
	return s
}

func TestUIStartsWithoutTunnelFile(t *testing.T) {
	h := newHarness(t, baseTable(t), WithUI(true), noTunnelOutbounds)
	h.start()
	h.waitRunning()
	if got := h.app.State(); got != StateDisconnected {
		t.Fatalf("state = %s", got)
	}
	if got := h.app.Snapshot().Tunnel; got != status.TunnelNone {
		t.Fatalf("Snapshot.Tunnel = %q while disconnected with no server", got)
	}
	// A table change while disconnected with no server is applied quietly.
	h.chk.publish(tbl(t, ent("a.example", route.Tunnel)))
	waitUntil(t, "debounce timer", func() bool { return h.fc.Waiters() >= 1 })
	h.fc.Advance(reloadDebounce)
	waitUntil(t, "table applied", func() bool { return len(h.app.currentTable().Entries) == 1 })
	if h.procs.started[nameSingBox] {
		t.Error("sing-box was started while disconnected")
	}
}

// Connect with no server in use runs in degraded mode: no tunnel outbound,
// tunnel-route domains direct, block still blocks, desync still runs.
func TestConnectWithoutServerIsDegraded(t *testing.T) {
	tab := tbl(t, ent("yt.example", route.Desync), ent("blocked.example", route.Tunnel),
		ent("plain.example", route.Direct), ent("bad.example", route.Block))
	h := newHarness(t, tab, WithUI(true), noTunnelOutbounds)
	h.start()
	h.waitRunning()
	if err := h.app.Connect(context.Background()); err != nil {
		t.Fatalf("Connect without a server: %v", err)
	}
	if h.app.State() != StateConnected || h.app.LastError() != nil {
		t.Fatalf("state %s, error %+v", h.app.State(), h.app.LastError())
	}
	var doc struct {
		Outbounds []map[string]any `json:"outbounds"`
		Route     struct {
			Final string           `json:"final"`
			Rules []map[string]any `json:"rules"`
		} `json:"route"`
	}
	if err := json.Unmarshal(h.singBoxFile(), &doc); err != nil {
		t.Fatal(err)
	}
	for _, o := range doc.Outbounds {
		if o["tag"] == "tunnel" {
			t.Fatalf("degraded config has a tunnel outbound: %v", o)
		}
	}
	if doc.Route.Final != "direct" {
		t.Errorf("final = %q", doc.Route.Final)
	}
	outbound := map[string]string{}
	for _, r := range doc.Route.Rules {
		ds, _ := r["domain_suffix"].([]any)
		for _, d := range ds {
			if o, ok := r["outbound"].(string); ok {
				outbound[d.(string)] = o
			} else {
				outbound[d.(string)] = fmt.Sprint(r["action"])
			}
		}
	}
	want := map[string]string{"blocked.example": "direct", "plain.example": "direct", "yt.example": "desync", "bad.example": "reject"}
	for d, w := range want {
		if outbound[d] != w {
			t.Errorf("%s -> %q, want %q", d, outbound[d], w)
		}
	}
	if _, ok := h.procs.spec(string(h.cfg.Desync.Engine)); !ok {
		t.Error("the desync engine did not start in degraded mode")
	}
	if !h.procs.started[nameSingBox] {
		t.Error("sing-box did not start")
	}
	// The status says so; the tunnel-route entry is flagged, the others are not.
	if got := h.app.Snapshot().Tunnel; got != status.TunnelNone {
		t.Errorf("Snapshot.Tunnel = %q", got)
	}
	for _, r := range h.app.Routes() {
		if r.Degraded != (r.Route == route.Tunnel) {
			t.Errorf("%s (%s): Degraded = %v", r.Domain, r.Route, r.Degraded)
		}
	}
}

// A stored server that is not in use (never activated) leaves the same degraded mode.
func TestConnectWithStoredButInactiveServerIsDegraded(t *testing.T) {
	h := newHarness(t, baseTable(t), WithUI(true), noTunnelOutbounds)
	h.start()
	h.waitRunning()
	h.addServer(1)
	if err := h.app.Connect(context.Background()); err != nil {
		t.Fatal(err)
	}
	if h.app.Snapshot().Tunnel != status.TunnelNone {
		t.Error("an inactive server counts as a tunnel")
	}
}

// Activating a server while connected without one switches to full mode through
// the normal reload path; a failed start rolls back to degraded mode.
func TestActivateWhileDegradedSwitchesToFullMode(t *testing.T) {
	h := newHarness(t, baseTable(t), WithUI(true), noTunnelOutbounds)
	h.start()
	h.waitRunning()
	ctx := context.Background()
	s := h.addServer(1)
	if err := h.app.Connect(ctx); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(h.singBoxFile()), "192.0.2.1") {
		t.Fatal("degraded config names the server")
	}
	if reloaded, err := h.app.ActivateServer(ctx, s.ID); err != nil || !reloaded {
		t.Fatalf("ActivateServer = %v, %v", reloaded, err)
	}
	waitUntil(t, "retry timer", func() bool { return h.fc.Waiters() >= 1 })
	if got := h.app.Snapshot().Tunnel; got != status.TunnelNone {
		t.Errorf("Snapshot.Tunnel = %q while the restart waits for the gap", got)
	}
	h.fc.Advance(singBoxMinGap)
	waitUntil(t, "pending cleared", func() bool { return h.app.pendingActivation() == nil })
	cfg := string(h.singBoxFile())
	if !strings.Contains(cfg, "192.0.2.1") || !strings.Contains(cfg, `"final": "tunnel"`) {
		t.Fatalf("full config not applied:\n%s", cfg)
	}
	if got := h.app.Snapshot().Tunnel; got != status.TunnelReady {
		t.Errorf("Snapshot.Tunnel = %q after the switch", got)
	}
	for _, r := range h.app.Routes() {
		if r.Degraded {
			t.Errorf("%s still flagged", r.Domain)
		}
	}
}

func TestActivateWhileDegradedRollsBackToDegraded(t *testing.T) {
	h := newHarness(t, baseTable(t), WithUI(true), noTunnelOutbounds)
	h.start()
	h.waitRunning()
	ctx := context.Background()
	s := h.addServer(1)
	if err := h.app.Connect(ctx); err != nil {
		t.Fatal(err)
	}
	h.procs.restartErr[nameSingBox] = []error{errors.New("start failed"), nil}
	if _, err := h.app.ActivateServer(ctx, s.ID); err != nil {
		t.Fatal(err)
	}
	waitUntil(t, "retry timer", func() bool { return h.fc.Waiters() >= 1 })
	h.fc.Advance(singBoxMinGap)
	waitUntil(t, "rollback", func() bool { return h.app.LastError() != nil })
	if h.app.State() != StateConnected || h.app.Snapshot().Tunnel != status.TunnelNone {
		t.Errorf("state %s, tunnel %q", h.app.State(), h.app.Snapshot().Tunnel)
	}
	if _, err := os.Stat(h.tunnelFile()); err == nil {
		t.Error("the tunnel file of the failed activation is still there")
	}
	if h.app.serverStoreActive() != "" {
		t.Errorf("active = %q", h.app.serverStoreActive())
	}
	if strings.Contains(string(h.singBoxFile()), "192.0.2.1") {
		t.Error("sing-box config is not the degraded one")
	}
}

func TestTerminalModeStillNeedsTunnelFile(t *testing.T) {
	h := newHarness(t, baseTable(t), noTunnelOutbounds)
	err := h.app.Run(context.Background())
	if err == nil || ExitCode(err) != ExitConfig {
		t.Fatalf("Run = %v (exit %d), want a config error", err, ExitCode(err))
	}
	if !strings.Contains(err.Error(), "secrets") {
		t.Errorf("error does not name the secrets: %v", err)
	}
}

func TestUIModeRejectsBrokenTunnelFile(t *testing.T) {
	h := newHarness(t, baseTable(t), WithUI(true), noTunnelOutbounds)
	if err := os.MkdirAll(h.p.Secrets, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(h.tunnelFile(), []byte("not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := h.app.Run(context.Background()); ExitCode(err) != ExitConfig {
		t.Fatalf("Run = %v, want a config error for a broken tunnel file", err)
	}
}

func TestAddActivateThenConnectUsesServer(t *testing.T) {
	h := newHarness(t, baseTable(t), WithUI(true), noTunnelOutbounds)
	h.start()
	h.waitRunning()
	ctx := context.Background()

	a := h.addServer(1)
	b := h.addServer(2)
	if _, err := os.Stat(h.tunnelFile()); err == nil {
		t.Fatal("adding a server created the tunnel file")
	}
	reloaded, err := h.app.ActivateServer(ctx, b.ID)
	if err != nil || reloaded {
		t.Fatalf("ActivateServer = %v, %v", reloaded, err)
	}
	if !strings.Contains(h.readTunnel(), "192.0.2.2") {
		t.Fatalf("tunnel file = %s", h.readTunnel())
	}
	if runtime.GOOS != "windows" {
		if fi, _ := os.Stat(h.tunnelFile()); fi.Mode().Perm() != 0o600 {
			t.Errorf("tunnel file mode %v", fi.Mode().Perm())
		}
	}
	list, _ := h.app.Servers()
	if len(list) != 2 || list[0].Active || !list[1].Active || list[0].ID != a.ID {
		t.Fatalf("Servers = %+v", list)
	}
	// Activating the active server again changes nothing.
	if reloaded, err := h.app.ActivateServer(ctx, b.ID); err != nil || reloaded {
		t.Fatalf("repeat: %v %v", reloaded, err)
	}
	if err := h.app.Connect(ctx); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(h.singBoxFile()), "192.0.2.2") {
		t.Fatalf("sing-box config lacks the activated server:\n%s", h.singBoxFile())
	}
	// The desync exclusion comes from the new address.
	if spec, ok := h.procs.spec(string(h.cfg.Desync.Engine)); ok {
		if !strings.Contains(strings.Join(spec.Args, " "), "192.0.2.2") {
			t.Errorf("engine arguments lack the new server's address: %v", spec.Args)
		}
	}
}

func TestActivateErrors(t *testing.T) {
	h := newHarness(t, baseTable(t), WithUI(true), noTunnelOutbounds)
	h.start()
	h.waitRunning()
	ctx := context.Background()
	s := h.addServer(1)
	for _, id := range []string{"ffffffffffffffff", "../x", ""} {
		if _, err := h.app.ActivateServer(ctx, id); !errors.Is(err, ErrServerNotFound) {
			t.Errorf("Activate(%q) = %v", id, err)
		}
		if err := h.app.DeleteServer(ctx, id); !errors.Is(err, ErrServerNotFound) {
			t.Errorf("Delete(%q) = %v", id, err)
		}
	}
	for _, st := range []State{StateConnecting, StateDisconnecting} {
		h.app.setState(st)
		if _, err := h.app.ActivateServer(ctx, s.ID); !errors.Is(err, ErrBusy) {
			t.Errorf("Activate while %s = %v", st, err)
		}
		if err := h.app.DeleteServer(ctx, s.ID); !errors.Is(err, ErrBusy) {
			t.Errorf("Delete while %s = %v", st, err)
		}
	}
	h.app.setState(StateDisconnected)
	// The dry run of the full config fails: nothing changes.
	h.check = func(string) error { return errors.New("rejected") }
	if _, err := h.app.ActivateServer(ctx, s.ID); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("Activate with a failing dry run = %v", err)
	}
	if _, err := os.Stat(h.tunnelFile()); err == nil {
		t.Error("the tunnel file was written though the dry run failed")
	}
	if list, _ := h.app.Servers(); list[0].Active {
		t.Error("the server became active though the dry run failed")
	}
}

func TestDeleteRules(t *testing.T) {
	h := newHarness(t, baseTable(t), WithUI(true), noTunnelOutbounds)
	h.start()
	h.waitRunning()
	ctx := context.Background()
	a, b := h.addServer(1), h.addServer(2)
	if _, err := h.app.ActivateServer(ctx, a.ID); err != nil {
		t.Fatal(err)
	}
	if err := h.app.Connect(ctx); err != nil {
		t.Fatal(err)
	}
	// Connected: the active server is busy, another one may go.
	if err := h.app.DeleteServer(ctx, a.ID); !errors.Is(err, ErrBusy) {
		t.Fatalf("delete active while connected = %v", err)
	}
	if err := h.app.DeleteServer(ctx, b.ID); err != nil {
		t.Fatalf("delete another while connected = %v", err)
	}
	if err := h.app.Disconnect(ctx); err != nil {
		t.Fatal(err)
	}
	if err := h.app.DeleteServer(ctx, a.ID); err != nil {
		t.Fatalf("delete active while disconnected = %v", err)
	}
	if _, err := os.Stat(h.tunnelFile()); err == nil {
		t.Error("the tunnel file of the deleted active server is still there")
	}
	if list, _ := h.app.Servers(); len(list) != 0 {
		t.Errorf("servers left: %+v", list)
	}
	// With no server left, Connect runs without a tunnel.
	if err := h.app.Connect(ctx); err != nil {
		t.Fatalf("Connect with no server: %v", err)
	}
	if h.app.Snapshot().Tunnel != status.TunnelNone {
		t.Error("Snapshot.Tunnel is not none")
	}
}

func TestServerCallsSerializeWithOps(t *testing.T) {
	h := newHarness(t, baseTable(t), WithUI(true), noTunnelOutbounds)
	h.start()
	h.waitRunning()
	s := h.addServer(1)

	if err := h.app.op.Lock(context.Background()); err != nil {
		t.Fatal(err)
	}
	locked := true
	defer func() {
		if locked {
			h.app.op.Unlock()
		}
	}()
	for name, call := range map[string]func(context.Context) error{
		"AddServer":      func(c context.Context) error { _, err := h.app.AddServer(c, linkFor(2, "p2"), ""); return err },
		"ActivateServer": func(c context.Context) error { _, err := h.app.ActivateServer(c, s.ID); return err },
		"DeleteServer":   func(c context.Context) error { return h.app.DeleteServer(c, s.ID) },
	} {
		ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
		if err := call(ctx); !errors.Is(err, context.DeadlineExceeded) {
			t.Errorf("%s while an operation runs = %v, want the context error", name, err)
		}
		cancel()
	}
	// Reads and the offline test do not wait.
	if _, err := h.app.Servers(); err != nil {
		t.Error(err)
	}
	if proto, err := h.app.TestServer(context.Background(), "", linkFor(3, "p3")); err != nil || proto != "trojan" {
		t.Errorf("TestServer = %q %v", proto, err)
	}
	h.app.op.Unlock()
	locked = false
	if list, _ := h.app.Servers(); len(list) != 1 {
		t.Errorf("a cancelled call changed the list: %+v", list)
	}
}

// connectedTwoServers returns a connected harness whose active server is A and a second server B.
func connectedTwoServers(t *testing.T, opts ...Option) (h *harness, a, b servers.Server) {
	t.Helper()
	h = newHarness(t, tbl(t, ent("a.example", route.Tunnel)), append([]Option{WithUI(true), noTunnelOutbounds}, opts...)...)
	h.start()
	h.waitRunning()
	a, b = h.addServer(1), h.addServer(2)
	ctx := context.Background()
	if _, err := h.app.ActivateServer(ctx, a.ID); err != nil {
		t.Fatal(err)
	}
	if err := h.app.Connect(ctx); err != nil {
		t.Fatal(err)
	}
	return h, a, b
}

func TestActivateWhileConnectedReloadsAfterTheGap(t *testing.T) {
	h, _, b := connectedTwoServers(t)
	ev, unsub := h.app.Subscribe()
	defer unsub()
	h.log.clear()

	reloaded, err := h.app.ActivateServer(context.Background(), b.ID)
	if err != nil || !reloaded {
		t.Fatalf("ActivateServer = %v, %v", reloaded, err)
	}
	if !strings.Contains(h.readTunnel(), "192.0.2.2") {
		t.Fatalf("tunnel file not replaced: %s", h.readTunnel())
	}
	// The restart is held back by the minimum gap, as every sing-box restart is.
	waitUntil(t, "retry timer", func() bool { return h.fc.Waiters() >= 1 })
	if h.log.count("restart sing-box") != 0 {
		t.Fatal("sing-box restarted inside the minimum gap")
	}
	if h.app.pendingActivation() == nil {
		t.Fatal("the activation is not pending while the restart waits")
	}
	h.fc.Advance(singBoxMinGap)
	h.waitOp("restart sing-box")
	waitUntil(t, "pending cleared", func() bool { return h.app.pendingActivation() == nil })
	if !strings.Contains(string(h.singBoxFile()), "192.0.2.2") {
		t.Fatalf("sing-box config lacks the new server:\n%s", h.singBoxFile())
	}
	if h.log.count("restart sing-box") != 1 {
		t.Errorf("want exactly one restart: %s", h.ops())
	}
	if h.app.LastError() != nil || h.app.State() != StateConnected {
		t.Errorf("state %s, error %+v", h.app.State(), h.app.LastError())
	}
	_ = ev
}

func TestActivateRollsBackWhenSingBoxDoesNotStart(t *testing.T) {
	h, a, b := connectedTwoServers(t)
	wasTunnel := h.readTunnel()
	ev, unsub := h.app.Subscribe()
	defer unsub()
	// The first restart (new config) fails; the second (the old config) works.
	h.procs.restartErr[nameSingBox] = []error{errors.New("start failed"), nil}
	if _, err := h.app.ActivateServer(context.Background(), b.ID); err != nil {
		t.Fatal(err)
	}
	waitUntil(t, "retry timer", func() bool { return h.fc.Waiters() >= 1 })
	h.fc.Advance(singBoxMinGap)
	waitUntil(t, "rollback", func() bool { return h.app.LastError() != nil })

	if got := h.readTunnel(); got != wasTunnel {
		t.Errorf("tunnel file not restored:\n%s\nwant\n%s", got, wasTunnel)
	}
	if h.app.serverStoreActive() != a.ID {
		t.Errorf("active = %q, want the previous server %q", h.app.serverStoreActive(), a.ID)
	}
	if le := h.app.LastError(); le.Code != CodeConnectFailed {
		t.Errorf("LastError = %+v", le)
	}
	if h.app.State() != StateConnected {
		t.Errorf("state = %s, want connected on the previous server", h.app.State())
	}
	if strings.Contains(string(h.singBoxFile()), "192.0.2.2") || !strings.Contains(string(h.singBoxFile()), "192.0.2.1") {
		t.Errorf("sing-box config is not the previous one:\n%s", h.singBoxFile())
	}
	if h.app.pendingActivation() != nil {
		t.Error("the activation is still pending")
	}
	// The failure reached the event stream.
	sawState := false
	for len(ev) > 0 {
		if (<-ev).Kind == EventState {
			sawState = true
		}
	}
	if !sawState {
		t.Error("no state event announced the failure")
	}
	// Activating again later works (the cache is back on A, so the B outbound is rendered afresh).
	h.procs.restartErr[nameSingBox] = nil
	if _, err := h.app.ActivateServer(context.Background(), b.ID); err != nil {
		t.Fatal(err)
	}
	waitUntil(t, "retry timer", func() bool { return h.fc.Waiters() >= 1 })
	h.fc.Advance(singBoxMinGap)
	waitUntil(t, "second activation", func() bool { return h.app.pendingActivation() == nil })
	if !strings.Contains(string(h.singBoxFile()), "192.0.2.2") {
		t.Errorf("the second activation did not apply:\n%s", h.singBoxFile())
	}
}

func TestActivateRollsBackWhenCheckRejectsTheConfig(t *testing.T) {
	h, a, b := connectedTwoServers(t)
	wasTunnel := h.readTunnel()
	// The dry run of ActivateServer passes; the reload's own check of the candidate file fails.
	h.check = func(file string) error {
		if strings.HasSuffix(file, ".new") {
			return errors.New("rejected")
		}
		return nil
	}
	if _, err := h.app.ActivateServer(context.Background(), b.ID); err != nil {
		t.Fatal(err)
	}
	waitUntil(t, "rollback", func() bool { return h.app.LastError() != nil })
	if h.readTunnel() != wasTunnel || h.app.serverStoreActive() != a.ID {
		t.Error("the previous server was not restored")
	}
	if h.log.count("restart sing-box") != 0 {
		t.Error("sing-box was restarted although the config was rejected")
	}
	if h.app.State() != StateConnected {
		t.Errorf("state = %s", h.app.State())
	}
}

func TestDisconnectWhilePendingKeepsTheNewServer(t *testing.T) {
	h, _, b := connectedTwoServers(t)
	if _, err := h.app.ActivateServer(context.Background(), b.ID); err != nil {
		t.Fatal(err)
	}
	if err := h.app.Disconnect(context.Background()); err != nil {
		t.Fatal(err)
	}
	if h.app.pendingActivation() != nil {
		t.Error("pending survived the disconnect")
	}
	if err := h.app.Connect(context.Background()); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(h.singBoxFile()), "192.0.2.2") {
		t.Errorf("the next connect did not use the activated server:\n%s", h.singBoxFile())
	}
}

func (a *App) serverStoreActive() string {
	st, err := a.serverStore()
	if err != nil {
		return ""
	}
	return st.Active()
}

// TestServerSecretsStayOutOfErrorsLogsAndIndex plants a marker in every secret
// field and follows it through add, test, activate, a rollback and delete.
func TestServerSecretsStayOutOfErrorsLogsAndIndex(t *testing.T) {
	const m = "SECRETMARKER4242"
	logs := &syncBuffer{}
	h, _, _ := connectedTwoServers(t, withLog(logs))
	ctx := context.Background()
	var seen []string
	note := func(err error) {
		if err != nil {
			seen = append(seen, err.Error())
			var um interface{ UserMessage() string }
			if errors.As(err, &um) {
				seen = append(seen, um.UserMessage())
			}
		}
	}
	ok := "vless://00000000-0000-4000-8000-000000000000@192.0.2.9:443?security=reality&sni=example.com&pbk=" + m + "&sid=" + m + "#n"
	h.check = func(file string) error { return errors.New("sing-box said no") }
	_, err := h.app.AddServer(ctx, ok, "")
	note(err)
	_, err = h.app.AddServer(ctx, "trojan://"+m+"@example.com", "")
	note(err)
	_, err = h.app.AddServer(ctx, `{"type":"trojan","server":"192.0.2.8","server_port":443,"password":"`+m+`","detour":"x"}`, "")
	note(err)
	_, err = h.app.TestServer(ctx, "", ok)
	note(err)
	h.check = func(string) error { return nil }
	s, err := h.app.AddServer(ctx, "ss://"+b64(t, "aes-256-gcm:"+m)+"@192.0.2.7:8388", "")
	if err != nil {
		t.Fatal(err)
	}
	_, err = h.app.AddServer(ctx, "ss://"+b64(t, "aes-256-gcm:"+m)+"@192.0.2.7:8388", "") // duplicate
	note(err)
	h.procs.restartErr[nameSingBox] = []error{errors.New("start failed"), nil}
	if _, err := h.app.ActivateServer(ctx, s.ID); err != nil {
		t.Fatal(err)
	}
	waitUntil(t, "retry timer", func() bool { return h.fc.Waiters() >= 1 })
	h.fc.Advance(singBoxMinGap)
	waitUntil(t, "rollback", func() bool { return h.app.LastError() != nil })
	list, _ := h.app.Servers()
	lb, _ := json.Marshal(list)
	idx, _ := os.ReadFile(filepath.Join(h.p.Secrets, "servers", "index.json"))
	le, _ := json.Marshal(h.app.LastError())

	for what, text := range map[string]string{
		"errors": strings.Join(seen, "\n"), "log": logs.String(), "Servers()": string(lb), "index.json": string(idx), "LastError": string(le),
		"log ring": strings.Join(mustLogs(t, h), "\n"),
	} {
		if strings.Contains(text, m) {
			t.Errorf("%s carries the marker:\n%s", what, text)
		}
	}
	if logs.String() == "" {
		t.Error("the test captured no log lines")
	}
}

func mustLogs(t *testing.T, h *harness) []string {
	t.Helper()
	var all []string
	for _, n := range []string{LogSplitter, LogSingBox, LogDesync} {
		l, err := h.app.Logs(n)
		if err != nil {
			t.Fatal(err)
		}
		all = append(all, l...)
	}
	return all
}

// TestServerFixturesRenderAsTunnel: every parse fixture renders through
// singbox.Generate as the tunnel outbound.
func TestServerFixturesRenderAsTunnel(t *testing.T) {
	for _, in := range fixtureFiles(t) {
		name := strings.TrimSuffix(path.Base(filepath.ToSlash(in)), ".in")
		t.Run(name, func(t *testing.T) {
			b, err := os.ReadFile(in)
			if err != nil {
				t.Fatal(err)
			}
			p, err := servers.Parse(string(b))
			if err != nil {
				t.Fatal(err)
			}
			cfg := DefaultConfig().SingBox
			cfg.ClashSecret = "test"
			table, _ := route.NewTable("n", time.Unix(0, 0), []route.Entry{{Domain: "t.example", Route: route.Tunnel, Source: route.SourceStatic}})
			out, err := singbox.Generate(cfg, singbox.Outbounds{Tunnel: p.Outbound}, table)
			if err != nil {
				t.Fatal(err)
			}
			var doc struct {
				Outbounds []map[string]any `json:"outbounds"`
			}
			if err := json.Unmarshal(out, &doc); err != nil {
				t.Fatal(err)
			}
			found := false
			for _, o := range doc.Outbounds {
				if o["tag"] == singbox.TagTunnel {
					found = o["type"] == p.Protocol && o["server"] == p.Host
				}
			}
			if !found {
				t.Errorf("no tunnel outbound for %s/%s in:\n%s", p.Protocol, p.Host, out)
			}
		})
	}
}

func fixtureFiles(t *testing.T) []string {
	t.Helper()
	files, err := filepath.Glob(filepath.Join("servers", "testdata", "*.in"))
	if err != nil || len(files) == 0 {
		t.Fatalf("no fixtures: %v", err)
	}
	return files
}

func b64(t *testing.T, s string) string {
	t.Helper()
	return base64.StdEncoding.EncodeToString([]byte(s))
}
