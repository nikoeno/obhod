package dnscheck

import (
	"bufio"
	"context"
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strings"
	"time"
)

// Learn asks ip on plain HTTP (port 80) for host and, if the answer is a redirect to a foreign host
// (the typical ISP block page), records that host and ip in stubs. It reports whether a stub was learned.
// Use it with an address that a system resolver returned for a domain DoH resolves differently, or for a canary domain.
func Learn(ctx context.Context, dial DialFunc, host string, ip netip.Addr, stubs *Stubs) (bool, error) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	conn, err := dial(ctx, "tcp", net.JoinHostPort(ip.String(), "80"))
	if err != nil {
		return false, err
	}
	defer conn.Close()
	if dl, ok := ctx.Deadline(); ok {
		conn.SetDeadline(dl)
	}
	if _, err := fmt.Fprintf(conn, "GET / HTTP/1.1\r\nHost: %s\r\nUser-Agent: Mozilla/5.0\r\nAccept: */*\r\nConnection: close\r\n\r\n", host); err != nil {
		return false, err
	}
	resp, err := http.ReadResponse(bufio.NewReader(conn), &http.Request{Method: http.MethodGet})
	if err != nil {
		return false, err
	}
	resp.Body.Close()
	if resp.StatusCode < 300 || resp.StatusCode >= 400 {
		return false, nil
	}
	u, err := url.Parse(resp.Header.Get("Location"))
	if err != nil || u.Hostname() == "" {
		return false, nil
	}
	loc := strings.ToLower(u.Hostname())
	if sameSite(loc, host) {
		return false, nil // an ordinary http→https or www redirect
	}
	stubs.AddHost(loc)
	stubs.AddIP(ip)
	return true, nil
}

// sameSite reports whether a and b share their last two labels (example.com vs www.example.com).
func sameSite(a, b string) bool {
	tail := func(s string) string {
		p := strings.Split(strings.TrimSuffix(strings.ToLower(s), "."), ".")
		if len(p) > 2 {
			p = p[len(p)-2:]
		}
		return strings.Join(p, ".")
	}
	return tail(a) == tail(b)
}
