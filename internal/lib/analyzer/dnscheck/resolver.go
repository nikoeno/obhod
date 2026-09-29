package dnscheck

import (
	"context"
	"fmt"
	"net"
	"net/netip"
	"strings"
)

// DialFunc is how this package reaches the network (injected; it doesn't import dialer).
type DialFunc func(ctx context.Context, network, addr string) (net.Conn, error)

// Resolver looks up names.
type Resolver interface {
	LookupIP(ctx context.Context, host string) ([]netip.Addr, error) // A (+AAAA if enabled)
	LookupTXT(ctx context.Context, name string) ([]string, error)
}

type fake struct {
	answers map[string][]netip.Addr
	txt     map[string][]string
}

// NewFake returns a resolver answering from fixed maps; unknown names fail with a "no such host" error.
func NewFake(answers map[string][]netip.Addr, txt map[string][]string) Resolver {
	return fake{answers: answers, txt: txt}
}

func (f fake) LookupIP(ctx context.Context, host string) ([]netip.Addr, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if a, ok := f.answers[strings.ToLower(host)]; ok && len(a) > 0 {
		return append([]netip.Addr(nil), a...), nil
	}
	return nil, fmt.Errorf("lookup %s: %w", host, ErrNXDomain)
}

func (f fake) LookupTXT(ctx context.Context, name string) ([]string, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if t, ok := f.txt[strings.ToLower(name)]; ok && len(t) > 0 {
		return append([]string(nil), t...), nil
	}
	return nil, fmt.Errorf("lookup %s: %w", name, ErrNXDomain)
}

type system struct{ r *net.Resolver }

// NewSystem returns a resolver backed by the operating system. It is only used for the poisoning comparison.
func NewSystem() Resolver { return system{r: &net.Resolver{}} }

func (s system) LookupIP(ctx context.Context, host string) ([]netip.Addr, error) {
	ips, err := s.r.LookupNetIP(ctx, "ip4", host)
	if err != nil {
		return nil, err
	}
	for i := range ips {
		ips[i] = ips[i].Unmap()
	}
	return ips, nil
}

func (s system) LookupTXT(ctx context.Context, name string) ([]string, error) {
	return s.r.LookupTXT(ctx, name)
}
