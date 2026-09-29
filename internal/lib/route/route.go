package route

import (
	"errors"
	"fmt"
	"net/netip"
	"strings"
)

// Route is where a connection for a domain goes.
type Route string

// The known routes.
const (
	Direct Route = "direct" // not blocked, pass through
	Desync Route = "desync" // local DPI desync engine
	Tunnel Route = "tunnel" // encrypted tunnel to the VPS
	Proxy  Route = "proxy"  // separate SOCKS/HTTP proxy
	Block  Route = "block"  // drop
)

// ErrInvalidRoute is returned for an unknown route name.
var ErrInvalidRoute = errors.New("invalid route")

// ErrInvalidDomain is returned for a string that is not a plain domain name.
var ErrInvalidDomain = errors.New("invalid domain")

// All returns every route in stable order.
func All() []Route { return []Route{Direct, Desync, Tunnel, Proxy, Block} }

// Parse converts a route name to a Route.
func Parse(s string) (Route, error) {
	r := Route(strings.ToLower(strings.TrimSpace(s)))
	if !r.Valid() {
		return "", fmt.Errorf("%w: %q", ErrInvalidRoute, s)
	}
	return r, nil
}

// Valid reports whether r is a known route.
func (r Route) Valid() bool {
	for _, x := range All() {
		if r == x {
			return true
		}
	}
	return false
}

// Checkable reports whether the checker may change this route (direct, desync, tunnel).
func (r Route) Checkable() bool { return r == Direct || r == Desync || r == Tunnel }

// Normalize lowercases, trims a trailing dot and whitespace, converts IDN to punycode,
// and rejects IPs, wildcards, schemes, ports and paths.
func Normalize(domain string) (string, error) {
	d := strings.TrimSuffix(strings.ToLower(strings.TrimSpace(domain)), ".")
	bad := func(why string) (string, error) {
		return "", fmt.Errorf("%w: %q (%s)", ErrInvalidDomain, domain, why)
	}
	switch {
	case d == "":
		return bad("empty")
	case strings.Contains(d, "*"):
		return bad("wildcard")
	case strings.Contains(d, "://"):
		return bad("scheme")
	case strings.ContainsAny(d, "/?#"):
		return bad("path")
	case strings.Contains(d, ":"):
		return bad("port or IPv6")
	}
	if _, err := netip.ParseAddr(d); err == nil {
		return bad("IP address")
	}
	labels := strings.Split(d, ".")
	for i, l := range labels {
		if l == "" {
			return bad("empty label")
		}
		if !isASCII(l) {
			enc, err := punycode(l)
			if err != nil {
				return bad(err.Error())
			}
			l = "xn--" + enc
			labels[i] = l
		}
		if len(l) > 63 {
			return bad("label too long")
		}
		if l[0] == '-' || l[len(l)-1] == '-' {
			return bad("label starts or ends with hyphen")
		}
		for _, c := range []byte(l) {
			if !(c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '-' || c == '_') {
				return bad("invalid character")
			}
		}
	}
	d = strings.Join(labels, ".")
	if len(d) > 253 {
		return bad("too long")
	}
	return d, nil
}

func isASCII(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] >= 0x80 {
			return false
		}
	}
	return true
}

// punycode encodes one label per RFC 3492 (without the "xn--" prefix).
func punycode(label string) (string, error) {
	const (
		base, tmin, tmax, skew, damp = 36, 1, 26, 38, 700
	)
	rs := []rune(label)
	var out []byte
	for _, r := range rs {
		if r < 0x80 {
			out = append(out, byte(r))
		}
	}
	b := len(out)
	h := b
	if b > 0 {
		out = append(out, '-')
	}
	digit := func(d int) byte {
		if d < 26 {
			return byte('a' + d)
		}
		return byte('0' + d - 26)
	}
	adapt := func(delta, n int, first bool) int {
		if first {
			delta /= damp
		} else {
			delta /= 2
		}
		delta += delta / n
		k := 0
		for delta > ((base-tmin)*tmax)/2 {
			delta /= base - tmin
			k += base
		}
		return k + (base-tmin+1)*delta/(delta+skew)
	}
	n, delta, bias := 128, 0, 72
	for h < len(rs) {
		m := int(^uint32(0) >> 1)
		for _, r := range rs {
			if int(r) >= n && int(r) < m {
				m = int(r)
			}
		}
		delta += (m - n) * (h + 1)
		n = m
		for _, r := range rs {
			if int(r) < n {
				delta++
			}
			if int(r) == n {
				q := delta
				for k := base; ; k += base {
					t := k - bias
					if t < tmin {
						t = tmin
					} else if t > tmax {
						t = tmax
					}
					if q < t {
						break
					}
					out = append(out, digit(t+(q-t)%(base-t)))
					q = (q - t) / (base - t)
				}
				out = append(out, digit(q))
				bias = adapt(delta, h+1, h == b)
				delta = 0
				h++
			}
		}
		delta++
		n++
	}
	return string(out), nil
}
