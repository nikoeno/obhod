package dnscheck

import (
	"bytes"
	"context"
	"crypto/x509"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"time"

	"github.com/miekg/dns"
	utls "github.com/refraction-networking/utls"
)

type doh struct {
	endpoints []string
	client    *http.Client
}

// NewDoH returns a resolver that speaks DNS-over-HTTPS (RFC 8484, POST) to the endpoints in order, using
// dial to reach them and the given uTLS fingerprint ("chrome", "firefox", "safari" or "go").
// Endpoints should use IP addresses (https://1.1.1.1/dns-query) so no system lookup is needed to reach them.
func NewDoH(endpoints []string, dial DialFunc, fingerprint string) (Resolver, error) {
	return newDoH(endpoints, dial, fingerprint, nil)
}

func newDoH(endpoints []string, dial DialFunc, fingerprint string, roots *x509.CertPool) (Resolver, error) {
	if len(endpoints) == 0 {
		return nil, errors.New("dnscheck: no DoH endpoints")
	}
	if dial == nil {
		return nil, errors.New("dnscheck: nil dial function")
	}
	for _, e := range endpoints {
		u, err := url.Parse(e)
		if err != nil || u.Scheme != "https" || u.Host == "" {
			return nil, fmt.Errorf("dnscheck: bad DoH endpoint %q", e)
		}
	}
	id := helloID(fingerprint)
	tr := &http.Transport{
		DialTLSContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
			raw, err := dial(ctx, network, addr)
			if err != nil {
				return nil, err
			}
			host, _, _ := net.SplitHostPort(addr)
			uc := utls.UClient(raw, &utls.Config{ServerName: host, RootCAs: roots, NextProtos: []string{"http/1.1"}}, id)
			if err := uc.BuildHandshakeState(); err != nil {
				raw.Close()
				return nil, err
			}
			for _, e := range uc.Extensions {
				if a, ok := e.(*utls.ALPNExtension); ok {
					a.AlpnProtocols = []string{"http/1.1"}
				}
			}
			if err := uc.HandshakeContext(ctx); err != nil {
				raw.Close()
				return nil, err
			}
			return uc, nil
		},
		MaxIdleConns:    2,
		IdleConnTimeout: 30 * time.Second,
	}
	return &doh{endpoints: append([]string(nil), endpoints...), client: &http.Client{Transport: tr, Timeout: 10 * time.Second}}, nil
}

func helloID(fp string) utls.ClientHelloID {
	switch fp {
	case "firefox":
		return utls.HelloFirefox_Auto
	case "safari":
		return utls.HelloSafari_Auto
	case "go":
		return utls.HelloGolang
	}
	return utls.HelloChrome_Auto
}

// exchange sends one query to the endpoints in turn and returns the first usable answer.
func (d *doh) exchange(ctx context.Context, name string, qtype uint16) (*dns.Msg, error) {
	q := new(dns.Msg)
	q.SetQuestion(dns.Fqdn(name), qtype)
	q.Id = 0 // RFC 8484 §4.1: cache friendliness
	wire, err := q.Pack()
	if err != nil {
		return nil, err
	}
	var errs []error
	for _, ep := range d.endpoints {
		resp, err := d.query(ctx, ep, wire)
		if err == nil {
			return resp, nil
		}
		errs = append(errs, fmt.Errorf("doh %s: %w", ep, err))
		if ctx.Err() != nil {
			break
		}
	}
	return nil, errors.Join(errs...)
}

func (d *doh) query(ctx context.Context, endpoint string, wire []byte) (*dns.Msg, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(wire))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/dns-message")
	req.Header.Set("Accept", "application/dns-message")
	resp, err := d.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("HTTP status %d", resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	if err != nil {
		return nil, err
	}
	m := new(dns.Msg)
	if err := m.Unpack(body); err != nil {
		return nil, fmt.Errorf("bad DNS message: %w", err)
	}
	return m, nil
}

func (d *doh) LookupIP(ctx context.Context, host string) ([]netip.Addr, error) {
	m, err := d.exchange(ctx, host, dns.TypeA)
	if err != nil {
		return nil, err
	}
	switch m.Rcode {
	case dns.RcodeSuccess:
	case dns.RcodeNameError:
		return nil, fmt.Errorf("lookup %s: %w", host, ErrNXDomain)
	default:
		return nil, fmt.Errorf("lookup %s: %s", host, dns.RcodeToString[m.Rcode])
	}
	var out []netip.Addr
	for _, rr := range m.Answer {
		if a, ok := rr.(*dns.A); ok {
			if ip, ok := netip.AddrFromSlice(a.A); ok {
				out = append(out, ip.Unmap())
			}
		}
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("lookup %s: no A records", host)
	}
	return out, nil
}

func (d *doh) LookupTXT(ctx context.Context, name string) ([]string, error) {
	m, err := d.exchange(ctx, name, dns.TypeTXT)
	if err != nil {
		return nil, err
	}
	switch m.Rcode {
	case dns.RcodeSuccess:
	case dns.RcodeNameError:
		return nil, fmt.Errorf("lookup %s: %w", name, ErrNXDomain)
	default:
		return nil, fmt.Errorf("lookup %s: %s", name, dns.RcodeToString[m.Rcode])
	}
	var out []string
	for _, rr := range m.Answer {
		if t, ok := rr.(*dns.TXT); ok {
			s := ""
			for _, p := range t.Txt {
				s += p
			}
			out = append(out, s)
		}
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("lookup %s: no TXT records", name)
	}
	return out, nil
}
