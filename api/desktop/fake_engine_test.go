package desktop

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/nikoejento/obhod/internal/lib/route"
	"github.com/nikoejento/obhod/internal/pc"
	"github.com/nikoejento/obhod/internal/pc/status"
)

// fakeEngine is the engine of the gateway tests. It mimics what pc.App does to
// state, last error and events, so the stream tests see realistic sequences.
type fakeEngine struct {
	mu       sync.Mutex
	state    pc.State
	snap     status.Snapshot
	routes   []pc.RouteInfo
	nets     []pc.NetworkInfo
	checking bool
	lastErr  *pc.OpError
	logs     map[string][]string
	calls    []string
	subs     map[chan pc.Event]struct{}

	ready    chan struct{}
	runErr   error
	runNow   bool // Run returns runErr at once
	checkErr error
	logsErr  error
	srv      srvState

	// connectHook runs inside Connect (state connecting) and decides its result.
	connectHook func(ctx context.Context) error
	// disconnectHook runs inside Disconnect.
	disconnectHook func(ctx context.Context) error
	// failWith maps a Connect error to the OpError the fake records, like pc.Classify.
	failWith func(err error) *pc.OpError
}

func newFakeEngine() *fakeEngine {
	f := &fakeEngine{
		state: pc.StateDisconnected,
		snap:  status.Snapshot{Version: "splitter test", Network: "unknown network"},
		logs:  map[string][]string{},
		subs:  map[chan pc.Event]struct{}{},
		ready: make(chan struct{}),
	}
	f.failWith = pc.Classify
	return f
}

func (f *fakeEngine) call(s string) {
	f.mu.Lock()
	f.calls = append(f.calls, s)
	f.mu.Unlock()
}

func (f *fakeEngine) called() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.calls...)
}

func (f *fakeEngine) callCount(prefix string) int {
	n := 0
	for _, c := range f.called() {
		if strings.HasPrefix(c, prefix) {
			n++
		}
	}
	return n
}

func (f *fakeEngine) makeReady() { close(f.ready) }

func (f *fakeEngine) Run(ctx context.Context) error {
	f.call("Run")
	if f.runNow {
		return f.runErr
	}
	<-ctx.Done()
	return f.runErr
}

func (f *fakeEngine) Ready() <-chan struct{} { return f.ready }

func (f *fakeEngine) setState(s pc.State) {
	f.mu.Lock()
	f.state = s
	f.mu.Unlock()
	f.publish(pc.Event{Kind: pc.EventState})
}

func (f *fakeEngine) Connect(ctx context.Context) error {
	f.call("Connect")
	f.mu.Lock()
	f.lastErr = nil
	f.mu.Unlock()
	f.setState(pc.StateConnecting)
	var err error
	if f.connectHook != nil {
		err = f.connectHook(ctx)
	}
	if err != nil {
		if ctx.Err() == nil {
			f.mu.Lock()
			f.lastErr = f.failWith(err)
			f.mu.Unlock()
		}
		f.setState(pc.StateDisconnected)
		return err
	}
	f.setState(pc.StateConnected)
	return nil
}

func (f *fakeEngine) Disconnect(ctx context.Context) error {
	f.call("Disconnect")
	f.setState(pc.StateDisconnecting)
	var err error
	if f.disconnectHook != nil {
		err = f.disconnectHook(ctx)
	}
	f.setState(pc.StateDisconnected)
	return err
}

func (f *fakeEngine) State() pc.State {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.state
}

func (f *fakeEngine) Snapshot() status.Snapshot {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.snap
}

func (f *fakeEngine) Routes() []pc.RouteInfo {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.routes
}

func (f *fakeEngine) Networks() []pc.NetworkInfo {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.nets
}

func (f *fakeEngine) Checking() bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.checking
}

func (f *fakeEngine) CheckNow(domains ...string) error {
	f.call("CheckNow " + strings.Join(domains, ","))
	if f.checkErr != nil {
		return f.checkErr
	}
	known := false
	if len(domains) == 0 {
		known = true
	}
	f.mu.Lock()
	for _, d := range domains {
		for _, r := range f.routes {
			if r.Domain == d {
				known = true
			}
		}
	}
	f.mu.Unlock()
	if !known {
		return pc.ErrUnknownDomain
	}
	return nil
}

func (f *fakeEngine) LastError() *pc.OpError {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.lastErr
}

func (f *fakeEngine) Logs(name string) ([]string, error) {
	f.call("Logs " + name)
	if f.logsErr != nil {
		return nil, f.logsErr
	}
	switch name {
	case "splitter", "sing-box", "desync":
		f.mu.Lock()
		defer f.mu.Unlock()
		return f.logs[name], nil
	}
	return nil, fmt.Errorf("%w: %q", pc.ErrUnknownLog, name)
}

// Subscribe mimics pc: a buffer of 64, and a subscriber that falls behind is
// dropped and its channel closed. It never blocks the publisher.
func (f *fakeEngine) Subscribe() (<-chan pc.Event, func()) {
	ch := make(chan pc.Event, 64)
	f.mu.Lock()
	f.subs[ch] = struct{}{}
	f.mu.Unlock()
	return ch, func() {
		f.mu.Lock()
		defer f.mu.Unlock()
		if _, ok := f.subs[ch]; ok {
			delete(f.subs, ch)
			close(ch)
		}
	}
}

func (f *fakeEngine) subscribers() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.subs)
}

func (f *fakeEngine) publish(ev pc.Event) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for ch := range f.subs {
		select {
		case ch <- ev:
		default:
			delete(f.subs, ch)
			close(ch)
		}
	}
}

// ---- fixtures ----

var (
	t0 = time.Date(2026, 10, 31, 12, 0, 0, 0, time.UTC)
	t1 = t0.Add(5 * time.Minute)
)

// fillFixtures gives the fake a realistic table, snapshot and networks.
func (f *fakeEngine) fillFixtures() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.routes = []pc.RouteInfo{
		{Domain: "example.org", Route: route.Direct, Source: route.SourceChecked, Since: t0, LastCheck: t1},
		{Domain: "video.example", Route: route.Desync, Source: route.SourceChecked, Since: t1, LastCheck: t1,
			History: []status.Change{{Domain: "video.example", From: route.Tunnel, To: route.Desync, At: t1}}},
		{Domain: "vpn-only.example", Route: route.Tunnel, Source: route.SourceStatic, Since: t0, LastCheck: t1},
	}
	f.nets = []pc.NetworkInfo{{Label: "AS64500 Example", Since: t0}}
	f.snap = status.Snapshot{
		Version:   "splitter test",
		Network:   "AS64500 Example",
		LastCheck: t1,
		NextCheck: t1.Add(time.Hour),
		Routes:    map[route.Route]int{route.Direct: 1, route.Desync: 1, route.Tunnel: 1},
		Traffic:   map[route.Route]int64{route.Direct: 3000, route.Tunnel: 1000},
		Savings:   0.75,
		Processes: []status.Process{{Name: "sing-box", State: "running"}, {Name: "winws2", State: "running", Restarts: 1}},
		Warnings:  []string{"sing-box's Clash API is not answering"},
		Recent:    []status.Change{{Domain: "video.example", From: route.Tunnel, To: route.Desync, At: t1}},
	}
}

var errBoom = errors.New("boom")

// ---- server management ----

// userErr is an error of the kind the servers module returns: a kind for
// errors.Is and one fixed sentence.
type userErr struct {
	kind error
	msg  string
}

func (e *userErr) Error() string       { return e.msg }
func (e *userErr) Is(t error) bool     { return t == e.kind }
func (e *userErr) UserMessage() string { return e.msg }

// srvState is the fake's server list. The rules mirror pc: ErrBusy while
// connecting or disconnecting, the active server cannot go while connected.
type srvState struct {
	list   []pc.Server
	next   int
	lastIn string // the last input passed to AddServer or TestServer (for the leak tests)

	addFn  func(input, name string) (pc.Server, error) // replaces the default Add
	testFn func(id, input string) (string, error)      // replaces the default Test
	err    map[string]error                            // method name -> error returned first
}

func (f *fakeEngine) setServers(list ...pc.Server) {
	f.mu.Lock()
	f.srv.list = list
	f.mu.Unlock()
}

func (f *fakeEngine) failServerCall(method string, err error) {
	f.mu.Lock()
	if f.srv.err == nil {
		f.srv.err = map[string]error{}
	}
	f.srv.err[method] = err
	f.mu.Unlock()
}

func (f *fakeEngine) srvErr(method string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.srv.err[method]
}

func fakeServer(i int, active bool) pc.Server {
	return pc.Server{
		ID: fmt.Sprintf("%016x", i), Name: fmt.Sprintf("Server %d", i), Protocol: "vless", Host: fmt.Sprintf("192.0.2.%d", i), Port: 443,
		Engine: "singbox", AddedAt: t0.Add(time.Duration(i) * time.Minute), Active: active,
	}
}

func (f *fakeEngine) Servers() ([]pc.Server, error) {
	f.call("Servers")
	if err := f.srvErr("Servers"); err != nil {
		return nil, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]pc.Server{}, f.srv.list...), nil
}

func (f *fakeEngine) AddServer(_ context.Context, input, name string) (pc.Server, error) {
	f.call("AddServer name=" + name)
	f.mu.Lock()
	f.srv.lastIn = input
	add := f.srv.addFn
	f.mu.Unlock()
	if err := f.srvErr("AddServer"); err != nil {
		return pc.Server{}, err
	}
	if add != nil {
		return add(input, name)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.srv.list) >= 20 {
		return pc.Server{}, &userErr{pc.ErrLimit, "The list is full (20 servers). Remove one first."}
	}
	f.srv.next++
	s := fakeServer(f.srv.next, false)
	if name != "" {
		s.Name = name
	}
	f.srv.list = append(f.srv.list, s)
	return s, nil
}

func (f *fakeEngine) find(id string) int {
	for i, s := range f.srv.list {
		if s.ID == id {
			return i
		}
	}
	return -1
}

func (f *fakeEngine) ActivateServer(_ context.Context, id string) (bool, error) {
	f.call("ActivateServer " + id)
	if err := f.srvErr("ActivateServer"); err != nil {
		return false, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	i := f.find(id)
	switch {
	case i < 0:
		return false, pc.ErrServerNotFound
	case f.state == pc.StateConnecting || f.state == pc.StateDisconnecting:
		return false, pc.ErrBusy
	case f.srv.list[i].Active:
		return false, nil
	}
	for j := range f.srv.list {
		f.srv.list[j].Active = j == i
	}
	return f.state == pc.StateConnected, nil
}

func (f *fakeEngine) DeleteServer(_ context.Context, id string) error {
	f.call("DeleteServer " + id)
	if err := f.srvErr("DeleteServer"); err != nil {
		return err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	i := f.find(id)
	switch {
	case i < 0:
		return pc.ErrServerNotFound
	case f.state == pc.StateConnecting || f.state == pc.StateDisconnecting,
		f.state == pc.StateConnected && f.srv.list[i].Active:
		return pc.ErrBusy
	}
	f.srv.list = append(f.srv.list[:i:i], f.srv.list[i+1:]...)
	return nil
}

func (f *fakeEngine) TestServer(_ context.Context, id, input string) (string, error) {
	f.call("TestServer id=" + id)
	f.mu.Lock()
	f.srv.lastIn = input
	test := f.srv.testFn
	f.mu.Unlock()
	if err := f.srvErr("TestServer"); err != nil {
		return "", err
	}
	if test != nil {
		return test(id, input)
	}
	return "vless", nil
}
