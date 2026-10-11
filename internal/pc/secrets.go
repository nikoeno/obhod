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

// excludeIPs returns the addresses the desync engine must never touch: the
// configured ExcludeIPs plus the tunnel server when its address is an IP
// literal, so a correct tunnel file protects itself.
func excludeIPs(configured []netip.Prefix, tunnel json.RawMessage) []netip.Prefix {
	out := append([]netip.Prefix(nil), configured...)
	var o struct {
		Server string `json:"server"`
	}
	if err := json.Unmarshal(tunnel, &o); err == nil {
		if a, err := netip.ParseAddr(o.Server); err == nil {
			p := netip.PrefixFrom(a.Unmap(), a.Unmap().BitLen())
			dup := false
			for _, q := range out {
				if q == p {
					dup = true
				}
			}
			if !dup {
				out = append(out, p)
			}
		}
	}
	return out
}

var errNoExclude = errors.New("no address to protect: set desync.exclude_ips or use an IP address as the tunnel server")
