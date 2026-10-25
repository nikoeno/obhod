package fakeasn

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"strconv"
	"strings"
	"time"
)

// Mapping assigns a client source prefix to a fake ISP.
type Mapping struct {
	Prefix netip.Prefix // client source prefix → ISP
	ASN    uint32
	Name   string
}

// Server is a running public-IP echo.
type Server struct {
	Echo netip.AddrPort // HTTP public-IP echo
}

// The fixed fields of every answer, in Team Cymru's layout.
const (
	country   = "ZZ"
	registry  = "lab"
	allocated = "2026-01-01"
)

// Run starts the echo server on echoListen. It stops when ctx is done.
// The listener exists when Run returns. The echo does not need the mappings (it reports the source
// address as seen); m is accepted so the lab configures both halves of the module from one list.
func Run(ctx context.Context, echoListen string, m []Mapping) (*Server, error) {
	_ = m
	ln, err := net.Listen("tcp", echoListen)
	if err != nil {
		return nil, fmt.Errorf("fakeasn: listen: %w", err)
	}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /", echo)
	srv := &http.Server{Handler: mux, ReadHeaderTimeout: 5 * time.Second}
	go srv.Serve(ln)
	go func() {
		<-ctx.Done()
		srv.Close()
		ln.Close() // Serve may not have tracked ln yet
	}()
	ap, err := netip.ParseAddrPort(ln.Addr().String())
	if err != nil {
		srv.Close()
		return nil, fmt.Errorf("fakeasn: listener address: %w", err)
	}
	return &Server{Echo: netip.AddrPortFrom(ap.Addr().Unmap(), ap.Port())}, nil
}

// echo writes the caller's source address as plain text.
func echo(w http.ResponseWriter, r *http.Request) {
	ap, err := netip.ParseAddrPort(r.RemoteAddr)
	if err != nil {
		http.Error(w, "bad remote address", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	fmt.Fprint(w, ap.Addr().Unmap().WithZone("").String())
}

// TXT answers Cymru-style names (<reversed ip>.origin.asn.cymru.com, origin6, AS<n>.asn.cymru.com)
// from the mappings; nil for any other name. It fits fakedns.TXTFunc.
// Names are matched case-insensitively, with or without a trailing dot. When several prefixes
// contain an address, the longest one wins.
func TXT(m []Mapping, name string) []string {
	name = strings.TrimSuffix(strings.ToLower(name), ".")
	switch {
	case strings.HasSuffix(name, ".origin.asn.cymru.com"):
		ip, ok := parseV4(strings.TrimSuffix(name, ".origin.asn.cymru.com"))
		if !ok {
			return nil
		}
		return origin(m, ip)
	case strings.HasSuffix(name, ".origin6.asn.cymru.com"):
		ip, ok := parseV6(strings.TrimSuffix(name, ".origin6.asn.cymru.com"))
		if !ok {
			return nil
		}
		return origin(m, ip)
	case strings.HasPrefix(name, "as") && strings.HasSuffix(name, ".asn.cymru.com"):
		n, err := strconv.ParseUint(strings.TrimSuffix(strings.TrimPrefix(name, "as"), ".asn.cymru.com"), 10, 32)
		if err != nil {
			return nil
		}
		for _, mp := range m {
			if mp.ASN == uint32(n) {
				return []string{fmt.Sprintf("%d | %s | %s | %s | %s", n, country, registry, allocated, mp.Name)}
			}
		}
	}
	return nil
}

// origin answers an origin(6) query for ip from the longest matching prefix.
func origin(m []Mapping, ip netip.Addr) []string {
	best := -1
	for i, mp := range m {
		if mp.Prefix.IsValid() && mp.Prefix.Contains(ip) && (best < 0 || mp.Prefix.Bits() > m[best].Prefix.Bits()) {
			best = i
		}
	}
	if best < 0 {
		return nil
	}
	mp := m[best]
	return []string{fmt.Sprintf("%d | %s | %s | %s | %s", mp.ASN, mp.Prefix.Masked(), country, registry, allocated)}
}

// parseV4 reads "d.c.b.a" (reversed octets) into a.b.c.d.
func parseV4(s string) (netip.Addr, bool) {
	labels := strings.Split(s, ".")
	if len(labels) != 4 {
		return netip.Addr{}, false
	}
	var b [4]byte
	for i, l := range labels {
		v, err := strconv.ParseUint(l, 10, 8)
		if err != nil {
			return netip.Addr{}, false
		}
		b[3-i] = byte(v)
	}
	return netip.AddrFrom4(b), true
}

// parseV6 reads 32 reversed nibble labels into an IPv6 address.
func parseV6(s string) (netip.Addr, bool) {
	labels := strings.Split(s, ".")
	if len(labels) != 32 {
		return netip.Addr{}, false
	}
	var b [16]byte
	for k, l := range labels {
		if len(l) != 1 {
			return netip.Addr{}, false
		}
		v, err := strconv.ParseUint(l, 16, 4)
		if err != nil {
			return netip.Addr{}, false
		}
		i := 15 - k/2
		if k%2 == 0 {
			b[i] |= byte(v)
		} else {
			b[i] |= byte(v) << 4
		}
	}
	return netip.AddrFrom16(b), true
}
