package clashapi

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

const testSecret = "s3cr3t-token"

func fixture(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile("testdata/" + name)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// serve replays a fixed response and records the Authorization header of each request.
func serve(t *testing.T, status int, body []byte) (*httptest.Server, func() []string) {
	t.Helper()
	var mu sync.Mutex
	var auth []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		auth = append(auth, r.Header.Get("Authorization"))
		mu.Unlock()
		w.WriteHeader(status)
		_, _ = w.Write(body)
	}))
	t.Cleanup(srv.Close)
	return srv, func() []string {
		mu.Lock()
		defer mu.Unlock()
		return append([]string(nil), auth...)
	}
}

func TestConnections(t *testing.T) {
	srv, auth := serve(t, 200, fixture(t, "connections.json"))
	c := New(srv.URL+"/", testSecret, nil).(*client)
	at := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	c.now = func() time.Time { return at }

	s, err := c.Connections(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if s.At != at || s.UploadTotal != 7890 || s.DownloadTotal != 123456 || len(s.Connections) != 2 {
		t.Fatalf("snapshot = %+v", s)
	}
	got := s.Connections[0]
	if got.ID != "6b1c0c0e-6c3a-4c4e-9a53-0d5b4d7f0001" || got.Host != "example.com" ||
		got.DstIP != "142.250.74.14" || got.DstPort != "443" || got.Network != "tcp" ||
		got.Process != `C:\Program Files\Browser\browser.exe` ||
		got.Upload != 1200 || got.Download != 98000 ||
		len(got.Chains) != 1 || got.Chains[0] != "tunnel" || got.Rule != "rule_set=tunnel => route(tunnel)" {
		t.Fatalf("conn[0] = %+v", got)
	}
	if want := time.Date(2026, 10, 1, 9, 0, 0, 123456789, time.UTC); !got.Start.Equal(want) {
		t.Fatalf("start = %v, want %v", got.Start, want)
	}
	if s.Connections[1].Network != "udp" || s.Connections[1].Process != "" {
		t.Fatalf("conn[1] = %+v", s.Connections[1])
	}
	if a := auth(); len(a) != 1 || a[0] != "Bearer "+testSecret {
		t.Fatalf("auth = %q", a)
	}
}

func TestConnectionsEmpty(t *testing.T) {
	srv, _ := serve(t, 200, fixture(t, "connections_empty.json"))
	s, err := New(srv.URL, "", nil).Connections(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(s.Connections) != 0 || s.At.IsZero() {
		t.Fatalf("snapshot = %+v", s)
	}
}

func TestVersion(t *testing.T) {
	srv, _ := serve(t, 200, fixture(t, "version.json"))
	v, err := New(srv.URL, testSecret, nil).Version(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if v != "sing-box 1.14.2" {
		t.Fatalf("version = %q", v)
	}
}

func TestNoAuthHeaderWithoutSecret(t *testing.T) {
	srv, auth := serve(t, 200, fixture(t, "version.json"))
	if _, err := New(srv.URL, "", nil).Version(context.Background()); err != nil {
		t.Fatal(err)
	}
	if a := auth(); len(a) != 1 || a[0] != "" {
		t.Fatalf("auth = %q, want empty", a)
	}
}

func TestHTTPErrors(t *testing.T) {
	for _, status := range []int{401, 500} {
		srv, _ := serve(t, status, []byte(`{"message":"`+testSecret+`"}`))
		c := New(srv.URL, testSecret, nil)
		_, err := c.Connections(context.Background())
		want := "clashapi: GET /connections: " + strconv.Itoa(status)
		if err == nil || err.Error() != want {
			t.Fatalf("status %d: err = %v, want %q", status, err, want)
		}
		want = "clashapi: GET /version: " + strconv.Itoa(status)
		if _, err := c.Version(context.Background()); err == nil || err.Error() != want {
			t.Fatalf("status %d: version err = %v, want %q", status, err, want)
		}
	}
}

func TestMalformedBody(t *testing.T) {
	srv, _ := serve(t, 200, []byte(`{"connections": [`))
	_, err := New(srv.URL, "", nil).Connections(context.Background())
	if err == nil || !strings.Contains(err.Error(), "GET /connections: decode") {
		t.Fatalf("err = %v", err)
	}
}

func TestBadStart(t *testing.T) {
	srv, _ := serve(t, 200, []byte(`{"connections":[{"id":"a","start":"yesterday"},{"id":"b","start":"2026-10-01T09:00:00Z"}]}`))
	snap, err := New(srv.URL, "", nil).Connections(context.Background())
	if err != nil {
		t.Fatalf("one bad start failed the snapshot: %v", err)
	}
	if len(snap.Connections) != 2 || !snap.Connections[0].Start.IsZero() || snap.Connections[1].Start.IsZero() {
		t.Fatalf("connections = %+v", snap.Connections)
	}
}

func TestContextCancel(t *testing.T) {
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { <-release }))
	defer srv.Close()
	defer close(release)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := New(srv.URL, testSecret, nil).Connections(ctx)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
}

func TestSecretNotInTransportError(t *testing.T) {
	srv := httptest.NewServer(http.NotFoundHandler())
	url := srv.URL
	srv.Close() // connection refused
	_, err := New(url, testSecret, nil).Version(context.Background())
	if err == nil {
		t.Fatal("expected error")
	}
	if strings.Contains(err.Error(), testSecret) {
		t.Fatalf("secret leaked: %v", err)
	}
}

func TestDefaultClientIgnoresProxy(t *testing.T) {
	c := New("http://127.0.0.1:1", "", nil).(*client)
	tr, ok := c.hc.Transport.(*http.Transport)
	if !ok || tr.Proxy != nil || c.hc.Timeout <= 0 {
		t.Fatalf("default client = %+v", c.hc)
	}
}

func TestFakeRepeatsLast(t *testing.T) {
	f := NewFake(Snapshot{UploadTotal: 1}, Snapshot{UploadTotal: 2})
	for _, want := range []int64{1, 2, 2, 2} {
		s, err := f.Connections(context.Background())
		if err != nil || s.UploadTotal != want {
			t.Fatalf("got %+v, %v; want upload %d", s, err, want)
		}
	}
	if s, err := NewFake().Connections(context.Background()); err != nil || len(s.Connections) != 0 {
		t.Fatalf("empty fake: %+v, %v", s, err)
	}
	if v, err := f.Version(context.Background()); err != nil || v != FakeVersion {
		t.Fatalf("version = %q, %v", v, err)
	}
}

func TestFakeConcurrent(t *testing.T) {
	f := NewFake(Snapshot{UploadTotal: 1}, Snapshot{UploadTotal: 2})
	var wg sync.WaitGroup
	for range 20 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, _ = f.Connections(context.Background())
		}()
	}
	wg.Wait()
}
