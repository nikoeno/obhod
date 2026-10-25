package fakeasn

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/netip"
	"strings"
	"testing"
	"time"

	"github.com/nikoejento/obhod/internal/lib/analyzer"
)

var testMappings = []Mapping{
	{Prefix: netip.MustParsePrefix("203.0.113.0/24"), ASN: 64500, Name: "LAB-ISP-A"},
	{Prefix: netip.MustParsePrefix("203.0.113.128/25"), ASN: 64502, Name: "LAB-ISP-A-SUB"},
	{Prefix: netip.MustParsePrefix("198.51.100.0/24"), ASN: 64501, Name: "LAB-ISP-B"},
	{Prefix: netip.MustParsePrefix("2001:db8:a::/48"), ASN: 64500, Name: "LAB-ISP-A"},
}

// txtResolver is an analyzer.Resolver backed by TXT.
type txtResolver struct{ m []Mapping }

func (r txtResolver) LookupIP(context.Context, string) ([]netip.Addr, error) {
	return nil, errors.New("not supported")
}

func (r txtResolver) LookupTXT(_ context.Context, name string) ([]string, error) {
	if txt := TXT(r.m, name); txt != nil {
		return txt, nil
	}
	return nil, errors.New("NXDOMAIN")
}

func TestEchoLoopback(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	s, err := Run(ctx, "127.0.0.1:0", testMappings)
	if err != nil {
		t.Fatal(err)
	}
	if !s.Echo.Addr().Is4() || s.Echo.Port() == 0 {
		t.Fatalf("Echo = %v", s.Echo)
	}
	c := &http.Client{Timeout: 3 * time.Second}
	resp, err := c.Get("http://" + s.Echo.String() + "/")
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 257))
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK || len(body) > 256 {
		t.Fatalf("status %d, %d bytes", resp.StatusCode, len(body))
	}
	if got := strings.TrimSpace(string(body)); got != "127.0.0.1" {
		t.Fatalf("echo = %q", got)
	}
	cancel()
	deadline := time.Now().Add(3 * time.Second)
	for {
		c.CloseIdleConnections()
		if _, err := c.Get("http://" + s.Echo.String() + "/"); err != nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("echo still serving after ctx was cancelled")
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func TestRunListenError(t *testing.T) {
	if _, err := Run(context.Background(), "not-an-address", nil); err == nil {
		t.Fatal("want a listen error")
	}
}

func TestLookupASNThroughParser(t *testing.T) {
	r := txtResolver{testMappings}
	cases := []struct {
		ip     string
		asn    uint32
		name   string
		prefix string
	}{
		{"203.0.113.7", 64500, "LAB-ISP-A", "203.0.113.0/24"},
		{"203.0.113.200", 64502, "LAB-ISP-A-SUB", "203.0.113.128/25"}, // longest prefix wins
		{"198.51.100.1", 64501, "LAB-ISP-B", "198.51.100.0/24"},
		{"2001:db8:a:1::42", 64500, "LAB-ISP-A", "2001:db8:a::/48"},
	}
	for _, c := range cases {
		a, err := analyzer.LookupASN(context.Background(), r, netip.MustParseAddr(c.ip))
		if err != nil {
			t.Fatalf("%s: %v", c.ip, err)
		}
		if a.Number != c.asn || a.Name != c.name || a.Country != "ZZ" || a.Prefix != netip.MustParsePrefix(c.prefix) {
			t.Fatalf("%s: got %+v", c.ip, a)
		}
	}
	if a, err := analyzer.LookupASN(context.Background(), r, netip.MustParseAddr("192.0.2.1")); err == nil {
		t.Fatalf("unmapped address resolved: %+v", a)
	}
	if a, err := analyzer.LookupASN(context.Background(), r, netip.MustParseAddr("2001:db8:b::1")); err == nil {
		t.Fatalf("unmapped IPv6 address resolved: %+v", a)
	}
}

func TestTXTNames(t *testing.T) {
	cases := map[string]bool{
		"7.113.0.203.origin.asn.cymru.com.":  true,
		"7.113.0.203.ORIGIN.ASN.CYMRU.COM":   true,
		"AS64501.asn.cymru.com":              true,
		"as64501.asn.cymru.com.":             true,
		"AS1.asn.cymru.com":                  false,
		"113.0.203.origin.asn.cymru.com":     false,
		"300.113.0.203.origin.asn.cymru.com": false,
		"1.2.origin6.asn.cymru.com":          false,
		"example.com":                        false,
		"":                                   false,
	}
	for name, want := range cases {
		if got := TXT(testMappings, name); (got != nil) != want {
			t.Errorf("TXT(%q) = %q, want answer: %v", name, got, want)
		}
	}
	if got := TXT(testMappings, "AS64501.asn.cymru.com"); len(got) != 1 || got[0] != "64501 | ZZ | lab | 2026-01-01 | LAB-ISP-B" {
		t.Fatalf("AS record = %q", got)
	}
	if got := TXT(testMappings, "7.113.0.203.origin.asn.cymru.com"); len(got) != 1 || got[0] != "64500 | 203.0.113.0/24 | ZZ | lab | 2026-01-01" {
		t.Fatalf("origin record = %q", got)
	}
}
