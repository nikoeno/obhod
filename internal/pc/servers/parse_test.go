package servers

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"flag"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

var update = flag.Bool("update", false, "rewrite the golden files in testdata")

const fakeUUID = "00000000-0000-4000-8000-000000000000"

type goldenParsed struct {
	Protocol string          `json:"protocol"`
	Host     string          `json:"host"`
	Port     int             `json:"port"`
	Engine   string          `json:"engine"`
	Name     string          `json:"name"`
	Outbound json.RawMessage `json:"outbound"`
}

func readFixtures(t *testing.T) []string {
	t.Helper()
	files, err := filepath.Glob(filepath.Join("testdata", "*.in"))
	if err != nil || len(files) == 0 {
		t.Fatalf("no fixtures: %v", err)
	}
	return files
}

func TestGoldenParse(t *testing.T) {
	for _, in := range readFixtures(t) {
		name := strings.TrimSuffix(filepath.Base(in), ".in")
		t.Run(name, func(t *testing.T) {
			b, err := os.ReadFile(in)
			if err != nil {
				t.Fatal(err)
			}
			p, err := Parse(string(b))
			if err != nil {
				t.Fatalf("Parse: %v", err)
			}
			got, err := json.Marshal(goldenParsed{p.Protocol, p.Host, p.Port, p.Engine, p.Name, p.Outbound})
			if err != nil {
				t.Fatal(err)
			}
			var buf bytes.Buffer
			if err := json.Indent(&buf, got, "", "  "); err != nil {
				t.Fatal(err)
			}
			buf.WriteByte('\n')
			golden := filepath.Join("testdata", name+".golden.json")
			if *update {
				if err := os.WriteFile(golden, buf.Bytes(), 0o644); err != nil {
					t.Fatal(err)
				}
				return
			}
			want, err := os.ReadFile(golden)
			if err != nil {
				t.Fatalf("%v (run with -update to create it)", err)
			}
			want = bytes.ReplaceAll(want, []byte("\r\n"), []byte("\n"))
			if !bytes.Equal(want, buf.Bytes()) {
				t.Errorf("differs from %s:\n--- got ---\n%s--- want ---\n%s", golden, buf.Bytes(), want)
			}
			if bytes.Contains(p.Outbound, []byte(`"tag"`)) {
				t.Error("the outbound must not carry a tag")
			}
		})
	}
}

func b64(s string) string { return base64.StdEncoding.EncodeToString([]byte(s)) }

func TestParseNegative(t *testing.T) {
	vmess := func(json string) string { return "vmess://" + b64(json) }
	cases := []struct {
		name  string
		input string
		want  error
	}{
		{"empty", "   ", ErrInvalidInput},
		{"not a link", "hello world", ErrInvalidInput},
		{"two links", "trojan://p@example.com:443\ntrojan://q@example.com:443", ErrInvalidInput},
		{"unknown scheme", "wireguard://x@example.com:1", ErrUnsupportedProtocol},
		{"http link", "https://example.com/", ErrUnsupportedProtocol},
		{"vless no port", "vless://" + fakeUUID + "@example.com?security=none", ErrInvalidInput},
		{"vless bad port", "vless://" + fakeUUID + "@example.com:99999", ErrInvalidInput},
		{"vless port zero", "vless://" + fakeUUID + "@example.com:0", ErrInvalidInput},
		{"vless port text", "vless://" + fakeUUID + "@example.com:abc", ErrInvalidInput},
		{"vless no host", "vless://" + fakeUUID + "@:443", ErrInvalidInput},
		{"vless empty credential", "vless://@example.com:443", ErrInvalidInput},
		{"vless not a uuid", "vless://not-a-uuid@example.com:443", ErrInvalidInput},
		{"vless ipv6 without brackets", "vless://" + fakeUUID + "@2001:db8::1:443", ErrInvalidInput},
		{"vless host with space", "vless://" + fakeUUID + "@exa mple.com:443", ErrInvalidInput},
		{"vless reality no key", "vless://" + fakeUUID + "@example.com:443?security=reality&sni=example.com", ErrInvalidInput},
		{"vless reality no sni", "vless://" + fakeUUID + "@example.com:443?security=reality&pbk=AAAA", ErrInvalidInput},
		{"vless xtls security", "vless://" + fakeUUID + "@example.com:443?security=xtls", ErrUnsupportedProtocol},
		{"vless pq encryption", "vless://" + fakeUUID + "@example.com:443?encryption=mlkem768x25519plus.native.0rtt.AAAA", ErrUnsupportedProtocol},
		{"vless xhttp", "vless://" + fakeUUID + "@example.com:443?security=tls&type=xhttp&path=%2F", ErrUnsupportedProtocol},
		{"vless kcp", "vless://" + fakeUUID + "@example.com:443?type=kcp", ErrUnsupportedProtocol},
		{"vless splithttp", "vless://" + fakeUUID + "@example.com:443?type=splithttp", ErrUnsupportedProtocol},
		{"vless odd flow", "vless://" + fakeUUID + "@example.com:443?flow=other", ErrUnsupportedProtocol},
		{"vless bad fingerprint", "vless://" + fakeUUID + "@example.com:443?security=tls&fp=nope", ErrInvalidInput},
		{"trojan no password", "trojan://example.com:443", ErrInvalidInput},
		{"trojan empty password", "trojan://@example.com:443", ErrInvalidInput},
		{"ss bad base64", "ss://!!!notbase64!!!@example.com:8388", ErrInvalidInput},
		{"ss legacy bad base64", "ss://@@@@", ErrInvalidInput},
		{"ss unknown cipher", "ss://" + b64("rot13:pw") + "@example.com:8388", ErrUnsupportedProtocol},
		{"ss no password", "ss://" + b64("aes-256-gcm:") + "@example.com:8388", ErrInvalidInput},
		{"ss unknown plugin", "ss://" + b64("aes-256-gcm:pw") + "@example.com:8388?plugin=other", ErrUnsupportedProtocol},
		{"hy2 no password", "hy2://example.com:443", ErrInvalidInput},
		{"hy2 bad range", "hy2://pw@example.com:5000-100", ErrInvalidInput},
		{"hy2 pin", "hy2://pw@example.com:443?pinSHA256=AA", ErrInvalidInput},
		{"hy2 obfs without password", "hy2://pw@example.com:443?obfs=salamander", ErrInvalidInput},
		{"hy2 other obfs", "hy2://pw@example.com:443?obfs=other&obfs-password=x", ErrUnsupportedProtocol},
		{"vmess bad base64", "vmess://%%%", ErrInvalidInput},
		{"vmess not json", vmess("hello"), ErrInvalidInput},
		{"vmess no port", vmess(`{"add":"example.com","id":"` + fakeUUID + `"}`), ErrInvalidInput},
		{"vmess bad port", vmess(`{"add":"example.com","port":"70000","id":"` + fakeUUID + `"}`), ErrInvalidInput},
		{"vmess no id", vmess(`{"add":"example.com","port":"443"}`), ErrInvalidInput},
		{"vmess odd cipher", vmess(`{"add":"example.com","port":"443","id":"` + fakeUUID + `","scy":"rc4"}`), ErrUnsupportedProtocol},
		{"vmess kcp", vmess(`{"add":"example.com","port":"443","id":"` + fakeUUID + `","net":"kcp"}`), ErrUnsupportedProtocol},
		{"tuic no password", "tuic://" + fakeUUID + "@example.com:443", ErrInvalidInput},
		{"tuic bad congestion", "tuic://" + fakeUUID + ":pw@example.com:443?congestion_control=weird", ErrUnsupportedProtocol},

		{"json two objects", `{"type":"trojan"} {"type":"trojan"}`, ErrInvalidInput},
		{"json array", `[{"type":"trojan"}]`, ErrInvalidInput},
		{"json broken", `{"type":`, ErrInvalidInput},
		{"json no type", `{"server":"example.com","server_port":443}`, ErrInvalidInput},
		{"json socks", `{"type":"socks","server":"example.com","server_port":1080}`, ErrUnsupportedProtocol},
		{"json detour", `{"type":"trojan","server":"example.com","server_port":443,"password":"p","detour":"direct"}`, ErrInvalidInput},
		{"json bind_interface", `{"type":"trojan","server":"example.com","server_port":443,"password":"p","bind_interface":"eth0"}`, ErrInvalidInput},
		{"json inet4_bind_address", `{"type":"trojan","server":"example.com","server_port":443,"password":"p","inet4_bind_address":"192.0.2.1"}`, ErrInvalidInput},
		{"json nested detour", `{"type":"trojan","server":"example.com","server_port":443,"password":"p","tls":{"enabled":true,"detour":"x"}}`, ErrInvalidInput},
		{"json no server", `{"type":"trojan","server_port":443,"password":"p"}`, ErrInvalidInput},
		{"json string port", `{"type":"trojan","server":"example.com","server_port":"443","password":"p"}`, ErrInvalidInput},
		{"json fractional port", `{"type":"trojan","server":"example.com","server_port":443.5,"password":"p"}`, ErrInvalidInput},
		{"json no password", `{"type":"trojan","server":"example.com","server_port":443}`, ErrInvalidInput},
		{"json reality no key", `{"type":"vless","server":"example.com","server_port":443,"uuid":"` + fakeUUID + `","tls":{"enabled":true,"server_name":"example.com","reality":{"enabled":true}}}`, ErrInvalidInput},
		{"json xray transport", `{"type":"vless","server":"example.com","server_port":443,"uuid":"` + fakeUUID + `","transport":{"type":"xhttp"}}`, ErrUnsupportedProtocol},
		{"json quic transport", `{"type":"vless","server":"example.com","server_port":443,"uuid":"` + fakeUUID + `","transport":{"type":"quic"}}`, ErrUnsupportedProtocol},
		{"json deep", `{"type":"trojan","server":"example.com","server_port":443,"password":"p","a":{"b":{"c":{"d":{"e":{"f":{"g":1}}}}}}}`, ErrInvalidInput},
		{"json too many fields", `{"type":"trojan","server":"example.com","server_port":443,"password":"p","x":[` + strings.Repeat("1,", 600) + `1]}`, ErrInvalidInput},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := Parse(c.input)
			if !errors.Is(err, c.want) {
				t.Fatalf("Parse error = %v, want kind %v", err, c.want)
			}
			if _, ok := err.(interface{ UserMessage() string }); !ok {
				t.Errorf("error %T has no UserMessage", err)
			}
		})
	}
}

func TestParseOversized(t *testing.T) {
	big := "trojan://" + strings.Repeat("a", MaxInputBytes) + "@example.com:443"
	if _, err := Parse(big); !errors.Is(err, ErrTooLarge) {
		t.Fatalf("oversized link: %v", err)
	}
	if _, err := Parse(strings.Repeat(" ", MaxInputBytes) + "trojan://p@example.com:443"); !errors.Is(err, ErrTooLarge) {
		t.Fatalf("oversized padding: %v", err)
	}
	// Exactly at the limit is accepted.
	ok := "trojan://p@example.com:443?x=" + strings.Repeat("a", MaxInputBytes-len("trojan://p@example.com:443?x="))
	if _, err := Parse(ok); err != nil {
		t.Fatalf("input of exactly %d bytes: %v", MaxInputBytes, err)
	}
}

func TestParseDetails(t *testing.T) {
	p, err := Parse("\ufeff  trojan://pw@Example.com:443#" + strings.Repeat("n", 80) + "  ")
	if err != nil {
		t.Fatal(err)
	}
	if len([]rune(p.Name)) != MaxNameRunes {
		t.Errorf("fragment name cut to %d runes, got %d", MaxNameRunes, len([]rune(p.Name)))
	}
	if p.Protocol != "trojan" || p.Port != 443 || p.Engine != EngineSingBox {
		t.Errorf("summary = %+v", p)
	}

	// The tag of a raw outbound is dropped; numbers survive unchanged.
	p, err = Parse(`{"type":"trojan","tag":"mine","server":"example.com","server_port":443,"password":"pw","multiplex":{"enabled":true,"max_connections":4}}`)
	if err != nil {
		t.Fatal(err)
	}
	var o map[string]any
	if err := json.Unmarshal(p.Outbound, &o); err != nil {
		t.Fatal(err)
	}
	if _, has := o["tag"]; has {
		t.Error("tag was kept")
	}
	if !strings.Contains(string(p.Outbound), `"max_connections":4`) {
		t.Errorf("numbers changed: %s", p.Outbound)
	}

	// hy2 port hopping: the summary port is the first range start or the single port.
	p, err = Parse("hy2://pw@198.51.100.9:20000-30000")
	if err != nil || p.Port != 20000 {
		t.Fatalf("range only: port %d err %v", p.Port, err)
	}
	p, err = Parse("hy2://pw@198.51.100.9")
	if err != nil || p.Port != 443 {
		t.Fatalf("default port: %d %v", p.Port, err)
	}
}

func TestValidName(t *testing.T) {
	for s, want := range map[string]bool{
		"Server 1": true, "Мой сервер": true, strings.Repeat("a", 60): true,
		"": false, "   ": false, strings.Repeat("a", 61): false, "bad\nname": false, "tab\there": false, "nul\x00": false,
	} {
		if got := ValidName(s); got != want {
			t.Errorf("ValidName(%q) = %v, want %v", s, got, want)
		}
	}
}
