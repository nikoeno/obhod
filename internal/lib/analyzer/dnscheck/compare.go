package dnscheck

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"strings"

	"github.com/nikoejento/obhod/internal/lib/analyzer/lib/verdict"
)

// ErrNXDomain is returned (wrapped) when the name does not exist.
var ErrNXDomain = errors.New("no such domain")

// Check is the result of comparing the system resolver with DoH.
type Check struct {
	Host    string
	System  []netip.Addr
	DoH     []netip.Addr
	Verdict verdict.Verdict // OK | DNS_POISONED | DNS_FAIL
	FakeIP  bool            // system answer in 198.18.0.0/15 (local TUN client)
	Reason  string
}

var (
	fakeIPRange = netip.MustParsePrefix("198.18.0.0/15")
	cgnat       = netip.MustParsePrefix("100.64.0.0/10")
	reserved    = netip.MustParsePrefix("240.0.0.0/4")
)

// isBogon reports whether ip can never be the real address of a public site. The FakeIP range is not a bogon here.
func isBogon(ip netip.Addr) bool {
	ip = ip.Unmap()
	if fakeIPRange.Contains(ip) {
		return false
	}
	return ip.IsUnspecified() || ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() ||
		ip.IsMulticast() || cgnat.Contains(ip) || reserved.Contains(ip)
}

func isNotFound(err error) bool {
	var de *net.DNSError
	if errors.Is(err, ErrNXDomain) || (errors.As(err, &de) && de.IsNotFound) {
		return true
	}
	return err != nil && strings.Contains(strings.ToLower(err.Error()), "no such host")
}

// Compare resolves host with both resolvers and classifies the difference.
//
// Different answers are not poisoning by themselves (CDNs differ). Poisoning is reported only when the
// system answer is a known stub, a bogon/private address, or NXDOMAIN while DoH resolves. FakeIP is informational.
func Compare(ctx context.Context, host string, sys, doh Resolver, stubs *Stubs) Check {
	c := Check{Host: host, Verdict: verdict.OK}
	dohIPs, dohErr := doh.LookupIP(ctx, host)
	if dohErr != nil {
		c.Verdict = verdict.DNSFail
		c.Reason = "DoH lookup failed: " + dohErr.Error()
		return c
	}
	c.DoH = dohIPs
	sysIPs, sysErr := sys.LookupIP(ctx, host)
	if sysErr != nil {
		if isNotFound(sysErr) {
			c.Verdict = verdict.DNSPoisoned
			c.Reason = "system resolver says NXDOMAIN while DoH resolves"
		} else {
			c.Reason = "system resolver failed: " + sysErr.Error()
		}
		return c
	}
	c.System = sysIPs

	dohBogus := false
	for _, ip := range dohIPs {
		dohBogus = dohBogus || isBogon(ip)
	}
	for _, ip := range sysIPs {
		switch {
		case fakeIPRange.Contains(ip.Unmap()):
			c.FakeIP = true
		case stubs.HasIP(ip):
			c.Verdict, c.Reason = verdict.DNSPoisoned, fmt.Sprintf("system answer %s is a known ISP stub", ip)
		case isBogon(ip) && !dohBogus:
			c.Verdict, c.Reason = verdict.DNSPoisoned, fmt.Sprintf("system answer %s is a bogon/private address", ip)
		}
		if c.Verdict == verdict.DNSPoisoned {
			return c
		}
	}
	switch {
	case c.FakeIP:
		c.Reason = "system answer is a FakeIP (local TUN client); informational"
	case !sameSet(sysIPs, dohIPs):
		c.Reason = "answers differ but both look routable (CDN or geo DNS)"
	}
	return c
}

func sameSet(a, b []netip.Addr) bool {
	m := map[netip.Addr]bool{}
	for _, x := range a {
		m[x.Unmap()] = true
	}
	if len(m) != len(b) {
		return false
	}
	for _, x := range b {
		if !m[x.Unmap()] {
			return false
		}
	}
	return true
}
