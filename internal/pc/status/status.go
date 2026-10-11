package status

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"strconv"
	"sync"
	"time"

	"github.com/nikoejento/obhod/internal/lib/route"
	"github.com/nikoejento/obhod/internal/pc/lib/clashapi"
)

// DefaultListen is the address used when Config.Listen is empty.
const DefaultListen = "127.0.0.1:9091"

// ErrNotLoopback is returned (wrapped) by New for a listen address that is not loopback.
var ErrNotLoopback = errors.New("status: listen address must be a loopback address")

// Snapshot is the state shown on the status page.
type Snapshot struct {
	Version   string
	Network   string // label (ASN name), never the public IP
	LastCheck time.Time
	NextCheck time.Time
	Routes    map[route.Route]int   // domain counts
	Traffic   map[route.Route]int64 // bytes
	Savings   float64               // share of traffic NOT via tunnel
	Processes []Process
	Warnings  []string
	Recent    []Change // last route changes
}

// Process is one managed process.
type Process struct {
	Name, State string
	Restarts    int
}

// Change is one route change.
type Change struct {
	Domain   string
	From, To route.Route
	At       time.Time
}

// Source is implemented by an adapter in the pc app.
type Source interface{ Snapshot() Snapshot }

// Config configures the server.
type Config struct {
	Listen string `yaml:"listen"` // default "127.0.0.1:9091"; non-loopback addresses are rejected
	Token  string `yaml:"token"`  // optional; if set, required as ?token=
}

// Server serves the read-only status page.
type Server struct {
	cfg  Config
	src  Source
	live clashapi.Client

	mu   sync.Mutex
	port string // the port requests must address; set from Listen or the bound listener
}

// New validates cfg and returns a Server. live may be nil, then /api/traffic reports no data.
func New(cfg Config, src Source, live clashapi.Client) (*Server, error) {
	if src == nil {
		return nil, errors.New("status: no Source")
	}
	if cfg.Listen == "" {
		cfg.Listen = DefaultListen
	}
	host, port, err := net.SplitHostPort(cfg.Listen)
	if err != nil {
		return nil, fmt.Errorf("status: listen address %q: %w", cfg.Listen, err)
	}
	if n, err := strconv.Atoi(port); err != nil || n < 0 || n > 65535 {
		return nil, fmt.Errorf("status: listen address %q: bad port", cfg.Listen)
	}
	if err := checkLoopbackHost(host); err != nil {
		return nil, fmt.Errorf("%w: %q", err, cfg.Listen)
	}
	s := &Server{cfg: cfg, src: src, live: live}
	if port != "0" {
		s.port = port
	}
	return s, nil
}

// checkLoopbackHost accepts a loopback IP or a name that resolves only to loopback addresses.
func checkLoopbackHost(host string) error {
	if ip, err := netip.ParseAddr(host); err == nil {
		if !ip.IsLoopback() {
			return ErrNotLoopback
		}
		return nil
	}
	if host != "localhost" {
		return ErrNotLoopback // other names could resolve anywhere
	}
	addrs, err := net.DefaultResolver.LookupIPAddr(context.Background(), host)
	if err != nil || len(addrs) == 0 {
		return ErrNotLoopback
	}
	for _, a := range addrs {
		if !a.IP.IsLoopback() {
			return ErrNotLoopback
		}
	}
	return nil
}

// Run serves until ctx is done, then shuts down gracefully. It returns nil on
// a clean shutdown.
func (s *Server) Run(ctx context.Context) error {
	ln, err := net.Listen("tcp", s.cfg.Listen)
	if err != nil {
		return fmt.Errorf("status: listen: %w", err)
	}
	if _, port, err := net.SplitHostPort(ln.Addr().String()); err == nil {
		s.mu.Lock()
		s.port = port
		s.mu.Unlock()
	}
	srv := &http.Server{
		Handler:           s.handler(),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       10 * time.Second,
		WriteTimeout:      10 * time.Second,
		IdleTimeout:       60 * time.Second,
	}
	errc := make(chan error, 1)
	go func() { errc <- srv.Serve(ln) }()
	select {
	case err := <-errc:
		return fmt.Errorf("status: serve: %w", err)
	case <-ctx.Done():
	}
	shutCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := srv.Shutdown(shutCtx); err != nil {
		_ = srv.Close()
	}
	<-errc
	return nil
}
