package pc

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/netip"
	"os"
	"path/filepath"

	"github.com/nikoejento/obhod/internal/lib/paths"
	"github.com/nikoejento/obhod/internal/pc/singbox"
)

// loadSecrets reads the outbound definitions named in cfg from p.Secrets. An
// error names the file, never its content.
func loadSecrets(cfg SecretsConfig, p paths.Paths) (singbox.Outbounds, error) {
	read := func(name string) (json.RawMessage, error) {
		file := filepath.Join(p.Secrets, name)
		b, err := os.ReadFile(file)
		if err != nil {
			return nil, fmt.Errorf("secrets: %w", err)
		}
		var obj map[string]json.RawMessage
		if err := json.Unmarshal(b, &obj); err != nil || obj == nil {
			return nil, fmt.Errorf("secrets: %s is not a JSON object", file)
		}
		return json.RawMessage(b), nil
	}
	var ob singbox.Outbounds
	var err error
	if ob.Tunnel, err = read(cfg.Tunnel); err != nil {
		return singbox.Outbounds{}, err
	}
	if cfg.Proxy != "" {
		if ob.Proxy, err = read(cfg.Proxy); err != nil {
			return singbox.Outbounds{}, err
		}
	}
	return ob, nil
}

// tunnelServer returns the "server" field of the tunnel outbound.
func tunnelServer(tunnel json.RawMessage) string {
	var o struct {
		Server string `json:"server"`
	}
	if err := json.Unmarshal(tunnel, &o); err != nil {
		return ""
	}
	return o.Server
}

// tunnelHost returns the tunnel server when it is a host name, "" for an IP
// literal or a missing server.
func tunnelHost(tunnel json.RawMessage) string {
	s := tunnelServer(tunnel)
	if _, err := netip.ParseAddr(s); err == nil {
		return ""
	}
	return s
}

// excludeIPs returns the addresses the desync engine must never touch: the
// configured ExcludeIPs, the tunnel server when its address is an IP literal,
// and the resolved addresses of a host-name tunnel server, so a correct
// tunnel file protects itself.
func excludeIPs(configured []netip.Prefix, tunnel json.RawMessage, resolved ...netip.Addr) []netip.Prefix {
	out := append([]netip.Prefix(nil), configured...)
	add := func(a netip.Addr) {
		a = a.Unmap()
		p := netip.PrefixFrom(a, a.BitLen())
		for _, q := range out {
			if q == p {
				return
			}
		}
		out = append(out, p)
	}
	if a, err := netip.ParseAddr(tunnelServer(tunnel)); err == nil {
		add(a)
	}
	for _, a := range resolved {
		if a.IsValid() {
			add(a)
		}
	}
	return out
}

var errNoExclude = errors.New("no address to protect: set desync.exclude_ips or use an IP address as the tunnel server")

// errTunnelUnresolved means the tunnel server is a host name that did not
// resolve at connect time, so the desync engine cannot be kept off it.
var errTunnelUnresolved = errors.New("the tunnel server's host name could not be resolved before connecting, so the desync engine cannot be kept off it; " +
	"use an IP address as the tunnel server (desync.exclude_ips does not count as protection for a host name), or connect again when DNS works")
