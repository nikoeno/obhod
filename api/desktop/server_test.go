package desktop

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nikoejento/obhod/internal/lib/route"
	"github.com/nikoejento/obhod/internal/pc"
	"github.com/nikoejento/obhod/internal/pc/status"
)

var update = flag.Bool("update", false, "rewrite the golden files in testdata")

const testToken = "tok-0123456789abcdefghijklmnopqrstuvwxyz_ABCDEF"

// lockedBuffer is a bytes.Buffer safe for the logger's goroutines.
type lockedBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (l *lockedBuffer) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.Write(p)
}
func (l *lockedBuffer) String() string { l.mu.Lock(); defer l.mu.Unlock(); return l.b.String() }

// env is a gateway on a real loopback listener with a fake engine.
type env struct {
	t      *testing.T
	eng    *fakeEngine
	srv    *server
	ts     *httptest.Server
	log    *lockedBuffer
	ctx    context.Context
	cancel context.CancelFunc
}

func newEnv(t *testing.T) *env {
	t.Helper()
	e := &env{t: t, eng: newFakeEngine(), log: &lockedBuffer{}}
	e.ctx, e.cancel = context.WithCancel(context.Background())
	logger := slog.New(slog.NewTextHandler(e.log, &slog.HandlerOptions{Level: slog.LevelDebug}))
	e.srv = newServer(e.eng, testToken, "splitter test", logger, e.ctx, e.cancel)
	e.ts = httptest.NewUnstartedServer(e.srv)
	e.ts.Config = e.srv.httpServer()
	e.ts.Start()
	e.srv.setAddr(e.ts.Listener.Addr())
	t.Cleanup(func() {
		e.cancel()
		e.ts.Close()
	})
	return e
}

type result struct {
	code int
	hdr  http.Header
	body []byte
}

func (r result) json(t *testing.T, v any) {
	t.Helper()
	if err := json.Unmarshal(r.body, v); err != nil {
		t.Fatalf("body is not JSON: %v\n%s", err, r.body)
	}
}

// errCode returns the "code" of an error body.
func (r result) errCode(t *testing.T) string {
	t.Helper()
	var b struct{ Code string }
	r.json(t, &b)
	return b.Code
}

// request builds a request with the right token unless the caller overrides Authorization.
func (e *env) request(method, path string, body io.Reader, hdr ...string) *http.Request {
	e.t.Helper()
	req, err := http.NewRequest(method, e.ts.URL+path, body)
	if err != nil {
		e.t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+testToken)
	for i := 0; i+1 < len(hdr); i += 2 {
		if hdr[i+1] == "" {
			req.Header.Del(hdr[i])
		} else {
			req.Header.Set(hdr[i], hdr[i+1])
		}
	}
	return req
}

func (e *env) do(method, path, body string, hdr ...string) result {
	e.t.Helper()
	var rd io.Reader
	if body != "" {
		rd = strings.NewReader(body)
		hdr = append([]string{"Content-Type", "application/json"}, hdr...)
	}
	return e.send(e.request(method, path, rd, hdr...))
}

func (e *env) send(req *http.Request) result {
	e.t.Helper()
	resp, err := e.ts.Client().Do(req)
	if err != nil {
		e.t.Fatalf("%s %s: %v", req.Method, req.URL.Path, err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return result{resp.StatusCode, resp.Header, b}
}

func (e *env) get(path string) result  { e.t.Helper(); return e.do(http.MethodGet, path, "") }
func (e *env) post(path string) result { e.t.Helper(); return e.do(http.MethodPost, path, "") }

// golden compares body (re-indented) with testdata/<name>.golden.json.
func golden(t *testing.T, name string, body []byte) {
	t.Helper()
	var buf bytes.Buffer
	if err := json.Indent(&buf, bytes.TrimSpace(body), "", "  "); err != nil {
		t.Fatalf("%s: not JSON: %v\n%s", name, err, body)
	}
	buf.WriteByte('\n')
	path := filepath.Join("testdata", name+".golden.json")
	if *update {
		if err := os.MkdirAll("testdata", 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, buf.Bytes(), 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("%v (run with -update to create it)", err)
	}
	want = bytes.ReplaceAll(want, []byte("\r\n"), []byte("\n"))
	if !bytes.Equal(want, buf.Bytes()) {
		t.Errorf("%s differs from the golden file:\n--- got ---\n%s--- want ---\n%s", name, buf.Bytes(), want)
	}
}

// ---- SSE client ----

type sseEvent struct{ ID, Name, Data string }

type sseClient struct {
	resp   *http.Response
	events chan sseEvent
}

// parseSSE reads an event stream. Comments arrive as Name "comment", the
// retry field as Name "retry".
func parseSSE(r io.Reader, out chan<- sseEvent) {
	defer close(out)
	sc := bufio.NewScanner(r)
	var cur sseEvent
	have := false
	for sc.Scan() {
		line := sc.Text()
		switch {
		case line == "":
			if have {
				out <- cur
			}
			cur, have = sseEvent{}, false
		case strings.HasPrefix(line, ":"):
			out <- sseEvent{Name: "comment", Data: strings.TrimSpace(line[1:])}
		case strings.HasPrefix(line, "retry: "):
			out <- sseEvent{Name: "retry", Data: line[len("retry: "):]}
		case strings.HasPrefix(line, "id: "):
			cur.ID, have = line[4:], true
		case strings.HasPrefix(line, "event: "):
			cur.Name, have = line[7:], true
		case strings.HasPrefix(line, "data: "):
			cur.Data, have = line[6:], true
		}
	}
}

func (e *env) openStream() *sseClient {
	e.t.Helper()
	resp, err := e.ts.Client().Do(e.request(http.MethodGet, "/api/v1/events", nil))
	if err != nil {
		e.t.Fatal(err)
	}
	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		e.t.Fatalf("events: %d %s", resp.StatusCode, b)
	}
	c := &sseClient{resp: resp, events: make(chan sseEvent, 256)}
	go parseSSE(resp.Body, c.events)
	e.t.Cleanup(func() { resp.Body.Close() })
	return c
}

func (c *sseClient) next(t *testing.T) sseEvent {
	t.Helper()
	select {
	case ev, ok := <-c.events:
		if !ok {
			t.Fatal("event stream ended")
		}
		return ev
	case <-time.After(5 * time.Second):
		t.Fatal("no event within 5 s")
	}
	return sseEvent{}
}

// until returns the next event named name, skipping others.
func (c *sseClient) until(t *testing.T, name string) sseEvent {
	t.Helper()
	for {
		if ev := c.next(t); ev.Name == name {
			return ev
		}
	}
}

// state returns the next state event decoded.
func (c *sseClient) state(t *testing.T) stateBody {
	t.Helper()
	var s stateBody
	if err := json.Unmarshal([]byte(c.until(t, "state").Data), &s); err != nil {
		t.Fatal(err)
	}
	return s
}

// waitState reads state events until one has the wanted state.
func (c *sseClient) waitState(t *testing.T, want string) stateBody {
	t.Helper()
	for {
		if s := c.state(t); s.State == want {
			return s
		}
	}
}

func (c *sseClient) ended(t *testing.T) {
	t.Helper()
	for {
		select {
		case _, ok := <-c.events:
			if !ok {
				return
			}
		case <-time.After(5 * time.Second):
			t.Fatal("stream did not end")
		}
	}
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(2 * time.Millisecond)
	}
}

// allPaths lists every endpoint with its method.
var allPaths = []struct{ method, path string }{
	{"GET", "/api/v1/state"}, {"GET", "/api/v1/routes"}, {"GET", "/api/v1/processes"}, {"GET", "/api/v1/logs?name=splitter"},
	{"GET", "/api/v1/version"}, {"GET", "/api/v1/events"}, {"POST", "/api/v1/connect"}, {"POST", "/api/v1/disconnect"},
	{"POST", "/api/v1/check-now"}, {"POST", "/api/v1/shutdown"},
	{"GET", "/api/v1/servers"}, {"POST", "/api/v1/servers"}, {"POST", "/api/v1/servers/test"},
	{"POST", "/api/v1/servers/0000000000000001/activate"}, {"DELETE", "/api/v1/servers/0000000000000001"},
	{"GET", "/api/v1/sites"}, {"POST", "/api/v1/sites"}, {"DELETE", "/api/v1/sites/example.com"},
}

// ---- auth, Host, headers ----

func TestTokenIsRequiredEverywhere(t *testing.T) {
	e := newEnv(t)
	targets := append([]struct{ method, path string }{}, allPaths...)
	targets = append(targets, struct{ method, path string }{"GET", "/api/v1/nope"}, struct{ method, path string }{"GET", "/"},
		struct{ method, path string }{"DELETE", "/api/v1/state"})
	bad := map[string][]string{
		"none":         {"Authorization", ""},
		"wrong":        {"Authorization", "Bearer " + testToken + "x"},
		"prefix":       {"Authorization", "Bearer " + testToken[:len(testToken)-1]},
		"empty bearer": {"Authorization", "Bearer "},
		"no scheme":    {"Authorization", testToken},
		"basic":        {"Authorization", "Basic " + testToken},
		"query token":  {"X-Token", testToken},
	}
	for _, tg := range targets {
		for name, h := range bad {
			path := tg.path
			if name == "query token" {
				h = []string{"Authorization", ""}
				path += map[bool]string{true: "&", false: "?"}[strings.Contains(path, "?")] + "token=" + testToken
			}
			r := e.do(tg.method, path, "", h...)
			if r.code != http.StatusUnauthorized || len(r.body) != 0 || r.hdr.Get("WWW-Authenticate") != "Bearer" {
				t.Errorf("%s %s (%s): %d, body %q, WWW-Authenticate %q", tg.method, tg.path, name, r.code, r.body, r.hdr.Get("WWW-Authenticate"))
			}
		}
	}
	// The right token passes (an unknown path then gets 404, which proves auth ran first).
	if r := e.get("/api/v1/version"); r.code != http.StatusOK {
		t.Fatalf("right token: %d", r.code)
	}
	if r := e.get("/api/v1/nope"); r.code != http.StatusNotFound || r.errCode(t) != "not_found" {
		t.Fatalf("unknown path: %d %s", r.code, r.body)
	}
	if e.eng.callCount("Connect")+e.eng.callCount("CheckNow") != 0 {
		t.Fatal("an unauthenticated request reached the engine")
	}
}

func TestHostCheck(t *testing.T) {
	e := newEnv(t)
	port := e.srv.port
	if port == "" {
		t.Fatal("port not set")
	}
	cases := map[string]int{
		"127.0.0.1:" + port: 200, "localhost:" + port: 200, "[::1]:" + port: 200, "127.0.0.2:" + port: 200,
		"evil.example:" + port: 403, "evil.example": 403, "127.0.0.1:1": 403, "localhost:1": 403, "127.0.0.1": 403,
		"0.0.0.0:" + port: 403, "192.0.2.1:" + port: 403, "[::1%25lo]:" + port: 403, "": 403,
	}
	for host, want := range cases {
		for _, tok := range []string{testToken, ""} {
			req := httptest.NewRequest("GET", "/api/v1/version", nil)
			req.Host = host
			if tok != "" {
				req.Header.Set("Authorization", "Bearer "+tok)
			}
			rec := httptest.NewRecorder()
			e.srv.ServeHTTP(rec, req)
			got := rec.Code
			if tok == "" && want == 200 {
				want = 401
			}
			if got != want {
				t.Errorf("Host %q token=%v: %d, want %d", host, tok != "", got, want)
			}
		}
	}
	// A foreign Host is refused before auth, with the forbidden body.
	req := httptest.NewRequest("GET", "/api/v1/state", nil)
	req.Host = "evil.example"
	rec := httptest.NewRecorder()
	e.srv.ServeHTTP(rec, req)
	if rec.Code != 403 || !strings.Contains(rec.Body.String(), `"code":"forbidden"`) {
		t.Fatalf("foreign Host: %d %s", rec.Code, rec.Body)
	}
	if !strings.Contains(e.log.String(), "unexpected Host") {
		t.Error("a rejected Host was not logged")
	}
}

func TestNoCORSAndSecurityHeaders(t *testing.T) {
	e := newEnv(t)
	e.eng.fillFixtures()
	check := func(what string, r result) {
		t.Helper()
		for k := range r.hdr {
			if strings.HasPrefix(strings.ToLower(k), "access-control-") {
				t.Errorf("%s: CORS header %s", what, k)
			}
		}
		for k, v := range map[string]string{
			"Content-Security-Policy": "default-src 'self'", "X-Content-Type-Options": "nosniff",
			"Referrer-Policy": "no-referrer", "Cache-Control": "no-store",
		} {
			if got := r.hdr.Get(k); got != v {
				t.Errorf("%s: %s = %q, want %q", what, k, got, v)
			}
		}
	}
	for _, tg := range allPaths {
		if tg.path == "/api/v1/events" || tg.path == "/api/v1/shutdown" {
			continue
		}
		check(tg.method+" "+tg.path, e.do(tg.method, tg.path, "", "Origin", "https://evil.example"))
		r := e.do("OPTIONS", tg.path, "", "Origin", "https://evil.example", "Access-Control-Request-Method", "POST")
		if r.code != http.StatusMethodNotAllowed || r.hdr.Get("Allow") == "" || r.errCode(t) != "method_not_allowed" {
			t.Errorf("OPTIONS %s: %d allow=%q", tg.path, r.code, r.hdr.Get("Allow"))
		}
		check("OPTIONS "+tg.path, r)
	}
	check("401", e.do("GET", "/api/v1/state", "", "Authorization", ""))
	check("404", e.get("/api/v1/nope"))
	check("405", e.do("PUT", "/api/v1/state", ""))
	check("400", e.do("POST", "/api/v1/connect", `{"x":1}`))
	req := httptest.NewRequest("GET", "/api/v1/state", nil)
	req.Host = "evil.example"
	rec := httptest.NewRecorder()
	e.srv.ServeHTTP(rec, req)
	check("403", result{rec.Code, rec.Header(), rec.Body.Bytes()})

	// Wrong method on a POST endpoint names POST; on a GET endpoint, GET.
	if r := e.get("/api/v1/connect"); r.code != 405 || r.hdr.Get("Allow") != "POST" {
		t.Errorf("GET connect: %d %q", r.code, r.hdr.Get("Allow"))
	}
	if r := e.post("/api/v1/state"); r.code != 405 || r.hdr.Get("Allow") != "GET" {
		t.Errorf("POST state: %d %q", r.code, r.hdr.Get("Allow"))
	}
	// The event stream carries the headers too.
	s := e.openStream()
	check("events", result{hdr: s.resp.Header})
	if ct := s.resp.Header.Get("Content-Type"); ct != "text/event-stream; charset=utf-8" {
		t.Errorf("events Content-Type = %q", ct)
	}
	if s.resp.ContentLength != -1 {
		t.Errorf("events have a Content-Length: %d", s.resp.ContentLength)
	}
}

func TestServerTimeoutsAndLimitsAreSet(t *testing.T) {
	srv := newServer(newFakeEngine(), testToken, "v", nil, context.Background(), func() {}).httpServer()
	if srv.ReadHeaderTimeout != 5*time.Second || srv.ReadTimeout != 10*time.Second || srv.WriteTimeout != 15*time.Second ||
		srv.IdleTimeout != 60*time.Second || srv.MaxHeaderBytes != 16<<10 {
		t.Fatalf("server = %+v", srv)
	}
	if srv.TLSNextProto != nil {
		t.Error("HTTP/2 configured")
	}
}

// ---- limits ----

func TestBodyRules(t *testing.T) {
	e := newEnv(t)
	e.eng.fillFixtures()
	big := `{"domains":["` + strings.Repeat("a", maxBodyBytes) + `"]}`

	if r := e.do("POST", "/api/v1/check-now", big); r.code != 413 || r.errCode(t) != "too_large" {
		t.Errorf("large body: %d %s", r.code, r.body)
	}
	// Without a Content-Length the limit still applies.
	req := e.request("POST", "/api/v1/check-now", io.MultiReader(strings.NewReader(big)), "Content-Type", "application/json")
	if r := e.send(req); r.code != 413 {
		t.Errorf("chunked large body: %d %s", r.code, r.body)
	}
	exact := `{"domains":["example.org"]}` + strings.Repeat(" ", maxBodyBytes-len(`{"domains":["example.org"]}`))
	if r := e.do("POST", "/api/v1/check-now", exact); r.code != 202 {
		t.Errorf("body of exactly the limit: %d %s", r.code, r.body)
	}
	cases := []struct {
		name, body, ctype string
		code              int
	}{
		{"unknown field", `{"domains":["example.org"],"x":1}`, "application/json", 400},
		{"wrong type", `{"domains":"example.org"}`, "application/json", 400},
		{"malformed", `{"domains":`, "application/json", 400},
		{"trailing data", `{"domains":["example.org"]} {}`, "application/json", 400},
		{"text content type", `{"domains":["example.org"]}`, "text/plain", 415},
		{"no content type", `{"domains":["example.org"]}`, "-", 415},
		{"json with charset", `{"domains":["example.org"]}`, "application/json; charset=utf-8", 202},
		{"invalid domain", `{"domains":["not a domain"]}`, "application/json", 400},
		{"ip address", `{"domains":["192.0.2.1"]}`, "application/json", 400},
	}
	for _, c := range cases {
		hdr := []string{"Content-Type", c.ctype}
		if c.ctype == "-" {
			hdr = []string{"Content-Type", ""}
		}
		req := e.request("POST", "/api/v1/check-now", strings.NewReader(c.body), hdr...)
		if c.ctype == "-" {
			req.Header.Del("Content-Type")
		}
		r := e.send(req)
		if r.code != c.code {
			t.Errorf("%s: %d %s, want %d", c.name, r.code, r.body, c.code)
		} else if c.code >= 400 && r.errCode(t) != "bad_request" {
			t.Errorf("%s: code %s", c.name, r.errCode(t))
		}
	}
	// Action endpoints without parameters take no fields.
	for _, p := range []string{"connect", "disconnect", "shutdown"} {
		if r := e.do("POST", "/api/v1/"+p, `{"force":true}`); r.code != 400 {
			t.Errorf("%s with a field: %d", p, r.code)
		}
	}
	// GETs take no body.
	if r := e.do("GET", "/api/v1/state", `{}`); r.code != 400 {
		t.Errorf("GET with a body: %d", r.code)
	}
	if e.ctx.Err() != nil {
		t.Fatal("a rejected shutdown request ended the app")
	}
}

func TestStreamLimit(t *testing.T) {
	e := newEnv(t)
	var open []*sseClient
	for i := 0; i < maxStreams; i++ {
		c := e.openStream()
		c.until(t, "state")
		open = append(open, c)
	}
	r := e.get("/api/v1/events")
	if r.code != 429 || r.errCode(t) != "busy" {
		t.Fatalf("5th stream: %d %s", r.code, r.body)
	}
	golden(t, "error_busy_429", r.body)
	// Closing one frees a slot.
	open[0].resp.Body.Close()
	waitFor(t, "a free stream slot", func() bool { return e.eng.subscribers() == maxStreams-1 })
	c := e.openStream()
	c.until(t, "state")
}

// ---- GET payloads ----

func TestGetGoldens(t *testing.T) {
	e := newEnv(t)

	// Empty engine: nil slices and maps appear as [] and {}.
	r := e.get("/api/v1/state")
	golden(t, "state_disconnected", r.body)
	if strings.Count(string(r.body), "null") != 1 { // only "error":null
		t.Errorf("null in the empty state: %s", r.body)
	}
	golden(t, "routes_empty", e.get("/api/v1/routes").body)
	golden(t, "processes_empty", e.get("/api/v1/processes").body)
	golden(t, "version", e.get("/api/v1/version").body)
	for _, name := range []string{"splitter", "sing-box", "desync"} {
		golden(t, "logs_empty_"+name, e.get("/api/v1/logs?name="+name).body)
	}

	e.eng.fillFixtures()
	e.eng.logs["splitter"] = []string{"time=2026-10-31T12:00:00Z level=INFO msg=state state=connected"}
	e.eng.logs["sing-box"] = []string{"INFO router started", "INFO inbound/tun started"}
	e.eng.logs["desync"] = []string{"winws2 started"}
	e.eng.setState(pc.StateConnected)
	e.eng.checking = true
	golden(t, "state_connected", e.get("/api/v1/state").body)
	e.eng.checking = false
	golden(t, "routes_filled", e.get("/api/v1/routes").body)
	golden(t, "processes", e.get("/api/v1/processes").body)
	for _, name := range []string{"splitter", "sing-box", "desync"} {
		golden(t, "logs_"+name, e.get("/api/v1/logs?name="+name).body)
	}

	// A connection state with no process ever started keeps the three entries.
	e.eng.mu.Lock()
	e.eng.snap.Processes = nil
	e.eng.mu.Unlock()
	golden(t, "processes_never_started", e.get("/api/v1/processes").body)
}

func TestStateErrorGoldens(t *testing.T) {
	e := newEnv(t)
	e.eng.fillFixtures()
	for _, op := range []*pc.OpError{
		{Code: pc.CodePermissionDenied, Message: "The app needs permission to change how traffic is routed."},
		{Code: pc.CodeNeedsSetup, Message: "One-time setup is needed on this computer.", Commands: []string{"nft add table inet splitter", "ip rule add fwmark 0x40000000/0x40000000 lookup main priority 8999"}},
		{Code: pc.CodeTunnelMissing, Message: "The detour server is not set up or cannot be reached."},
		{Code: pc.CodeCrashed, Message: "The connection stopped unexpectedly."},
		{Code: pc.CodeConnectFailed, Message: "Could not connect. See the engine log for details."},
	} {
		e.eng.mu.Lock()
		e.eng.lastErr = op
		e.eng.mu.Unlock()
		r := e.get("/api/v1/state")
		var s struct{ Error *struct{ Code string } }
		r.json(t, &s)
		if s.Error == nil || s.Error.Code != op.Code {
			t.Fatalf("%s: %s", op.Code, r.body)
		}
		if op.Code != pc.CodeNeedsSetup && strings.Contains(string(r.body), `"commands"`) {
			t.Errorf("%s: commands present", op.Code)
		}
		golden(t, "state_error_"+op.Code, r.body)
	}
}

func TestLogsNameIsValidated(t *testing.T) {
	e := newEnv(t)
	for _, q := range []string{"", "?name=", "?name=nope", "?name=../../etc/passwd", "?name=Splitter"} {
		r := e.get("/api/v1/logs" + q)
		if r.code != 400 || r.errCode(t) != "bad_request" {
			t.Errorf("logs%s: %d %s", q, r.code, r.body)
		}
	}
	golden(t, "error_bad_request", e.get("/api/v1/logs?name=nope").body)
}

func TestProcessNamesAreMapped(t *testing.T) {
	e := newEnv(t)
	e.eng.mu.Lock()
	e.eng.snap.Processes = []status.Process{{Name: "nfqws2", State: "failed", Restarts: 5}}
	e.eng.mu.Unlock()
	var ps []status.Process
	e.get("/api/v1/processes").json(t, &ps)
	if len(ps) != 3 || ps[0].Name != "splitter" || ps[0].State != "running" || ps[1] != (status.Process{Name: "sing-box", State: "stopped"}) ||
		ps[2] != (status.Process{Name: "desync", State: "failed", Restarts: 5}) {
		t.Fatalf("processes = %+v", ps)
	}
}

// ---- actions ----

func TestConnectIsAsyncAndIdempotent(t *testing.T) {
	e := newEnv(t)
	release := make(chan struct{})
	e.eng.connectHook = func(ctx context.Context) error {
		select {
		case <-release:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	s := e.openStream()
	if st := s.state(t); st.State != "disconnected" {
		t.Fatalf("first state = %s", st.State)
	}
	r := e.post("/api/v1/connect")
	if r.code != 202 || strings.TrimSpace(string(r.body)) != `{"ok":true,"state":"connecting"}` {
		t.Fatalf("connect: %d %s", r.code, r.body)
	}
	// The handler returned although Connect has not finished.
	s.waitState(t, "connecting")
	if e.eng.State() != pc.StateConnecting {
		t.Fatalf("state = %s", e.eng.State())
	}
	if r := e.post("/api/v1/connect"); r.code != 200 || !strings.Contains(string(r.body), `"state":"connecting"`) {
		t.Fatalf("connect while connecting: %d %s", r.code, r.body)
	}
	close(release)
	s.waitState(t, "connected")
	if r := e.post("/api/v1/connect"); r.code != 200 || !strings.Contains(string(r.body), `"state":"connected"`) {
		t.Fatalf("connect while connected: %d %s", r.code, r.body)
	}
	if n := e.eng.callCount("Connect"); n != 1 {
		t.Fatalf("Connect called %d times", n)
	}
	// While disconnecting: busy.
	e.eng.setState(pc.StateDisconnecting)
	r = e.post("/api/v1/connect")
	if r.code != 409 || r.errCode(t) != "busy" {
		t.Fatalf("connect while disconnecting: %d %s", r.code, r.body)
	}
	golden(t, "error_busy_409", r.body)
}

func TestDisconnect(t *testing.T) {
	e := newEnv(t)
	// Disconnected: nothing to do.
	if r := e.post("/api/v1/disconnect"); r.code != 200 || strings.TrimSpace(string(r.body)) != `{"ok":true,"state":"disconnected"}` {
		t.Fatalf("disconnect while disconnected: %d %s", r.code, r.body)
	}
	e.eng.setState(pc.StateConnected)
	s := e.openStream()
	s.until(t, "state")
	r := e.post("/api/v1/disconnect")
	if r.code != 202 || strings.TrimSpace(string(r.body)) != `{"ok":true,"state":"disconnecting"}` {
		t.Fatalf("disconnect: %d %s", r.code, r.body)
	}
	s.waitState(t, "disconnected")
	if e.eng.callCount("Disconnect") != 1 {
		t.Fatalf("calls: %v", e.eng.called())
	}
	e.eng.setState(pc.StateDisconnecting)
	if r := e.post("/api/v1/disconnect"); r.code != 200 || !strings.Contains(string(r.body), `"state":"disconnecting"`) {
		t.Fatalf("disconnect while disconnecting: %d %s", r.code, r.body)
	}
}

func TestDisconnectWhileConnectingCancelsTheConnect(t *testing.T) {
	e := newEnv(t)
	started := make(chan struct{})
	e.eng.connectHook = func(ctx context.Context) error {
		close(started)
		<-ctx.Done()
		return ctx.Err()
	}
	s := e.openStream()
	if r := e.post("/api/v1/connect"); r.code != 202 {
		t.Fatalf("connect: %d", r.code)
	}
	<-started
	if r := e.post("/api/v1/disconnect"); r.code != 202 {
		t.Fatalf("disconnect: %d %s", r.code, r.body)
	}
	// It ends in disconnected, without an error.
	waitFor(t, "disconnected", func() bool { return e.eng.callCount("Disconnect") == 1 && e.eng.State() == pc.StateDisconnected })
	for {
		st := s.state(t)
		if st.State == "disconnected" && e.eng.callCount("Disconnect") == 1 {
			if st.Error != nil {
				t.Fatalf("a cancelled connect shows an error: %+v", st.Error)
			}
			break
		}
	}
	if e.eng.LastError() != nil {
		t.Fatalf("LastError = %+v", e.eng.LastError())
	}
}

func TestConnectFailuresReachTheStream(t *testing.T) {
	for _, op := range []*pc.OpError{
		{Code: pc.CodePermissionDenied, Message: "The app needs permission to change how traffic is routed."},
		{Code: pc.CodeNeedsSetup, Message: "One-time setup is needed on this computer.", Commands: []string{"nft add table inet splitter"}},
		{Code: pc.CodeTunnelMissing, Message: "The detour server is not set up or cannot be reached."},
		{Code: pc.CodeConnectFailed, Message: "Could not connect. See the engine log for details."},
	} {
		t.Run(op.Code, func(t *testing.T) {
			e := newEnv(t)
			e.eng.failWith = func(error) *pc.OpError { return op }
			gate := make(chan struct{})
			e.eng.connectHook = func(context.Context) error { <-gate; return errBoom }
			s := e.openStream()
			s.until(t, "state")
			if r := e.post("/api/v1/connect"); r.code != 202 {
				t.Fatalf("connect: %d", r.code)
			}
			s.waitState(t, "connecting")
			close(gate)
			st := s.waitState(t, "disconnected")
			if st.Error == nil || st.Error.Code != op.Code || strings.Join(st.Error.Commands, ",") != strings.Join(op.Commands, ",") {
				t.Fatalf("error = %+v", st.Error)
			}
			// A new connect clears it.
			gate2 := make(chan struct{})
			e.eng.connectHook = func(context.Context) error { <-gate2; return nil }
			e.post("/api/v1/connect")
			if st := s.waitState(t, "connecting"); st.Error != nil {
				t.Fatalf("error not cleared: %+v", st.Error)
			}
			close(gate2)
			s.waitState(t, "connected")
		})
	}
}

func TestCheckNow(t *testing.T) {
	e := newEnv(t)
	e.eng.fillFixtures()
	r := e.do("POST", "/api/v1/check-now", `{"domains":["Example.ORG.","video.example"]}`)
	if r.code != 202 || strings.TrimSpace(string(r.body)) != `{"ok":true,"checking":true}` {
		t.Fatalf("check-now: %d %s", r.code, r.body)
	}
	if got := e.eng.called()[len(e.eng.called())-1]; got != "CheckNow example.org,video.example" {
		t.Fatalf("engine call = %q", got)
	}
	if r := e.post("/api/v1/check-now"); r.code != 202 {
		t.Fatalf("check-now without body: %d", r.code)
	}
	if got := e.eng.called()[len(e.eng.called())-1]; got != "CheckNow " {
		t.Fatalf("engine call = %q", got)
	}
	if r := e.do("POST", "/api/v1/check-now", `{"domains":[]}`); r.code != 202 || e.eng.called()[len(e.eng.called())-1] != "CheckNow " {
		t.Fatalf("empty list: %d", r.code)
	}
	r = e.do("POST", "/api/v1/check-now", `{"domains":["nope.example"]}`)
	if r.code != 404 || r.errCode(t) != "unknown_domain" {
		t.Fatalf("unknown domain: %d %s", r.code, r.body)
	}
	golden(t, "error_unknown_domain", r.body)
	var many []string
	for i := 0; i <= maxCheckDomains; i++ {
		many = append(many, fmt.Sprintf("d%d.example", i))
	}
	b, _ := json.Marshal(map[string]any{"domains": many})
	n := len(e.eng.called())
	if r := e.do("POST", "/api/v1/check-now", string(b)); r.code != 400 || len(e.eng.called()) != n {
		t.Fatalf("101 domains: %d", r.code)
	}
	if r := e.do("POST", "/api/v1/check-now", `{"domains":["bad name"]}`); r.code != 400 || len(e.eng.called()) != n {
		t.Fatalf("invalid name: %d", r.code)
	}
}

func TestShutdownCancelsTheRunContext(t *testing.T) {
	e := newEnv(t)
	e.eng.fillFixtures()
	r := e.post("/api/v1/shutdown")
	if r.code != 200 || strings.TrimSpace(string(r.body)) != `{"ok":true}` {
		t.Fatalf("shutdown: %d %s", r.code, r.body)
	}
	select {
	case <-e.ctx.Done():
	case <-time.After(5 * time.Second):
		t.Fatal("the run context was not cancelled")
	}
	waitFor(t, "shutting down", e.srv.shuttingDown)
	// Everything else now says the engine is unreachable; shutdown still answers 200.
	r = e.get("/api/v1/state")
	if r.code != 503 || r.errCode(t) != "engine_unreachable" {
		t.Fatalf("state after shutdown: %d %s", r.code, r.body)
	}
	golden(t, "error_engine_unreachable", r.body)
	if r := e.post("/api/v1/connect"); r.code != 503 {
		t.Fatalf("connect after shutdown: %d", r.code)
	}
	if r := e.post("/api/v1/shutdown"); r.code != 200 {
		t.Fatalf("second shutdown: %d", r.code)
	}
	if r := e.do("GET", "/api/v1/state", "", "Authorization", ""); r.code != 401 {
		t.Fatalf("auth still applies: %d", r.code)
	}
}

// ---- events ----

func TestEventStreamStartAndPayloads(t *testing.T) {
	e := newEnv(t)
	e.eng.fillFixtures()
	s := e.openStream()
	if ev := s.next(t); ev.Name != "retry" || ev.Data != "2000" {
		t.Fatalf("first bytes = %+v", ev)
	}
	first := s.next(t)
	if first.Name != "state" || first.ID != "1" {
		t.Fatalf("first event = %+v", first)
	}
	var st stateBody
	if err := json.Unmarshal([]byte(first.Data), &st); err != nil || st.State != "disconnected" || st.Snapshot.State != "disconnected" || len(st.Networks) != 1 {
		t.Fatalf("state payload: %v %+v", err, st)
	}
	if strings.Contains(first.Data, "\n") {
		t.Fatal("data is not one line")
	}

	at := t1
	e.eng.publish(pc.Event{Kind: pc.EventRoutes, Changes: []status.Change{
		{Domain: "video.example", From: route.Tunnel, To: route.Desync, At: at},
		{Domain: "new.example", To: route.Direct, At: at},
	}})
	ev := s.until(t, "routes")
	want := `{"changes":[{"Domain":"video.example","From":"tunnel","To":"desync","At":"2026-10-31T12:05:00Z"},{"Domain":"new.example","From":"","To":"direct","At":"2026-10-31T12:05:00Z"}]}`
	if ev.Data != want || ev.ID != "2" {
		t.Fatalf("routes event = %+v", ev)
	}
	e.eng.publish(pc.Event{Kind: pc.EventCheck, Check: pc.CheckEvent{Running: true, Domains: []string{"example.org"}}})
	if ev := s.until(t, "check"); ev.Data != `{"running":true,"domains":["example.org"]}` {
		t.Fatalf("check start = %s", ev.Data)
	}
	e.eng.publish(pc.Event{Kind: pc.EventCheck, Check: pc.CheckEvent{Running: false, Changed: 2}})
	if ev := s.until(t, "check"); ev.Data != `{"running":false,"domains":null,"changed":2}` {
		t.Fatalf("check end = %s", ev.Data)
	}
	e.eng.publish(pc.Event{Kind: pc.EventTraffic, Traffic: pc.TrafficTick{
		Upload: 10, Download: 20, Connections: 3, Delta: map[route.Route]int64{route.Direct: 5},
		Total: map[route.Route]int64{route.Direct: 50, route.Tunnel: 10}, Savings: 0.5, At: t1,
	}})
	want = `{"uploadTotal":10,"downloadTotal":20,"connections":3,"delta":{"direct":5},"Traffic":{"direct":50,"tunnel":10},"Savings":0.5,"At":"2026-10-31T12:05:00Z"}`
	if ev := s.until(t, "traffic"); ev.Data != want {
		t.Fatalf("traffic = %s", ev.Data)
	}
	e.eng.publish(pc.Event{Kind: pc.EventTraffic})
	if ev := s.until(t, "traffic"); !strings.Contains(ev.Data, `"delta":{}`) || !strings.Contains(ev.Data, `"Traffic":{}`) {
		t.Fatalf("empty traffic = %s", ev.Data)
	}
	// A reconnect starts over with a full state, no replay.
	s2 := e.openStream()
	s2.until(t, "retry")
	if ev := s2.next(t); ev.Name != "state" || ev.ID != "1" {
		t.Fatalf("reconnect first event = %+v", ev)
	}
}

func TestEventStreamKeepAlive(t *testing.T) {
	e := newEnv(t)
	e.srv.pingEvery = 20 * time.Millisecond
	s := e.openStream()
	s.until(t, "state")
	if ev := s.until(t, "comment"); ev.Data != "ping" {
		t.Fatalf("comment = %+v", ev)
	}
}

func TestEventStreamsEndOnShutdown(t *testing.T) {
	e := newEnv(t)
	var streams []*sseClient
	for i := 0; i < 3; i++ {
		c := e.openStream()
		c.until(t, "state")
		streams = append(streams, c)
	}
	if r := e.post("/api/v1/shutdown"); r.code != 200 {
		t.Fatalf("shutdown: %d", r.code)
	}
	for i, c := range streams {
		ev := c.until(t, "shutdown")
		if ev.Data != "{}" {
			t.Fatalf("stream %d: shutdown event %+v", i, ev)
		}
		c.ended(t)
	}
	// The server returns once the streams are gone.
	done := make(chan struct{})
	go func() { e.srv.finish(e.ts.Config); close(done) }()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("finish did not return")
	}
	if _, err := e.ts.Client().Do(e.request("GET", "/api/v1/version", nil)); err == nil {
		t.Fatal("the listener is still open")
	}
}

func TestStreamsEndWhenTheAppEndsForAnotherReason(t *testing.T) {
	e := newEnv(t)
	c := e.openStream()
	c.until(t, "state")
	e.cancel() // a signal or the parent process, not the endpoint
	c.until(t, "shutdown")
	c.ended(t)
}

// blockingWriter never lets a write finish after the first few, like a client that does not read.
type blockingWriter struct {
	hdr     http.Header
	mu      sync.Mutex
	writes  int
	release chan struct{}
}

func (w *blockingWriter) Header() http.Header { return w.hdr }
func (w *blockingWriter) WriteHeader(int)     {}
func (w *blockingWriter) Flush()              {}
func (w *blockingWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	w.writes++
	n := w.writes
	w.mu.Unlock()
	if n > 2 {
		<-w.release
		return 0, io.ErrClosedPipe
	}
	return len(p), nil
}

func TestSlowClientIsDroppedWithoutBlockingOthers(t *testing.T) {
	e := newEnv(t)
	fast := e.openStream()
	fast.until(t, "state")

	bw := &blockingWriter{hdr: http.Header{}, release: make(chan struct{})}
	req := httptest.NewRequest("GET", "/api/v1/events", nil)
	req.Host = e.ts.Listener.Addr().String()
	req.Header.Set("Authorization", "Bearer "+testToken)
	done := make(chan struct{})
	go func() { e.srv.ServeHTTP(bw, req); close(done) }()
	waitFor(t, "the slow stream", func() bool { return e.eng.subscribers() == 2 })
	e.eng.publish(pc.Event{Kind: pc.EventRoutes}) // the third write blocks
	waitFor(t, "the slow stream to block", func() bool { bw.mu.Lock(); defer bw.mu.Unlock(); return bw.writes >= 3 })

	published := make(chan struct{})
	go func() {
		for i := 0; i < 500; i++ {
			e.eng.publish(pc.Event{Kind: pc.EventRoutes, Changes: []status.Change{{Domain: "a.example", To: route.Direct}}})
			time.Sleep(100 * time.Microsecond)
		}
		close(published)
	}()
	select {
	case <-published:
	case <-time.After(10 * time.Second):
		t.Fatal("publishing was blocked by a slow client")
	}
	// The fast client still gets events; the slow one was dropped.
	fast.until(t, "routes")
	waitFor(t, "the slow stream to be dropped", func() bool { return e.eng.subscribers() == 1 })
	close(bw.release)
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("the slow handler did not return")
	}
}

// ---- errors and secrets ----

const secretMarker = "SECRET-MARKER-9f3a"

func TestErrorBodiesGoldenAndNoSecrets(t *testing.T) {
	e := newEnv(t)
	e.eng.fillFixtures()
	e.eng.checkErr = fmt.Errorf("engine failed at /home/user/%s (203.0.113.9)", secretMarker)
	e.eng.logsErr = fmt.Errorf("read %s: denied", secretMarker)
	e.eng.connectHook = func(context.Context) error { return fmt.Errorf("start sing-box: %s", secretMarker) }

	var bodies [][]byte
	r := e.do("POST", "/api/v1/check-now", `{"domains":["example.org"]}`)
	if r.code != 500 || r.errCode(t) != "internal" {
		t.Fatalf("internal error: %d %s", r.code, r.body)
	}
	golden(t, "error_internal", r.body)
	bodies = append(bodies, r.body)
	r = e.get("/api/v1/logs?name=splitter")
	if r.code != 500 {
		t.Fatalf("logs error: %d", r.code)
	}
	bodies = append(bodies, r.body)

	r = e.do("POST", "/api/v1/check-now", `{"domains":["`+secretMarker+` x"]}`) // request content must not be echoed
	bodies = append(bodies, r.body)
	e.post("/api/v1/connect")
	waitFor(t, "connect to fail", func() bool { return e.eng.callCount("Connect") == 1 && e.eng.State() == pc.StateDisconnected })
	bodies = append(bodies, e.get("/api/v1/state").body)
	forbidden := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/api/v1/state", nil)
	req.Host = secretMarker + ".example"
	e.srv.ServeHTTP(forbidden, req)
	golden(t, "error_forbidden", forbidden.Body.Bytes())
	bodies = append(bodies, forbidden.Body.Bytes())
	golden(t, "error_not_found", e.get("/api/v1/nope").body)
	golden(t, "error_method_not_allowed", e.do("PUT", "/api/v1/state", "").body)
	golden(t, "error_too_large", e.do("POST", "/api/v1/check-now", `{"domains":["`+strings.Repeat("a", maxBodyBytes)+`"]}`).body)

	for _, b := range bodies {
		if strings.Contains(string(b), secretMarker) || strings.Contains(string(b), "203.0.113.9") || strings.Contains(string(b), "/home/user") {
			t.Errorf("a response leaks engine or request content: %s", b)
		}
	}
	// Internal error text stays out of the log too: pc logs its own errors, the gateway adds only the operation.
	if strings.Contains(e.log.String(), secretMarker) {
		t.Errorf("the log contains engine error text:\n%s", e.log)
	}
}

func TestTokenAndBodiesNeverReachTheLog(t *testing.T) {
	e := newEnv(t)
	e.eng.fillFixtures()
	wrong := "wrong-token-" + strings.Repeat("z", 30)
	e.do("GET", "/api/v1/state", "", "Authorization", "Bearer "+wrong)
	e.do("POST", "/api/v1/check-now", `{"domains":["example.org","body-marker.example"]}`)
	e.do("POST", "/api/v1/check-now", `{"domains":["body-marker-2.example"],"x":1}`)
	e.get("/api/v1/logs?name=splitter")
	e.post("/api/v1/connect")
	e.post("/api/v1/disconnect")
	s := e.openStream()
	s.until(t, "state")
	e.post("/api/v1/shutdown")
	s.ended(t)

	logs := e.log.String()
	for _, secret := range []string{testToken, wrong, "body-marker", "Authorization", "Bearer"} {
		if strings.Contains(logs, secret) {
			t.Errorf("the log contains %q:\n%s", secret, logs)
		}
	}
	for _, want := range []string{"request", "method=GET", "path=/api/v1/state", "status=200", "missing or wrong token", "shutdown requested"} {
		if !strings.Contains(logs, want) {
			t.Errorf("the log lacks %q:\n%s", want, logs)
		}
	}
}

func TestEngineInterfaceIsPCApp(t *testing.T) {
	var _ engine = (*pc.App)(nil)
}
