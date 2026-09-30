package netwatch

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"strings"
	"time"

	"github.com/nikoejento/obhod/internal/lib/netif"
)

const echoTimeout = 8 * time.Second

// echoLookup returns a PublicIP function that asks the endpoints (plain-text IP answers) in order, over a
// client whose sockets are pinned to the physical uplink, so the answer is the ISP's address and not a
// tunnel's. With no endpoints it always fails, which leaves gateway IDs as the only option.
func echoLookup(endpoints []string, uplink func(context.Context) (netif.Interface, error)) func(context.Context) (netip.Addr, error) {
	return func(ctx context.Context) (netip.Addr, error) {
		if len(endpoints) == 0 {
			return netip.Addr{}, errors.New("netwatch: no echo endpoints configured")
		}
		ifc, err := uplink(ctx)
		if err != nil {
			return netip.Addr{}, err
		}
		client := &http.Client{
			Timeout: echoTimeout,
			Transport: &http.Transport{
				DialContext:       (&net.Dialer{Timeout: echoTimeout, Control: netif.BindControl(ifc)}).DialContext,
				DisableKeepAlives: true,
				Proxy:             nil, // never through a system proxy: it may be the tunnel
			},
		}
		defer client.CloseIdleConnections()
		var lastErr error
		for _, ep := range endpoints {
			ip, err := fetchIP(ctx, client, ep)
			if err == nil {
				return ip, nil
			}
			lastErr = err
		}
		return netip.Addr{}, lastErr
	}
}

func fetchIP(ctx context.Context, c *http.Client, url string) (netip.Addr, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return netip.Addr{}, err
	}
	resp, err := c.Do(req)
	if err != nil {
		return netip.Addr{}, errors.New("netwatch: echo request failed") // the URL stays out of errors
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return netip.Addr{}, fmt.Errorf("netwatch: echo status %d", resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 256))
	if err != nil {
		return netip.Addr{}, err
	}
	ip, err := netip.ParseAddr(strings.TrimSpace(string(body)))
	if err != nil {
		return netip.Addr{}, errors.New("netwatch: echo answer is not an IP address")
	}
	return ip.Unmap(), nil
}
