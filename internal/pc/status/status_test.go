package status

import (
	"bytes"
	"context"
	"errors"
	"flag"
	"io"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/nikoejento/obhod/internal/lib/route"
	"github.com/nikoejento/obhod/internal/pc/lib/clashapi"
)

var update = flag.Bool("update", false, "rewrite golden files")

type fakeSource struct{ snap Snapshot }

func (f fakeSource) Snapshot() Snapshot { return f.snap }

func sample() Snapshot {
	at := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	return Snapshot{
		Version:   "splitter 1.2.3",
		State:     "connected",
		Network:   "AS12389 Rostelecom",
		LastCheck: at,
		NextCheck: at.Add(30 * time.Minute),
		Routes:    map[route.Route]int{route.Direct: 40, route.Desync: 3, route.Tunnel: 7},
		Traffic:   map[route.Route]int64{route.Direct: 9000, route.Tunnel: 1000},
		Savings:   0.9,
		Processes: []Process{{Name: "sing-box", State: "running"}, {Name: "winws2", State: "running", Restarts: 1}},
		Warnings:  []string{"probe port blocked"},
		Recent:    []Change{{Domain: "example.org", From: route.Tunnel, To: route.Desync, At: at}},
	}
}

func newServer(t *testing.T, cfg Config, live clashapi.Client) *Server {
	t.Helper()
	s, err := New(cfg, fakeSource{sample()}, live)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

// get sends a request straight to the handler with an explicit Host header.
func get(t *testing.T, h http.Handler, method, target, host string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, target, nil)
	req.Host = host
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func TestNewValidatesLoopback(t *testing.T) {
	ok := []string{"", "127.0.0.1:9091", "127.0.0.1:0", "[::1]:9091", "127.5.5.5:80", "localhost:9091"}
	bad := []string{"0.0.0.0:9091", "[::]:9091", ":9091", "192.168.1.10:9091", "8.8.8.8:9091", "example.com:9091", "127.0.0.1", "127.0.0.1:99999", "127.0.0.1:x"}
	for _, l := range ok {
		if _, err := New(Config{Listen: l}, fakeSource{}, nil); err != nil {
			t.Errorf("Listen %q rejected: %v", l, err)
		}
	}
	for _, l := range bad {
		if _, err := New(Config{Listen: l}, fakeSource{}, nil); err == nil {
			t.Errorf("Listen %q accepted", l)
		}
	}
	if _, err := New(Config{Listen: "10.0.0.1:1"}, fakeSource{}, nil); !errors.Is(err, ErrNotLoopback) {
		t.Errorf("err = %v, want ErrNotLoopback", err)
	}
	if _, err := New(Config{}, nil, nil); err == nil {
		t.Error("nil source accepted")
	}
}

func TestStatusGolden(t *testing.T) {
	h := newServer(t, Config{}, nil).handler()
	rec := get(t, h, "GET", "/api/status", "127.0.0.1:9091")
	if rec.Code != 200 || rec.Header().Get("Content-Type") != "application/json" {
		t.Fatalf("code %d, type %q", rec.Code, rec.Header().Get("Content-Type"))
	}
	path := filepath.Join("testdata", "status.golden.json")
	if *update {
		if err := os.MkdirAll("testdata", 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, rec.Body.Bytes(), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(bytes.ReplaceAll(want, []byte("\r\n"), []byte("\n")), rec.Body.Bytes()) {
		t.Fatalf("status JSON differs from golden (run with -update)\n%s", rec.Body.String())
	}
}

func TestTrafficTotalsOnly(t *testing.T) {
	live := clashapi.NewFake(clashapi.Snapshot{
		UploadTotal: 11, DownloadTotal: 22,
		Connections: []clashapi.Connection{{ID: "1", Host: "secret.example", DstIP: "203.0.113.9"}, {ID: "2", Host: "other.example"}},
	})
	h := newServer(t, Config{}, live).handler()
	rec := get(t, h, "GET", "/api/traffic", "127.0.0.1:9091")
	if rec.Code != 200 {
		t.Fatalf("code %d", rec.Code)
	}
	body := rec.Body.String()
	for _, leak := range []string{"secret.example", "other.example", "203.0.113.9"} {
		if strings.Contains(body, leak) {
			t.Fatalf("traffic response leaks %q: %s", leak, body)
		}
	}
	for _, want := range []string{`"uploadTotal": 11`, `"downloadTotal": 22`, `"connections": 2`} {
		if !strings.Contains(body, want) {
			t.Fatalf("missing %q in %s", want, body)
		}
	}
	// No client: empty totals instead of an error.
	h = newServer(t, Config{}, nil).handler()
	if rec := get(t, h, "GET", "/api/traffic", "127.0.0.1:9091"); rec.Code != 200 {
		t.Fatalf("no live client: code %d", rec.Code)
	}
}

type failingAPI struct{ clashapi.Client }

func (failingAPI) Connections(context.Context) (clashapi.Snapshot, error) {
	return clashapi.Snapshot{}, errors.New("clashapi: GET /connections: 401 secret-in-error")
}

// countingAPI counts how often the full connection list is downloaded.
type countingAPI struct {
	clashapi.Client
	n int
}

func (c *countingAPI) Connections(context.Context) (clashapi.Snapshot, error) {
	c.n++
	return clashapi.Snapshot{UploadTotal: 1}, nil
}

func TestTrafficIsCachedBriefly(t *testing.T) {
	api := &countingAPI{}
	now := time.Unix(1000, 0)
	srv := newServer(t, Config{}, api)
	srv.now = func() time.Time { return now }
	h := srv.handler()
	for range 5 {
		if rec := get(t, h, "GET", "/api/traffic", "127.0.0.1:9091"); rec.Code != 200 {
			t.Fatalf("code %d", rec.Code)
		}
	}
	if api.n != 1 {
		t.Fatalf("connection list downloaded %d times for 5 quick calls", api.n)
	}
	now = now.Add(trafficMaxAge)
	get(t, h, "GET", "/api/traffic", "127.0.0.1:9091")
	if api.n != 2 {
		t.Fatalf("cached totals not refreshed after %v: %d downloads", trafficMaxAge, api.n)
	}
}

func TestListenFindsBusyPortEarly(t *testing.T) {
	first := newServer(t, Config{Listen: "127.0.0.1:0"}, nil)
	if err := first.Listen(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { first.ln.Close() })
	first.mu.Lock()
	port := first.port
	first.mu.Unlock()
	second := newServer(t, Config{Listen: "127.0.0.1:" + port}, nil)
	if err := second.Listen(); err == nil {
		t.Fatal("second Listen on a busy port succeeded")
	}
}

func TestTrafficErrorHidesDetails(t *testing.T) {
	h := newServer(t, Config{}, failingAPI{}).handler()
	rec := get(t, h, "GET", "/api/traffic", "127.0.0.1:9091")
	if rec.Code != http.StatusBadGateway || strings.Contains(rec.Body.String(), "secret-in-error") {
		t.Fatalf("code %d body %q", rec.Code, rec.Body.String())
	}
}

func TestTokenRequiredAndAccepted(t *testing.T) {
	h := newServer(t, Config{Token: "s3cret"}, nil).handler()
	for _, path := range []string{"/", "/api/status", "/api/traffic"} {
		if rec := get(t, h, "GET", path, "127.0.0.1:9091"); rec.Code != 401 {
			t.Errorf("%s without token: %d", path, rec.Code)
		}
		if rec := get(t, h, "GET", path+"?token=wrong", "127.0.0.1:9091"); rec.Code != 401 {
			t.Errorf("%s wrong token: %d", path, rec.Code)
		}
		if rec := get(t, h, "GET", path+"?token=s3cret", "127.0.0.1:9091"); rec.Code != 200 {
			t.Errorf("%s right token: %d", path, rec.Code)
		}
	}
	// The static assets carry no data and need no token (the page cannot add it to <script src>).
	if rec := get(t, h, "GET", "/assets/app.js", "127.0.0.1:9091"); rec.Code != 200 {
		t.Errorf("asset without token: %d", rec.Code)
	}
	// Without a configured token nothing is required.
	h = newServer(t, Config{}, nil).handler()
	if rec := get(t, h, "GET", "/api/status", "127.0.0.1:9091"); rec.Code != 200 {
		t.Errorf("no token configured: %d", rec.Code)
	}
}

func TestHostHeaderGuard(t *testing.T) {
	h := newServer(t, Config{}, nil).handler()
	for host, want := range map[string]int{
		"127.0.0.1:9091":    200,
		"localhost:9091":    200,
		"[::1]:9091":        200,
		"evil.example:9091": 403, // DNS rebinding: a foreign name pointing at 127.0.0.1
		"evil.example":      403,
		"127.0.0.1:8080":    403, // another port
		"192.168.1.5:9091":  403,
		"127.0.0.1":         403, // implies port 80
	} {
		if rec := get(t, h, "GET", "/api/status", host); rec.Code != want {
			t.Errorf("Host %q: %d, want %d", host, rec.Code, want)
		}
	}
}

func TestMethodsAndPaths(t *testing.T) {
	h := newServer(t, Config{}, nil).handler()
	for _, m := range []string{"POST", "PUT", "DELETE", "PATCH", "HEAD", "OPTIONS"} {
		for _, p := range []string{"/", "/api/status", "/api/traffic", "/assets/app.js"} {
			rec := get(t, h, m, p, "127.0.0.1:9091")
			if rec.Code != http.StatusMethodNotAllowed || rec.Header().Get("Allow") != "GET" {
				t.Errorf("%s %s: %d", m, p, rec.Code)
			}
		}
	}
	for _, p := range []string{"/nope", "/api/", "/assets/", "/assets/../secret", "/assets/missing.js"} {
		if rec := get(t, h, "GET", p, "127.0.0.1:9091"); rec.Code != 404 && rec.Code != 301 && rec.Code != 307 {
			t.Errorf("GET %s: %d", p, rec.Code)
		}
	}
}

func TestSecurityHeaders(t *testing.T) {
	h := newServer(t, Config{}, nil).handler()
	for _, p := range []string{"/", "/api/status", "/assets/app.js", "/nope"} {
		hd := get(t, h, "GET", p, "127.0.0.1:9091").Header()
		if hd.Get("Content-Security-Policy") != "default-src 'self'" || hd.Get("X-Content-Type-Options") != "nosniff" {
			t.Errorf("%s: headers %v", p, hd)
		}
		for k := range hd {
			if strings.HasPrefix(strings.ToLower(k), "access-control-") {
				t.Errorf("%s: CORS header %s", p, k)
			}
		}
	}
	// A refused request gets the headers too.
	if hd := get(t, h, "GET", "/", "evil.example").Header(); hd.Get("Content-Security-Policy") == "" {
		t.Error("403 response lacks the CSP header")
	}
	if rec := get(t, h, "GET", "/", "127.0.0.1:9091"); !strings.Contains(rec.Body.String(), "<title>splitter status</title>") ||
		!strings.HasPrefix(rec.Header().Get("Content-Type"), "text/html") {
		t.Errorf("index page: %d %q", rec.Code, rec.Header().Get("Content-Type"))
	}
	// A cross-origin preflight is refused, not answered.
	req := httptest.NewRequest("OPTIONS", "/api/status", nil)
	req.Host = "127.0.0.1:9091"
	req.Header.Set("Origin", "https://evil.example")
	req.Header.Set("Access-Control-Request-Method", "GET")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != 405 || rec.Header().Get("Access-Control-Allow-Origin") != "" {
		t.Errorf("preflight: %d %v", rec.Code, rec.Header())
	}
}

func TestAssetsAreSelfContained(t *testing.T) {
	n := 0
	err := fs.WalkDir(assets, "assets", func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		n++
		b, err := assets.ReadFile(path)
		if err != nil {
			return err
		}
		if strings.Contains(strings.ToLower(string(b)), "http") {
			t.Errorf("%s mentions http: no external URLs allowed", path)
		}
		if path == "assets/index.html" && strings.Contains(string(b), "<script>") {
			t.Errorf("%s has an inline script, which the CSP forbids", path)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if n < 3 {
		t.Fatalf("only %d embedded files", n)
	}
}

func TestRunServesAndShutsDown(t *testing.T) {
	s := newServer(t, Config{Listen: "127.0.0.1:0", Token: "tok"}, nil)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- s.Run(ctx) }()

	var port string
	deadline := time.Now().Add(5 * time.Second)
	for port == "" {
		s.mu.Lock()
		port = s.port
		s.mu.Unlock()
		if time.Now().After(deadline) {
			t.Fatal("server did not start")
		}
		time.Sleep(5 * time.Millisecond)
	}
	res, err := http.Get("http://127.0.0.1:" + port + "/api/status?token=tok")
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(res.Body)
	res.Body.Close()
	if res.StatusCode != 200 || !strings.Contains(string(body), "splitter 1.2.3") {
		t.Fatalf("status %d body %s", res.StatusCode, body)
	}
	// A request that addresses the server by another name is refused even over the real socket.
	req, _ := http.NewRequest("GET", "http://127.0.0.1:"+port+"/api/status?token=tok", nil)
	req.Host = "evil.example"
	res, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != 403 {
		t.Fatalf("foreign Host: %d", res.StatusCode)
	}

	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Run = %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("Run did not stop")
	}
}
