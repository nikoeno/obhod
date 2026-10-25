//go:build linux && lab

package lab

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"net/netip"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/miekg/dns"

	"github.com/nikoejento/obhod/internal/lib/faultserver"
	"github.com/nikoejento/obhod/lab/fakedns"
	"github.com/nikoejento/obhod/lab/fakedpi"
)

// TestHarness is the L5 smoke test: the topology, the fake TSPU, the resolvers, the ASN services
// and the VPS's proxy inbound, each proven by what the fake endpoints observed. It also checks that
// the root namespace is unchanged after teardown (rootSnapshot is in fakedpi_queue_test.go).
func TestHarness(t *testing.T) {
	if err := Preflight(); err != nil {
		t.Skip(err)
	}
	before := rootSnapshot(t)
	t.Cleanup(func() { before.compare(t, rootSnapshot(t)) }) // registered first: runs after the lab's teardown

	profiles, err := LoadProfiles("profiles")
	if err != nil {
		t.Fatal(err)
	}
	if len(profiles) < 2 || profiles[0].Name != "ispa" || profiles[1].Name != "ispb" {
		t.Fatalf("want profiles ispa and ispb first, got %+v", profiles)
	}
	if len(profiles[0].Poisoned) == 0 {
		t.Fatal("profile ispa poisons no name")
	}
	l := Up(t, profiles...)
	const host = "site-ok.test"
	site := l.AddSite(host, faultserver.Spec{Behavior: faultserver.OK, Bytes: 1024})
	ctx := context.Background()
	url := "https://" + host + "/"

	check := func(name string, fn func() error) {
		t.Helper()
		err := fn()
		note := "ok"
		if err != nil {
			note = err.Error()
			t.Errorf("%s: %v", name, err)
		}
		l.Report(name, err == nil, note)
	}
	curlFrom := func(want netip.Addr) error {
		n := len(site.Server.Conns())
		if _, err := l.Curl(ctx, "-o", "/dev/null", url); err != nil {
			return err
		}
		if !slices.ContainsFunc(site.Server.Conns()[n:], func(c faultserver.Conn) bool {
			return c.Remote.Addr() == want && c.SNI == host
		}) {
			return fmt.Errorf("site saw no connection from %v: %+v", want, site.Server.Conns()[n:])
		}
		return nil
	}

	check("direct via ispa: system resolver and site log", func() error {
		return curlFrom(l.ClientAddr("ispa"))
	})

	check("naive-sni on ispa resets curl", func() error {
		l.SetRules("ispa", fakedpi.Rule{Kind: fakedpi.NaiveSNI, Domains: []string{host}})
		defer l.SetRules("ispa")
		// X25519 only: a post-quantum key share would split the ClientHello over two segments, and
		// NaiveSNI by design only reads a hello that fits one.
		_, err := l.Curl(ctx, "--curves", "X25519", "-o", "/dev/null", url)
		if err == nil || !strings.Contains(strings.ToLower(err.Error()), "reset") {
			return fmt.Errorf("want a connection reset, got %v", err)
		}
		if !slices.ContainsFunc(l.DPI("ispa").Seen(), func(f fakedpi.Flow) bool {
			return f.SNI == host && f.Verdict == fakedpi.Reset && f.Rule == fakedpi.NaiveSNI
		}) {
			return fmt.Errorf("fakedpi has no reset flow for %s: %+v", host, l.DPI("ispa").Seen())
		}
		return nil
	})

	check("rules cleared: curl works again", func() error {
		return curlFrom(l.ClientAddr("ispa"))
	})

	check("ispa resolver poisons its listed name", func() error {
		for name, stub := range profiles[0].Poisoned {
			out, _, err := l.Exec(ctx, "getent", "hosts", name)
			if err != nil {
				return err
			}
			if f := strings.Fields(string(out)); len(f) == 0 || f[0] != stub.String() {
				return fmt.Errorf("getent hosts %s = %q, want %v", name, out, stub)
			}
		}
		return nil
	})

	l.UseISP("ispb")

	check("UseISP ispb: site sees ispb's address", func() error {
		return curlFrom(l.ClientAddr("ispb"))
	})

	check("DoH answers a site name and logs it", func() error {
		resp, err := dohQuery(ctx, l, host, dns.TypeA)
		if err != nil {
			return err
		}
		if !slices.ContainsFunc(resp.Answer, func(rr dns.RR) bool {
			a, ok := rr.(*dns.A)
			return ok && a.A.Equal(site.Addr.Addr().AsSlice())
		}) {
			return fmt.Errorf("answer %v, want %v", resp.Answer, site.Addr.Addr())
		}
		if !slices.ContainsFunc(l.DoH().Queries(), func(q fakedns.Query) bool {
			return q.Name == host && q.Type == "A" && q.Via == "doh" && q.From.Addr() == l.ClientAddr("ispb")
		}) {
			return fmt.Errorf("DoH log has no A query for %s from %v: %+v", host, l.ClientAddr("ispb"), l.DoH().Queries())
		}
		return nil
	})

	check("DoH answers the Cymru TXT for ispb", func() error {
		b := l.ClientAddr("ispb").As4()
		name := fmt.Sprintf("%d.%d.%d.%d.origin.asn.cymru.com", b[3], b[2], b[1], b[0])
		resp, err := dohQuery(ctx, l, name, dns.TypeTXT)
		if err != nil {
			return err
		}
		for _, rr := range resp.Answer {
			if txt, ok := rr.(*dns.TXT); ok && strings.HasPrefix(strings.Join(txt.Txt, ""), fmt.Sprint(profiles[1].ASN)+" ") {
				return nil
			}
		}
		return fmt.Errorf("TXT %s = %v, want ASN %d", name, resp.Answer, profiles[1].ASN)
	})

	check("ASN echo returns the active ISP's address", func() error {
		out, err := l.Curl(ctx, "http://"+echoAddr+"/")
		if err != nil {
			return err
		}
		if got := strings.TrimSpace(string(out)); got != l.ClientAddr("ispb").String() {
			return fmt.Errorf("echo = %q, want %v", got, l.ClientAddr("ispb"))
		}
		return nil
	})

	check("proxy route: SOCKS on the VPS", func() error {
		p := l.vpsP
		proxy := fmt.Sprintf("socks5h://%s:%s@%s", p.SOCKSUser, p.SOCKSPass, netip.AddrPortFrom(p.Addr, p.SOCKSPort))
		n := len(site.Server.Conns())
		if _, err := l.Curl(ctx, "--proxy", proxy, "-o", "/dev/null", url); err != nil {
			return err
		}
		if !slices.ContainsFunc(site.Server.Conns()[n:], func(c faultserver.Conn) bool {
			return c.Remote.Addr() == l.VPSAddr() && c.SNI == host
		}) {
			return fmt.Errorf("site saw no connection from the VPS %v: %+v", l.VPSAddr(), site.Server.Conns()[n:])
		}
		return nil
	})
}

// dohQuery sends one RFC 8484 GET to the lab DoH server from the client namespace.
func dohQuery(ctx context.Context, l *Lab, name string, qtype uint16) (*dns.Msg, error) {
	m := new(dns.Msg)
	m.SetQuestion(dns.Fqdn(name), qtype)
	m.Id = 0
	wire, err := m.Pack()
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	body, err := l.HTTPGet(ctx, "https://"+l.DoH().Addr.String()+"/dns-query?dns="+base64.RawURLEncoding.EncodeToString(wire))
	if err != nil {
		return nil, err
	}
	resp := new(dns.Msg)
	if err := resp.Unpack(body); err != nil {
		return nil, err
	}
	if resp.Rcode != dns.RcodeSuccess {
		return resp, errors.New("rcode " + dns.RcodeToString[resp.Rcode])
	}
	return resp, nil
}
