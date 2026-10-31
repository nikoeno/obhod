package pc

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"time"

	"github.com/nikoejento/obhod/internal/lib/route"
	"github.com/nikoejento/obhod/internal/pc/desync"
	"github.com/nikoejento/obhod/internal/pc/procman"
	"github.com/nikoejento/obhod/internal/pc/singbox"
	"github.com/nikoejento/obhod/internal/pc/status"
)

// The reads and actions the control API (api/desktop) needs.

// Ready is closed when the pre-flight passed and the checker runs (the state
// is Disconnected or later). If Run fails before that, it is never closed:
// watch Run's result too.
func (a *App) Ready() <-chan struct{} { return a.ready }

func (a *App) markReady() { a.readyOnce.Do(func() { close(a.ready) }) }

// Snapshot is what the status page shows. It includes the connection State.
func (a *App) Snapshot() status.Snapshot { return (&statusSource{a: a}).Snapshot() }

// RouteInfo is one entry of the route table with its per-entry data.
type RouteInfo struct {
	Domain    string
	Route     route.Route
	Source    route.Source
	Since     time.Time       // when the entry got its current route (the first table of this run for entries that never changed)
	LastCheck time.Time       // the last finished check cycle; global, not per domain
	History   []status.Change // route changes of this run, at most 10, oldest first
}

// Routes returns the current table sorted by domain. It is empty while the
// table is empty. Hostnames are the curated lists' entries, never observed ones.
func (a *App) Routes() []RouteInfo {
	last := a.d.checker.Status().LastCheck
	a.mu.Lock()
	defer a.mu.Unlock()
	out := make([]RouteInfo, 0, len(a.table.Entries))
	for _, e := range a.table.Entries {
		out = append(out, RouteInfo{
			Domain: e.Domain, Route: e.Route, Source: e.Source,
			Since: a.since[e.Domain], LastCheck: last,
			History: append([]status.Change(nil), a.history[e.Domain]...),
		})
	}
	return out
}

// NetworkInfo is a network seen since the app started.
type NetworkInfo struct {
	Label string // as Snapshot.Network, never the public IP
	Since time.Time
}

// Networks lists the identified networks seen this run, newest last, at most 10.
func (a *App) Networks() []NetworkInfo {
	st := a.d.checker.Status()
	a.noteNetwork(st.Network.Known, st.Network.ID, networkLabel(st.Network))
	a.mu.Lock()
	defer a.mu.Unlock()
	return append([]NetworkInfo(nil), a.networks...)
}

// noteNetwork records a network when it differs from the last identified one.
func (a *App) noteNetwork(known bool, id, label string) {
	if !known {
		return
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.netID == id && len(a.networks) > 0 {
		return
	}
	a.netID = id
	a.networks = append(a.networks, NetworkInfo{Label: label, Since: a.d.clk.Now()})
	if n := len(a.networks); n > maxNetworks {
		a.networks = append([]NetworkInfo(nil), a.networks[n-maxNetworks:]...)
	}
}

// Checking reports whether the checker is running a check.
func (a *App) Checking() bool { return a.d.checker.Status().Running }

// ErrUnknownDomain is returned by CheckNow when none of the domains is in the route table.
var ErrUnknownDomain = errors.New("pc: none of the domains is in the route table")

// CheckNow asks the checker for an immediate check. domains are normalized
// with route.Normalize (an invalid name is an error); names that are not in
// the table are ignored, and if none is, it returns ErrUnknownDomain. Without
// domains all candidates are checked. It returns at once; progress is on the
// event stream.
func (a *App) CheckNow(domains ...string) error {
	var named []string
	for _, d := range domains {
		n, err := route.Normalize(d)
		if err != nil {
			return err
		}
		named = append(named, n)
	}
	var known []string
	if len(named) > 0 {
		inTable := map[string]bool{}
		for _, e := range a.currentTable().Entries {
			inTable[e.Domain] = true
		}
		for _, n := range named {
			if inTable[n] {
				known = append(known, n)
			}
		}
		if len(known) == 0 {
			return ErrUnknownDomain
		}
	}
	a.d.checker.CheckNow(known...)
	a.noteCheckRequested(named)
	return nil
}

// OpError describes a failed or interrupted transition for the UI.
type OpError struct {
	Code     string   // permission_denied | needs_setup | tunnel_missing | crashed | connect_failed
	Message  string   // a fixed sentence: no addresses, paths or secrets
	Commands []string // needs_setup only: what the human has to run; the app never runs them
}

// The OpError codes.
const (
	CodePermissionDenied = "permission_denied"
	CodeNeedsSetup       = "needs_setup"
	CodeTunnelMissing    = "tunnel_missing"
	CodeCrashed          = "crashed"
	CodeConnectFailed    = "connect_failed"
)

// LastError returns the error of the last failed or interrupted transition,
// or nil. It is set when a connect fails or the connection drops, and cleared
// when the next Connect starts. A connect cancelled by the caller is no error.
func (a *App) LastError() *OpError {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.lastErr == nil {
		return nil
	}
	e := *a.lastErr
	e.Commands = append([]string(nil), e.Commands...)
	return &e
}

// setLastError stores e; publish announces it as a state event.
func (a *App) setLastError(e *OpError, publish bool) {
	a.mu.Lock()
	a.lastErr = e
	a.mu.Unlock()
	if publish {
		a.publish(Event{Kind: EventState})
	}
}

// Classify maps an error of Connect to an OpError. The message is a fixed
// sentence; the error text itself (it may name paths or addresses) goes only
// to the log.
func Classify(err error) *OpError {
	var ee *exitError
	switch {
	case err == nil:
		return nil
	case errors.As(err, &ee) && len(ee.commands) > 0:
		return &OpError{Code: CodeNeedsSetup, Message: "One-time setup is needed on this computer.", Commands: append([]string(nil), ee.commands...)}
	case errors.Is(err, procman.ErrNeedsAdmin), errors.As(err, &ee) && ee.code == ExitManual:
		return &OpError{Code: CodePermissionDenied, Message: "The app needs permission to change how traffic is routed."}
	case errors.Is(err, errTunnelUnresolved), errors.Is(err, singbox.ErrNoTunnel):
		return &OpError{Code: CodeTunnelMissing, Message: "The detour server is not set up or cannot be reached."}
	}
	return &OpError{Code: CodeConnectFailed, Message: "Could not connect. See the engine log for details."}
}

func crashedError() *OpError {
	return &OpError{Code: CodeCrashed, Message: "The connection stopped unexpectedly."}
}

// Log names accepted by Logs.
const (
	LogSplitter = "splitter"
	LogSingBox  = "sing-box"
	LogDesync   = "desync"
)

// ErrUnknownLog is returned by Logs for a name other than splitter, sing-box or desync.
var ErrUnknownLog = errors.New("pc: unknown log name")

// Logs returns the last lines (at most 200) of one log: "splitter" (the
// app's own lines, the same text as the log file; needs WithLogRing),
// "sing-box" or "desync" (the engine's output, from the process ring buffers).
// The slice is never nil. Other names return ErrUnknownLog.
func (a *App) Logs(name string) ([]string, error) {
	var lines []string
	switch name {
	case LogSplitter:
		if a.ring != nil {
			lines = a.ring.Lines()
		}
	case LogSingBox:
		lines = a.d.procs.Logs(nameSingBox)
	case LogDesync:
		for _, n := range []string{string(desync.WinWS), string(desync.NFQWS)} {
			if l := a.d.procs.Logs(n); len(l) > 0 {
				lines = l
				break
			}
		}
	default:
		return nil, fmt.Errorf("%w: %q", ErrUnknownLog, name)
	}
	if len(lines) > logTail {
		lines = lines[len(lines)-logTail:]
	}
	return append([]string{}, lines...), nil
}

const logTail = 200

// LogRing is an io.Writer that keeps the last N lines written to it. It is
// safe for concurrent use.
type LogRing struct {
	mu      sync.Mutex
	max     int
	lines   []string
	partial []byte
}

// NewLogRing returns a ring that keeps the last lines lines (at least 1).
func NewLogRing(lines int) *LogRing {
	if lines < 1 {
		lines = 1
	}
	return &LogRing{max: lines}
}

// Write splits p into lines (\n or \r\n) and keeps the last ones. A line
// without its newline is held until the rest arrives.
func (r *LogRing) Write(p []byte) (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.partial = append(r.partial, p...)
	for {
		i := bytes.IndexByte(r.partial, '\n')
		if i < 0 {
			break
		}
		line := strings.TrimSuffix(string(r.partial[:i]), "\r")
		r.partial = r.partial[i+1:]
		r.lines = append(r.lines, line)
		if len(r.lines) > r.max {
			r.lines = append([]string(nil), r.lines[len(r.lines)-r.max:]...)
		}
	}
	const maxPartial = 64 << 10 // a line without a newline must not grow without bound
	if len(r.partial) > maxPartial {
		r.partial = append([]byte(nil), r.partial[len(r.partial)-maxPartial:]...)
	}
	return len(p), nil
}

// Lines returns a copy of the kept lines, oldest first.
func (r *LogRing) Lines() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.lines...)
}

var _ io.Writer = (*LogRing)(nil)

// WithLogRing makes Logs("splitter") read from r. api/desktop passes
// io.MultiWriter(stderr, r) to NewLogger, so the ring holds the same lines as
// the log file.
func WithLogRing(r *LogRing) Option { return func(a *App) { a.ring = r } }
