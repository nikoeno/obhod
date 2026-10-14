package fakedns

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"io"
	"math/big"
	"net"
	"net/http"
	"net/netip"
	"strings"
	"testing"
	"time"

	"github.com/miekg/dns"
)

// selfSigned returns a certificate for 127.0.0.1 and a pool that trusts it.
func selfSigned(t *testing.T) (tls.Certificate, *x509.CertPool) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "fakedns test"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(time.Hour),
		IPAddresses:           []net.IP{net.IPv4(127, 0, 0, 1)},
		KeyUsage:              x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
		IsCA:                  true,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	leaf, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	pool := x509.NewCertPool()
	pool.AddCert(leaf)
	return tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key, Leaf: leaf}, pool
}

var (
	siteV4 = netip.MustParseAddr("203.0.113.10")
	siteV6 = netip.MustParseAddr("2001:db8::10")
	stub   = netip.MustParseAddr("198.51.100.66")
)

func testConfig() Config {
	return Config{
		Zone: map[string][]netip.Addr{
			"site.lab":     {siteV4, siteV6},
			"Blocked.Lab.": {netip.MustParseAddr("203.0.113.20")},
		},
		Poisoned: map[string]netip.Addr{"blocked.lab": stub},
		TXT: func(name string) []string {
			if name == "7.113.0.203.origin.asn.cymru.com" {
				return []string{"64500 | 203.0.113.0/24 | ZZ | lab | 2026-01-01", strings.Repeat("x", 300)}
			}
			return nil
		},
	}
}

// dohClient is a minimal RFC 8484 client trusting pool.
type dohClient struct {
	url string
	c   *http.Client
}

func newDoHClient(t *testing.T, s *Server, pool *x509.CertPool) *dohClient {
	tr := &http.Transport{TLSClientConfig: &tls.Config{RootCAs: pool}}
	t.Cleanup(tr.CloseIdleConnections)
	return &dohClient{url: "https://" + s.Addr.String() + "/dns-query", c: &http.Client{Transport: tr, Timeout: 5 * time.Second}}
}

func (d *dohClient) query(t *testing.T, get bool, name string, qtype uint16) *dns.Msg {
	t.Helper()
	q := new(dns.Msg)
	q.SetQuestion(dns.Fqdn(name), qtype)
	q.Id = 0
	wire, err := q.Pack()
	if err != nil {
		t.Fatal(err)
	}
	var req *http.Request
	if get {
		req, err = http.NewRequest(http.MethodGet, d.url+"?dns="+base64.RawURLEncoding.EncodeToString(wire), nil)
	} else {
		req, err = http.NewRequest(http.MethodPost, d.url, bytes.NewReader(wire))
		req.Header.Set("Content-Type", "application/dns-message")
	}
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Accept", "application/dns-message")
	resp, err := d.c.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK || resp.Header.Get("Content-Type") != "application/dns-message" {
		t.Fatalf("status %d, type %q", resp.StatusCode, resp.Header.Get("Content-Type"))
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	m := new(dns.Msg)
	if err := m.Unpack(body); err != nil {
		t.Fatal(err)
	}
	return m
}

func startDoH(t *testing.T, cfg Config) (*Server, *dohClient) {
	t.Helper()
	cert, pool := selfSigned(t)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	s, err := RunDoH(ctx, "127.0.0.1:0", cert, cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.Close)
	return s, newDoHClient(t, s, pool)
}

func startPlain(t *testing.T, cfg Config) *Server {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	s, err := RunPlain(ctx, "127.0.0.1:0", cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.Close)
	return s
}

func addrs(m *dns.Msg) []netip.Addr {
	var out []netip.Addr
	for _, rr := range m.Answer {
		switch r := rr.(type) {
		case *dns.A:
			a, _ := netip.AddrFromSlice(r.A)
			out = append(out, a.Unmap())
		case *dns.AAAA:
			a, _ := netip.AddrFromSlice(r.AAAA)
			out = append(out, a)
		}
	}
	return out
}

func wantAddrs(t *testing.T, m *dns.Msg, want ...netip.Addr) {
	t.Helper()
	got := addrs(m)
	if m.Rcode != dns.RcodeSuccess || len(got) != len(want) {
		t.Fatalf("rcode %s, answers %v, want %v", dns.RcodeToString[m.Rcode], got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("answers %v, want %v", got, want)
		}
	}
}

func TestDoHZone(t *testing.T) {
	s, c := startDoH(t, testConfig())
	for _, get := range []bool{true, false} {
		m := c.query(t, get, "site.lab", dns.TypeA)
		wantAddrs(t, m, siteV4)
		if m.Answer[0].Header().Ttl != 60 {
			t.Fatalf("TTL %d", m.Answer[0].Header().Ttl)
		}
		wantAddrs(t, c.query(t, get, "SITE.lab.", dns.TypeAAAA), siteV6)
		// Poisoning never applies over DoH.
		wantAddrs(t, c.query(t, get, "blocked.lab", dns.TypeA), netip.MustParseAddr("203.0.113.20"))
		if m := c.query(t, get, "missing.lab", dns.TypeA); m.Rcode != dns.RcodeNameError {
			t.Fatalf("missing.lab rcode %s", dns.RcodeToString[m.Rcode])
		}
		if m := c.query(t, get, "sub.blocked.lab", dns.TypeA); m.Rcode != dns.RcodeNameError {
			t.Fatalf("sub.blocked.lab over DoH rcode %s", dns.RcodeToString[m.Rcode])
		}
	}
	q := s.Queries()
	if len(q) != 10 {
		t.Fatalf("%d queries logged: %+v", len(q), q)
	}
	if q[0].Name != "site.lab" || q[0].Type != "A" || q[0].Via != "doh" || q[0].From.Addr() != netip.MustParseAddr("127.0.0.1") || q[0].At.IsZero() {
		t.Fatalf("first query %+v", q[0])
	}
	if q[1].Name != "site.lab" || q[1].Type != "AAAA" {
		t.Fatalf("second query %+v (name must be normalized)", q[1])
	}
}

func TestDoHTXTAndOtherTypes(t *testing.T) {
	s, c := startDoH(t, testConfig())
	m := c.query(t, false, "7.113.0.203.origin.asn.cymru.com", dns.TypeTXT)
	if m.Rcode != dns.RcodeSuccess || len(m.Answer) != 2 {
		t.Fatalf("TXT answer %v", m)
	}
	if got := strings.Join(m.Answer[0].(*dns.TXT).Txt, ""); got != "64500 | 203.0.113.0/24 | ZZ | lab | 2026-01-01" {
		t.Fatalf("TXT %q", got)
	}
	if got := strings.Join(m.Answer[1].(*dns.TXT).Txt, ""); got != strings.Repeat("x", 300) {
		t.Fatalf("long TXT has %d bytes", len(got))
	}
	if m := c.query(t, true, "1.2.3.4.origin.asn.cymru.com", dns.TypeTXT); m.Rcode != dns.RcodeNameError {
		t.Fatalf("unknown TXT rcode %s", dns.RcodeToString[m.Rcode])
	}
	if m := c.query(t, true, "site.lab", dns.TypeTXT); m.Rcode != dns.RcodeSuccess || len(m.Answer) != 0 {
		t.Fatalf("zone name without TXT: %v", m)
	}
	for _, typ := range []uint16{dns.TypeHTTPS, dns.TypeSVCB} {
		for _, name := range []string{"site.lab", "missing.lab"} {
			if m := c.query(t, true, name, typ); m.Rcode != dns.RcodeSuccess || len(m.Answer) != 0 {
				t.Fatalf("%s %s: %v", name, dns.TypeToString[typ], m)
			}
		}
	}
	types := map[string]int{}
	for _, q := range s.Queries() {
		types[q.Type]++
	}
	if types["TXT"] != 3 || types["HTTPS"] != 2 || types["SVCB"] != 2 {
		t.Fatalf("logged types %v", types)
	}
}

func TestDoHNilTXTHook(t *testing.T) {
	cfg := testConfig()
	cfg.TXT = nil
	_, c := startDoH(t, cfg)
	if m := c.query(t, false, "7.113.0.203.origin.asn.cymru.com", dns.TypeTXT); m.Rcode != dns.RcodeNameError {
		t.Fatalf("rcode %s", dns.RcodeToString[m.Rcode])
	}
}

func TestDoHBadRequests(t *testing.T) {
	s, c := startDoH(t, testConfig())
	for _, r := range []struct {
		method, query, ctype string
		want                 int
	}{
		{http.MethodGet, "?dns=!!!", "", http.StatusBadRequest},
		{http.MethodGet, "", "", http.StatusBadRequest},
		{http.MethodPost, "", "text/plain", http.StatusUnsupportedMediaType},
		{http.MethodPut, "", "", http.StatusMethodNotAllowed},
	} {
		req, _ := http.NewRequest(r.method, c.url+r.query, strings.NewReader("x"))
		if r.ctype != "" {
			req.Header.Set("Content-Type", r.ctype)
		}
		resp, err := c.c.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != r.want {
			t.Fatalf("%s %q: status %d, want %d", r.method, r.query, resp.StatusCode, r.want)
		}
	}
	if n := len(s.Queries()); n != 0 {
		t.Fatalf("%d queries logged for bad requests", n)
	}
}

func exchange(t *testing.T, network string, s *Server, name string, qtype uint16) *dns.Msg {
	t.Helper()
	q := new(dns.Msg)
	q.SetQuestion(dns.Fqdn(name), qtype)
	c := &dns.Client{Net: network, Timeout: 3 * time.Second}
	m, _, err := c.Exchange(q, s.Addr.String())
	if err != nil {
		t.Fatalf("%s %s: %v", network, name, err)
	}
	return m
}

func TestPlainUDPAndTCP(t *testing.T) {
	s := startPlain(t, testConfig())
	if !s.Addr.Addr().Is4() || s.Addr.Port() == 0 {
		t.Fatalf("Addr = %v", s.Addr)
	}
	for _, network := range []string{"udp", "tcp"} {
		wantAddrs(t, exchange(t, network, s, "site.lab", dns.TypeA), siteV4)
		wantAddrs(t, exchange(t, network, s, "site.lab", dns.TypeAAAA), siteV6)
		if m := exchange(t, network, s, "missing.lab", dns.TypeA); m.Rcode != dns.RcodeNameError {
			t.Fatalf("%s missing.lab rcode %s", network, dns.RcodeToString[m.Rcode])
		}
		// Poisoning: exact and suffix match, even outside the zone; AAAA gets no IPv4 stub.
		wantAddrs(t, exchange(t, network, s, "blocked.lab", dns.TypeA), stub)
		wantAddrs(t, exchange(t, network, s, "www.blocked.lab", dns.TypeA), stub)
		wantAddrs(t, exchange(t, network, s, "blocked.lab", dns.TypeAAAA))
		if m := exchange(t, network, s, "notblocked.lab", dns.TypeA); m.Rcode != dns.RcodeNameError {
			t.Fatalf("suffix match must respect labels, got %v", m)
		}
		if m := exchange(t, network, s, "7.113.0.203.origin.asn.cymru.com", dns.TypeTXT); m.Rcode != dns.RcodeSuccess || len(m.Answer) != 2 {
			t.Fatalf("%s TXT: %v", network, m)
		}
	}
	q := s.Queries()
	if len(q) != 16 {
		t.Fatalf("%d queries logged", len(q))
	}
	for i, want := range []string{"udp", "tcp"} {
		got := q[i*8]
		if got.Via != want || got.Name != "site.lab" || got.Type != "A" || got.From.Addr() != netip.MustParseAddr("127.0.0.1") || got.From.Port() == 0 {
			t.Fatalf("query %d = %+v, want via %s", i*8, got, want)
		}
	}
	if q[4].Name != "www.blocked.lab" || q[4].Via != "udp" {
		t.Fatalf("poisoned query not logged: %+v", q[4])
	}
}

func TestPlainFixedPort(t *testing.T) {
	first := startPlain(t, Config{})
	// The same address is taken now; a fixed port must fail instead of moving.
	if s, err := RunPlain(context.Background(), first.Addr.String(), Config{}); err == nil {
		s.Close()
		t.Fatal("bound an address in use")
	}
}

func TestSetZone(t *testing.T) {
	s := startPlain(t, testConfig())
	added := netip.MustParseAddr("203.0.113.99")
	s.SetZone(map[string][]netip.Addr{"New.Lab.": {added}, "bad..name": {added}})
	wantAddrs(t, exchange(t, "udp", s, "new.lab", dns.TypeA), added)
	if m := exchange(t, "udp", s, "site.lab", dns.TypeA); m.Rcode != dns.RcodeNameError {
		t.Fatalf("old zone still answers: %v", m)
	}
	// Poisoning is not part of the zone and survives the swap.
	wantAddrs(t, exchange(t, "tcp", s, "blocked.lab", dns.TypeA), stub)
}

func TestInvalidConfig(t *testing.T) {
	cert, _ := selfSigned(t)
	bad := []Config{
		{Zone: map[string][]netip.Addr{"": {siteV4}}},
		{Zone: map[string][]netip.Addr{"a..b": {siteV4}}},
		{Zone: map[string][]netip.Addr{"ok.lab": {{}}}},
		{Poisoned: map[string]netip.Addr{"ok.lab": {}}},
	}
	for i, cfg := range bad {
		if s, err := RunPlain(context.Background(), "127.0.0.1:0", cfg); err == nil {
			s.Close()
			t.Errorf("config %d: RunPlain accepted it", i)
		}
		if i < 3 {
			if s, err := RunDoH(context.Background(), "127.0.0.1:0", cert, cfg); err == nil {
				s.Close()
				t.Errorf("config %d: RunDoH accepted it", i)
			}
		}
	}
	if _, err := RunPlain(context.Background(), "no-port", Config{}); err == nil {
		t.Error("RunPlain accepted an address without a port")
	}
	if _, err := RunDoH(context.Background(), "no-port", cert, Config{}); err == nil {
		t.Error("RunDoH accepted an address without a port")
	}
}

func TestCustomTTL(t *testing.T) {
	cfg := testConfig()
	cfg.TTL = 5
	s := startPlain(t, cfg)
	if m := exchange(t, "udp", s, "site.lab", dns.TypeA); m.Answer[0].Header().Ttl != 5 {
		t.Fatalf("TTL %d", m.Answer[0].Header().Ttl)
	}
}

func TestCloseIdempotentAndCtx(t *testing.T) {
	cert, pool := selfSigned(t)
	ctx, cancel := context.WithCancel(context.Background())
	doh, err := RunDoH(ctx, "127.0.0.1:0", cert, testConfig())
	if err != nil {
		t.Fatal(err)
	}
	plain, err := RunPlain(ctx, "127.0.0.1:0", testConfig())
	if err != nil {
		t.Fatal(err)
	}
	c := newDoHClient(t, doh, pool)
	c.query(t, true, "site.lab", dns.TypeA)
	exchange(t, "tcp", plain, "site.lab", dns.TypeA)

	plain.Close()
	plain.Close()
	if _, err := net.DialTimeout("tcp", plain.Addr.String(), time.Second); err == nil {
		t.Fatal("TCP still accepting after Close")
	}
	cancel()
	deadline := time.Now().Add(3 * time.Second)
	for {
		c.c.CloseIdleConnections()
		conn, err := net.DialTimeout("tcp", doh.Addr.String(), time.Second)
		if err != nil {
			break
		}
		conn.Close()
		if time.Now().After(deadline) {
			t.Fatal("DoH still accepting after ctx was cancelled")
		}
		time.Sleep(20 * time.Millisecond)
	}
	doh.Close()
	doh.Close()
	if n := len(plain.Queries()); n != 1 {
		t.Fatalf("Queries after Close = %d", n)
	}
}
