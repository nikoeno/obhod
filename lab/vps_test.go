package lab

import (
	"context"
	"encoding/json"
	"net/netip"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

// A syntactically valid Reality key (32 bytes, base64url without padding); not a real secret.
const testRealityKey = "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"

func testVPSParams() vpsParams {
	return vpsParams{
		Addr:       netip.MustParseAddr(vpsAddr),
		VLESSPort:  vlessPort,
		SOCKSPort:  socksPort,
		UUID:       "00000000-0000-4000-8000-000000000000",
		Flow:       vlessFlow,
		PrivateKey: testRealityKey,
		PublicKey:  testRealityKey,
		ShortID:    "0123456789abcdef",
		ServerName: realityHost,
		Handshake:  netip.MustParseAddrPort("198.51.100.10:443"),
		Resolver:   netip.AddrPortFrom(netip.MustParseAddr(resolverAddr), 53),
		SOCKSUser:  "lab",
		SOCKSPass:  "0123456789abcdef",
	}
}

func TestVPSServerConfig(t *testing.T) {
	p := testVPSParams()
	out, err := p.serverConfig()
	if err != nil {
		t.Fatal(err)
	}
	var cfg struct {
		Inbounds []struct {
			Type       string `json:"type"`
			Listen     string `json:"listen"`
			ListenPort uint16 `json:"listen_port"`
		} `json:"inbounds"`
	}
	if err := json.Unmarshal(out, &cfg); err != nil {
		t.Fatal(err)
	}
	if len(cfg.Inbounds) != 2 || cfg.Inbounds[0].Type != "vless" || cfg.Inbounds[0].ListenPort != vlessPort ||
		cfg.Inbounds[1].Type != "socks" || cfg.Inbounds[1].ListenPort != socksPort || cfg.Inbounds[0].Listen != vpsAddr {
		t.Errorf("inbounds = %+v", cfg.Inbounds)
	}

	bad := p
	bad.PrivateKey = ""
	if _, err := bad.serverConfig(); err == nil {
		t.Error("serverConfig without a private key: want an error")
	}

	// Client side, in a minimal config so `check` can validate the outbounds too.
	client, err := json.Marshal(map[string]any{"outbounds": []json.RawMessage{p.tunnelOutbound(), p.proxyOutbound()}})
	if err != nil {
		t.Fatal(err)
	}
	bin := pinnedSingBox(t)
	if bin == "" {
		return
	}
	for name, body := range map[string][]byte{"server": out, "client": client} {
		f := filepath.Join(t.TempDir(), name+".json")
		if err := os.WriteFile(f, body, 0o600); err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		res, err := exec.CommandContext(ctx, bin, "check", "-c", f).CombinedOutput()
		cancel()
		if err != nil {
			t.Errorf("sing-box check %s: %v\n%s\n%s", name, err, res, body)
		}
	}
}

func TestRealityKeypair(t *testing.T) {
	if _, _, err := parseRealityKeypair([]byte("garbage\n")); err == nil {
		t.Error("parse garbage: want an error")
	}
	priv, pub, err := parseRealityKeypair([]byte("PrivateKey: abc\nPublicKey: def\n"))
	if err != nil || priv != "abc" || pub != "def" {
		t.Errorf("parse = %q, %q, %v", priv, pub, err)
	}
	bin := pinnedSingBox(t)
	if bin == "" {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, bin, "generate", "reality-keypair").Output()
	if err != nil {
		t.Fatal(err)
	}
	priv, pub, err = parseRealityKeypair(out)
	if err != nil || len(priv) != 43 || len(pub) != 43 {
		t.Errorf("generated keypair: lengths %d/%d, %v", len(priv), len(pub), err)
	}
	// The generated keys render into a config that passes check.
	p := testVPSParams()
	p.PrivateKey, p.PublicKey = priv, pub
	body, err := p.serverConfig()
	if err != nil {
		t.Fatal(err)
	}
	f := filepath.Join(t.TempDir(), "server.json")
	if err := os.WriteFile(f, body, 0o600); err != nil {
		t.Fatal(err)
	}
	if res, err := exec.CommandContext(ctx, bin, "check", "-c", f).CombinedOutput(); err != nil {
		t.Errorf("sing-box check: %v\n%s", err, res)
	}
}

func TestNewUUID(t *testing.T) {
	u, err := newUUID()
	if err != nil {
		t.Fatal(err)
	}
	if len(u) != 36 || u[14] != '4' || u[8] != '-' || u[23] != '-' {
		t.Errorf("uuid %q", u)
	}
}

// pinnedSingBox returns the pinned sing-box for this OS, or "" (with a log line) when it is not
// installed (`make fetch`).
func pinnedSingBox(t *testing.T) string {
	t.Helper()
	bin, err := thirdParty("sing-box")
	if err != nil {
		t.Logf("skipping sing-box check: %v", err)
		return ""
	}
	return bin
}
