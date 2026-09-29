package dnscheck

import (
	"context"
	"errors"
	"fmt"
	"net/netip"
	"strconv"
	"strings"
)

// ErrNoASN is returned when no ASN data exists for the address.
var ErrNoASN = errors.New("no ASN found")

// ASN describes the origin AS of an address.
type ASN struct {
	Number  uint32       `json:"number"`
	Name    string       `json:"name,omitempty"`
	Country string       `json:"country,omitempty"`
	Prefix  netip.Prefix `json:"prefix,omitzero"`
}

// LookupASN queries Team Cymru (origin.asn.cymru.com + asn.cymru.com TXT) through r.
func LookupASN(ctx context.Context, r Resolver, ip netip.Addr) (ASN, error) {
	ip = ip.Unmap()
	if !ip.IsValid() {
		return ASN{}, ErrNoASN
	}
	txt, err := r.LookupTXT(ctx, originName(ip))
	if err != nil || len(txt) == 0 {
		return ASN{}, fmt.Errorf("%w: %v", ErrNoASN, err)
	}
	f := fields(txt[0])
	if len(f) < 3 {
		return ASN{}, fmt.Errorf("%w: unexpected origin record %q", ErrNoASN, txt[0])
	}
	// Multi-origin answers list several AS numbers separated by spaces; take the first.
	num, err := strconv.ParseUint(strings.Fields(f[0])[0], 10, 32)
	if err != nil {
		return ASN{}, fmt.Errorf("%w: bad AS number in %q", ErrNoASN, txt[0])
	}
	a := ASN{Number: uint32(num), Country: f[2]}
	a.Prefix, _ = netip.ParsePrefix(f[1])
	if names, err := r.LookupTXT(ctx, fmt.Sprintf("AS%d.asn.cymru.com", num)); err == nil && len(names) > 0 {
		if nf := fields(names[0]); len(nf) >= 5 {
			a.Name = nf[4]
			if a.Country == "" {
				a.Country = nf[1]
			}
		}
	}
	return a, nil
}

func fields(s string) []string {
	parts := strings.Split(s, "|")
	for i := range parts {
		parts[i] = strings.TrimSpace(parts[i])
	}
	return parts
}

func originName(ip netip.Addr) string {
	if ip.Is4() {
		b := ip.As4()
		return fmt.Sprintf("%d.%d.%d.%d.origin.asn.cymru.com", b[3], b[2], b[1], b[0])
	}
	b := ip.As16()
	var sb strings.Builder
	for i := 15; i >= 0; i-- {
		fmt.Fprintf(&sb, "%x.%x.", b[i]&0xf, b[i]>>4)
	}
	return sb.String() + "origin6.asn.cymru.com"
}
