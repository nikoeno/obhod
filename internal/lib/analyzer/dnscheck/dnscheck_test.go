package dnscheck

import (
	"context"
	"crypto/x509"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"testing"

	"github.com/miekg/dns"

	"github.com/nikoejento/obhod/internal/lib/analyzer/lib/verdict"
)

func ips(s ...string) []netip.Addr {
	var out []netip.Addr
	for _, x := range s {
		out = append(out, netip.MustParseAddr(x))
	}
	return out
}

func TestCompare(t *testing.T) {
	stubs := NewStubs()
	stubs.AddIP(netip.MustParseAddr("203.0.113.66"))
	real := ips("93.184.216.34")
	cases := []struct {
		name   string
		sys    map[string][]netip.Addr
		doh    map[string][]netip.Addr
		want   verdict.Verdict
		fakeIP bool
		reason string
	}{
		{"same answers", map[string][]netip.Addr{"a.test": real}, map[string][]netip.Addr{"a.test": real}, verdict.OK, false, ""},
		{"cdn answers differ", map[string][]netip.Addr{"a.test": ips("151.101.1.1")}, map[string][]netip.Addr{"a.test": real}, verdict.OK, false, "differ"},
		{"known stub ip", map[string][]netip.Addr{"a.test": ips("203.0.113.66")}, map[string][]netip.Addr{"a.test": real}, verdict.DNSPoisoned, false, "stub"},
		{"private answer", map[string][]netip.Addr{"a.test": ips("10.0.0.1")}, map[string][]netip.Addr{"a.test": real}, verdict.DNSPoisoned, false, "bogon"},
		{"unspecified answer", map[string][]netip.Addr{"a.test": ips("0.0.0.0")}, map[string][]netip.Addr{"a.test": real}, verdict.DNSPoisoned, false, "bogon"},
		{"loopback answer", map[string][]netip.Addr{"a.test": ips("127.0.0.1")}, map[string][]netip.Addr{"a.test": real}, verdict.DNSPoisoned, false, "bogon"},
		{"nxdomain vs doh", nil, map[string][]netip.Addr{"a.test": real}, verdict.DNSPoisoned, false, "NXDOMAIN"},
		{"doh fails", map[string][]netip.Addr{"a.test": real}, nil, verdict.DNSFail, false, "DoH"},
		{"fakeip is informational", map[string][]netip.Addr{"a.test": ips("198.18.3.4")}, map[string][]netip.Addr{"a.test": real}, verdict.OK, true, "FakeIP"},
		{"both private (intranet)", map[string][]netip.Addr{"a.test": ips("10.0.0.1")}, map[string][]netip.Addr{"a.test": ips("10.0.0.1")}, verdict.OK, false, ""},
	}
	for _, c := range cases {
		got := Compare(context.Background(), "a.test", NewFake(c.sys, nil), NewFake(c.doh, nil), stubs)
		if got.Verdict != c.want || got.FakeIP != c.fakeIP || !strings.Contains(got.Reason, c.reason) {
			t.Errorf("%s: %+v", c.name, got)
		}
	}
	// A failing (non-NXDOMAIN) system resolver is not poisoning.
	got := Compare(context.Background(), "a.test", failing{errors.New("timeout")}, NewFake(map[string][]netip.Addr{"a.test": real}, nil), nil)
	if got.Verdict != verdict.OK || !strings.Contains(got.Reason, "system resolver failed") {
		t.Errorf("system failure: %+v", got)
	}
}

type failing struct{ err error }

func (f failing) LookupIP(context.Context, string) ([]netip.Addr, error) { return nil, f.err }
func (f failing) LookupTXT(context.Context, string) ([]string, error)    { return nil, f.err }

func TestStubs(t *testing.T) {
	s := NewStubs()
	s.AddHost("Block.ISP.example.")
	s.AddIP(netip.MustParseAddr("::ffff:1.2.3.4"))
	if !s.HasHost("block.isp.example") || s.HasHost("other") || !s.HasIP(netip.MustParseAddr("1.2.3.4")) || s.HasIP(netip.MustParseAddr("1.2.3.5")) {
		t.Fatal("lookup")
	}
	var nilStubs *Stubs
	if nilStubs.HasIP(netip.MustParseAddr("1.2.3.4")) || nilStubs.HasHost("x") || nilStubs.Hosts() != nil {
		t.Fatal("nil stubs must be empty")
	}
	if len(s.Hosts()) != 2 {
		t.Fatal(s.Hosts())
	}
}

func TestLookupASN(t *testing.T) {
	r := NewFake(nil, map[string][]string{
		"34.216.184.93.origin.asn.cymru.com": {"15133 | 93.184.216.0/24 | US | arin | 2008-06-02"},
		"as15133.asn.cymru.com":              {"15133 | US | arin | 2008-06-02 | EDGECAST, US"},
	})
	a, err := LookupASN(context.Background(), r, netip.MustParseAddr("93.184.216.34"))
	if err != nil {
		t.Fatal(err)
	}
	if a.Number != 15133 || a.Country != "US" || a.Name != "EDGECAST, US" || a.Prefix.String() != "93.184.216.0/24" {
		t.Fatalf("%+v", a)
	}
	if _, err := LookupASN(context.Background(), r, netip.MustParseAddr("8.8.8.8")); !errors.Is(err, ErrNoASN) {
		t.Fatalf("want ErrNoASN, got %v", err)
	}
	bad := NewFake(nil, map[string][]string{"1.1.1.1.origin.asn.cymru.com": {"garbage"}})
	if _, err := LookupASN(context.Background(), bad, netip.MustParseAddr("1.1.1.1")); !errors.Is(err, ErrNoASN) {
		t.Fatalf("garbage: %v", err)
	}
	if got := originName(netip.MustParseAddr("2001:db8::1")); got != "1.0.0.0.0.0.0.0.0.0.0.0.0.0.0.0.0.0.0.0.0.0.0.0.8.b.d.0.1.0.0.2.origin6.asn.cymru.com" {
		t.Fatalf("origin6: %s", got)
	}
}

// dohServer answers A and TXT queries from the given records.
func dohServer(t *testing.T, handler func(q dns.Question) (rcode int, rrs []dns.RR)) (*httptest.Server, *x509.CertPool) {
	t.Helper()
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.Header.Get("Content-Type") != "application/dns-message" {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		body, _ := io.ReadAll(r.Body)
		q := new(dns.Msg)
		if err := q.Unpack(body); err != nil || len(q.Question) != 1 {
			http.Error(w, "bad message", http.StatusBadRequest)
			return
		}
		resp := new(dns.Msg)
		resp.SetReply(q)
		rcode, rrs := handler(q.Question[0])
		resp.Rcode, resp.Answer = rcode, rrs
		out, _ := resp.Pack()
		w.Header().Set("Content-Type", "application/dns-message")
		w.Write(out)
	}))
	t.Cleanup(srv.Close)
	pool := x509.NewCertPool()
	pool.AddCert(srv.Certificate())
	return srv, pool
}

func TestDoH(t *testing.T) {
	srv, pool := dohServer(t, func(q dns.Question) (int, []dns.RR) {
		switch {
		case q.Name == "a.test." && q.Qtype == dns.TypeA:
			return dns.RcodeSuccess, []dns.RR{&dns.A{Hdr: dns.RR_Header{Name: q.Name, Rrtype: dns.TypeA, Class: dns.ClassINET}, A: net.ParseIP("192.0.2.5")}}
		case q.Name == "t.test." && q.Qtype == dns.TypeTXT:
			return dns.RcodeSuccess, []dns.RR{&dns.TXT{Hdr: dns.RR_Header{Name: q.Name, Rrtype: dns.TypeTXT, Class: dns.ClassINET}, Txt: []string{"hello ", "world"}}}
		case q.Name == "fail.test.":
			return dns.RcodeServerFailure, nil
		}
		return dns.RcodeNameError, nil
	})
	var d net.Dialer
	dial := func(ctx context.Context, network, addr string) (net.Conn, error) {
		return d.DialContext(ctx, network, addr)
	}
	r, err := newDoH([]string{srv.URL}, dial, "go", pool)
	if err != nil {
		t.Fatal(err)
	}
	got, err := r.LookupIP(context.Background(), "a.test")
	if err != nil || len(got) != 1 || got[0].String() != "192.0.2.5" {
		t.Fatalf("A: %v %v", got, err)
	}
	txt, err := r.LookupTXT(context.Background(), "t.test")
	if err != nil || len(txt) != 1 || txt[0] != "hello world" {
		t.Fatalf("TXT: %v %v", txt, err)
	}
	if _, err := r.LookupIP(context.Background(), "nx.test"); !errors.Is(err, ErrNXDomain) {
		t.Fatalf("NXDOMAIN: %v", err)
	}
	if _, err := r.LookupIP(context.Background(), "fail.test"); err == nil || errors.Is(err, ErrNXDomain) {
		t.Fatalf("SERVFAIL: %v", err)
	}

	// The certificate is verified: without the test root the lookup fails.
	strict, _ := newDoH([]string{srv.URL}, dial, "go", nil)
	if _, err := strict.LookupIP(context.Background(), "a.test"); err == nil {
		t.Fatal("untrusted DoH certificate accepted")
	}

	// A dead first endpoint falls through to the next; the error names the endpoint when all fail.
	dead := "https://127.0.0.1:1/dns-query"
	fb, _ := newDoH([]string{dead, srv.URL}, dial, "go", pool)
	if got, err := fb.LookupIP(context.Background(), "a.test"); err != nil || len(got) != 1 {
		t.Fatalf("fallback: %v %v", got, err)
	}
	all, _ := newDoH([]string{dead}, dial, "go", pool)
	if _, err := all.LookupIP(context.Background(), "a.test"); err == nil || !strings.Contains(err.Error(), dead) {
		t.Fatalf("error must name the endpoint: %v", err)
	}
}

func TestNewDoHValidation(t *testing.T) {
	dial := func(context.Context, string, string) (net.Conn, error) { return nil, errors.New("x") }
	if _, err := NewDoH(nil, dial, "chrome"); err == nil {
		t.Error("no endpoints")
	}
	if _, err := NewDoH([]string{"http://x/dns"}, dial, "chrome"); err == nil {
		t.Error("non-https")
	}
	if _, err := NewDoH([]string{"https://x/dns"}, nil, "chrome"); err == nil {
		t.Error("nil dial")
	}
}

func TestLearn(t *testing.T) {
	serve := func(resp string) DialFunc {
		return func(ctx context.Context, network, addr string) (net.Conn, error) {
			c, s := net.Pipe()
			go func() {
				defer s.Close()
				buf := make([]byte, 1024)
				s.Read(buf)
				io.WriteString(s, resp)
			}()
			return c, nil
		}
	}
	ip := netip.MustParseAddr("203.0.113.66")
	stubs := NewStubs()
	ok, err := Learn(context.Background(), serve("HTTP/1.1 302 Found\r\nLocation: http://block.isp.example/x\r\nContent-Length: 0\r\n\r\n"), "blocked.test", ip, stubs)
	if err != nil || !ok || !stubs.HasHost("block.isp.example") || !stubs.HasIP(ip) {
		t.Fatalf("stub not learned: %v %v", ok, err)
	}
	stubs = NewStubs()
	ok, _ = Learn(context.Background(), serve("HTTP/1.1 301 Moved\r\nLocation: https://www.blocked.test/\r\nContent-Length: 0\r\n\r\n"), "blocked.test", ip, stubs)
	if ok || stubs.HasIP(ip) {
		t.Fatal("ordinary redirect learned as stub")
	}
	ok, _ = Learn(context.Background(), serve("HTTP/1.1 200 OK\r\nContent-Length: 0\r\n\r\n"), "blocked.test", ip, stubs)
	if ok {
		t.Fatal("200 learned as stub")
	}
	if _, err := Learn(context.Background(), func(context.Context, string, string) (net.Conn, error) { return nil, errors.New("refused") }, "x.test", ip, stubs); err == nil {
		t.Fatal("dial error swallowed")
	}
}
