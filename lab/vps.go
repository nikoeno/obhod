package lab

// This file has no build tag on purpose: the sing-box server config of the fake VPS is rendered by
// pure Go, so a unit test can render it and run `sing-box check` on it on any OS without root.

import (
	"bufio"
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/netip"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

// vpsParams are the fake VPS's sing-box server settings. Everything except PrivateKey is also what
// a client needs (tunnelOutbound, proxyOutbound), so L7/L8 scenarios can write tunnel.json and
// proxy.json from the same values.
type vpsParams struct {
	Addr       netip.Addr     // VPS address, both inbounds listen on it
	VLESSPort  uint16         // VLESS+Reality inbound (the tunnel route)
	SOCKSPort  uint16         // SOCKS5 inbound with username/password (the proxy route)
	UUID       string         // VLESS user id
	Flow       string         // VLESS flow, the same on server and client
	PrivateKey string         // Reality private key (server only)
	PublicKey  string         // Reality public key (client)
	ShortID    string         // Reality short id, hex
	ServerName string         // Reality server name: a lab site that speaks TLS 1.3
	Handshake  netip.AddrPort // Reality handshake target (that lab site)
	Resolver   netip.AddrPort // plain DNS the server resolves client-requested names with
	SOCKSUser  string
	SOCKSPass  string
}

// vlessFlow is the flow the app's tunnel.json samples use; server and client must agree.
const vlessFlow = "xtls-rprx-vision"

// serverConfig renders the sing-box (1.14) server config: a VLESS+Reality inbound, a SOCKS inbound
// with credentials, one direct outbound, and an explicit DNS server so the VPS resolves lab names
// without depending on the namespace's resolv.conf.
func (p vpsParams) serverConfig() ([]byte, error) {
	if err := p.validate(); err != nil {
		return nil, err
	}
	cfg := map[string]any{
		"log": map[string]any{"level": "info", "timestamp": true},
		"dns": map[string]any{
			"servers": []any{map[string]any{
				"type": "udp", "tag": "lab", "server": p.Resolver.Addr().String(), "server_port": p.Resolver.Port(),
			}},
		},
		"inbounds": []any{
			map[string]any{
				"type":        "vless",
				"tag":         "vless-in",
				"listen":      p.Addr.String(),
				"listen_port": p.VLESSPort,
				"users":       []any{map[string]any{"name": "lab", "uuid": p.UUID, "flow": p.Flow}},
				"tls": map[string]any{
					"enabled":     true,
					"server_name": p.ServerName,
					"reality": map[string]any{
						"enabled": true,
						"handshake": map[string]any{
							"server":      p.Handshake.Addr().String(),
							"server_port": p.Handshake.Port(),
						},
						"private_key": p.PrivateKey,
						"short_id":    []string{p.ShortID},
					},
				},
			},
			map[string]any{
				"type":        "socks",
				"tag":         "socks-in",
				"listen":      p.Addr.String(),
				"listen_port": p.SOCKSPort,
				"users":       []any{map[string]any{"username": p.SOCKSUser, "password": p.SOCKSPass}},
			},
		},
		"outbounds": []any{map[string]any{"type": "direct", "tag": "direct"}},
		"route": map[string]any{
			"final":                   "direct",
			"default_domain_resolver": map[string]any{"server": "lab"},
		},
	}
	return json.MarshalIndent(cfg, "", "  ")
}

// tunnelOutbound is the client side of the VLESS+Reality inbound, in the shape the app reads from
// tunnel.json (the app forces the tag).
func (p vpsParams) tunnelOutbound() json.RawMessage {
	b, _ := json.Marshal(map[string]any{
		"type":        "vless",
		"tag":         "tunnel",
		"server":      p.Addr.String(),
		"server_port": p.VLESSPort,
		"uuid":        p.UUID,
		"flow":        p.Flow,
		"tls": map[string]any{
			"enabled":     true,
			"server_name": p.ServerName,
			"utls":        map[string]any{"enabled": true, "fingerprint": "chrome"},
			"reality":     map[string]any{"enabled": true, "public_key": p.PublicKey, "short_id": p.ShortID},
		},
	})
	return b
}

// proxyOutbound is the client side of the SOCKS inbound, in the shape the app reads from proxy.json.
func (p vpsParams) proxyOutbound() json.RawMessage {
	b, _ := json.Marshal(map[string]any{
		"type":        "socks",
		"tag":         "proxy",
		"server":      p.Addr.String(),
		"server_port": p.SOCKSPort,
		"version":     "5",
		"username":    p.SOCKSUser,
		"password":    p.SOCKSPass,
	})
	return b
}

func (p vpsParams) validate() error {
	switch {
	case !p.Addr.IsValid() || p.VLESSPort == 0 || p.SOCKSPort == 0:
		return errors.New("lab: vps: address or ports missing")
	case p.UUID == "" || p.PrivateKey == "" || p.PublicKey == "" || p.ShortID == "":
		return errors.New("lab: vps: uuid or Reality keys missing")
	case p.ServerName == "" || !p.Handshake.IsValid() || !p.Resolver.IsValid():
		return errors.New("lab: vps: server name, handshake target or resolver missing")
	case p.SOCKSUser == "" || p.SOCKSPass == "":
		return errors.New("lab: vps: SOCKS credentials missing")
	}
	return nil
}

// parseRealityKeypair reads the output of `sing-box generate reality-keypair`
// ("PrivateKey: ...\nPublicKey: ...").
func parseRealityKeypair(out []byte) (priv, pub string, err error) {
	sc := bufio.NewScanner(bytes.NewReader(out))
	for sc.Scan() {
		k, v, ok := strings.Cut(sc.Text(), ":")
		if !ok {
			continue
		}
		switch strings.TrimSpace(k) {
		case "PrivateKey":
			priv = strings.TrimSpace(v)
		case "PublicKey":
			pub = strings.TrimSpace(v)
		}
	}
	if priv == "" || pub == "" {
		return "", "", errors.New("lab: sing-box generate reality-keypair: unexpected output")
	}
	return priv, pub, nil
}

// newUUID returns a random version 4 UUID.
func newUUID() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", fmt.Errorf("lab: %w", err)
	}
	b[6] = b[6]&0x0f | 0x40
	b[8] = b[8]&0x3f | 0x80
	h := hex.EncodeToString(b[:])
	return h[0:8] + "-" + h[8:12] + "-" + h[12:16] + "-" + h[16:20] + "-" + h[20:], nil
}

// randHex returns n random bytes as 2n lowercase hex characters.
func randHex(n int) (string, error) {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("lab: %w", err)
	}
	return hex.EncodeToString(b), nil
}

// repoRoot walks up from the working directory to the folder holding go.mod.
func repoRoot() (string, error) {
	dir, err := os.Getwd()
	if err != nil {
		return "", fmt.Errorf("lab: %w", err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", errors.New("lab: go.mod not found above the working directory")
		}
		dir = parent
	}
}

// thirdPartyPath returns third_party/bin/<goos>-<goarch>/<name> (".exe" on Windows) under root,
// where `make fetch` installs the pinned binaries.
func thirdPartyPath(root, goos, goarch, name string) string {
	if goos == "windows" {
		name += ".exe"
	}
	return filepath.Join(root, "third_party", "bin", goos+"-"+goarch, name)
}

// thirdParty returns the pinned third-party binary name for this OS and architecture, or an error
// naming the missing file.
func thirdParty(name string) (string, error) {
	root, err := repoRoot()
	if err != nil {
		return "", err
	}
	p := thirdPartyPath(root, runtime.GOOS, runtime.GOARCH, name)
	if _, err := os.Stat(p); err != nil {
		return "", fmt.Errorf("lab: %s missing (run `make fetch`): %w", p, err)
	}
	return p, nil
}
