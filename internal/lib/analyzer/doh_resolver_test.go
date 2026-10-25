package analyzer

import (
	"context"
	"errors"
	"net"
	"sync"
	"testing"

	"github.com/nikoejento/obhod/internal/lib/route"
)

type recDialer struct {
	mu    sync.Mutex
	addrs []string
}

func (r *recDialer) Route() route.Route { return route.Direct }
func (r *recDialer) DialContext(_ context.Context, _, addr string) (net.Conn, error) {
	r.mu.Lock()
	r.addrs = append(r.addrs, addr)
	r.mu.Unlock()
	return nil, errors.New("fake: no network")
}

func TestNewDoHResolverValidates(t *testing.T) {
	cfg := DefaultConfig()
	if _, err := NewDoHResolver(cfg, nil); err == nil {
		t.Error("nil dialer accepted")
	}
	cfg.DoH = nil
	if _, err := NewDoHResolver(cfg, &recDialer{}); err == nil {
		t.Error("empty DoH accepted")
	}
}

func TestNewDoHResolverDialsThroughInjectedDialer(t *testing.T) {
	cfg := DefaultConfig()
	cfg.DoH = []string{"https://192.0.2.7/dns-query"}
	d := &recDialer{}
	r, err := NewDoHResolver(cfg, d)
	if err != nil || r == nil {
		t.Fatalf("resolver %v, err %v", r, err)
	}
	if _, err := r.LookupTXT(context.Background(), "example.test"); err == nil {
		t.Fatal("lookup succeeded with a failing dialer")
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	if len(d.addrs) == 0 || d.addrs[0] != "192.0.2.7:443" {
		t.Fatalf("dialed %v", d.addrs)
	}
}
