package dnscheck

import (
	"net/netip"
	"strings"
	"sync"
)

// Stubs are ISP block-page IPs/hosts learned during calibration. It is safe for concurrent use.
type Stubs struct {
	mu    sync.RWMutex
	ips   map[netip.Addr]bool
	hosts map[string]bool
}

// NewStubs returns an empty set.
func NewStubs() *Stubs { return &Stubs{ips: map[netip.Addr]bool{}, hosts: map[string]bool{}} }

// AddIP records a stub IP.
func (s *Stubs) AddIP(ip netip.Addr) {
	s.mu.Lock()
	s.ips[ip.Unmap()] = true
	s.mu.Unlock()
}

// AddHost records a stub host name (case-insensitive).
func (s *Stubs) AddHost(host string) {
	s.mu.Lock()
	s.hosts[strings.ToLower(strings.TrimSuffix(host, "."))] = true
	s.mu.Unlock()
}

// HasIP reports whether ip is a known stub IP. A nil *Stubs is empty.
func (s *Stubs) HasIP(ip netip.Addr) bool {
	if s == nil {
		return false
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.ips[ip.Unmap()]
}

// HasHost reports whether host is a known stub host. A nil *Stubs is empty.
func (s *Stubs) HasHost(host string) bool {
	if s == nil {
		return false
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.hosts[strings.ToLower(strings.TrimSuffix(host, "."))]
}

// Hosts returns the known stub hosts and IPs as strings, for the classifier.
func (s *Stubs) Hosts() []string {
	if s == nil {
		return nil
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	var out []string
	for h := range s.hosts {
		out = append(out, h)
	}
	for ip := range s.ips {
		out = append(out, ip.String())
	}
	return out
}
