package winws

import (
	"bytes"
	"errors"
	"flag"
	"math/rand"
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

var update = flag.Bool("update", false, "rewrite golden files")

func pfx(s string) netip.Prefix { return netip.MustParsePrefix(s) }

func TestFilterClauses(t *testing.T) {
	cases := []struct {
		name  string
		idx   int
		ips   []netip.Prefix
		ports [][2]uint16
		want  string
	}{
		{"interface only", 7, nil, nil, "ifIdx == 7"},
		{"ipv4 host", 7, []netip.Prefix{pfx("203.0.113.7/32")}, nil,
			"ifIdx == 7 and not (ip.DstAddr == 203.0.113.7)"},
		{"ipv4 net", 7, []netip.Prefix{pfx("10.0.0.0/8")}, nil,
			"ifIdx == 7 and not ((ip.DstAddr >= 10.0.0.0 and ip.DstAddr <= 10.255.255.255))"},
		{"unmasked prefix", 7, []netip.Prefix{pfx("192.168.5.9/24")}, nil,
			"ifIdx == 7 and not ((ip.DstAddr >= 192.168.5.0 and ip.DstAddr <= 192.168.5.255))"},
		{"ipv6 host", 7, []netip.Prefix{pfx("2001:db8::1/128")}, nil,
			"ifIdx == 7 and not (ipv6.DstAddr == 2001:db8::1)"},
		{"ipv6 net", 7, []netip.Prefix{pfx("2001:db8::/32")}, nil,
			"ifIdx == 7 and not ((ipv6.DstAddr >= 2001:db8:: and ipv6.DstAddr <= 2001:db8:ffff:ffff:ffff:ffff:ffff:ffff))"},
		{"mixed", 3, []netip.Prefix{pfx("2001:db8::/32"), pfx("203.0.113.0/30"), pfx("198.51.100.1/32")}, nil,
			"ifIdx == 3 and not ((ip.DstAddr >= 203.0.113.0 and ip.DstAddr <= 203.0.113.3) or ip.DstAddr == 198.51.100.1)" +
				" and not ((ipv6.DstAddr >= 2001:db8:: and ipv6.DstAddr <= 2001:db8:ffff:ffff:ffff:ffff:ffff:ffff))"},
		{"zero prefix", 3, []netip.Prefix{pfx("0.0.0.0/0")}, nil,
			"ifIdx == 3 and not ((ip.DstAddr >= 0.0.0.0 and ip.DstAddr <= 255.255.255.255))"},
		{"ports", 3, nil, [][2]uint16{{50000, 50099}, {8080, 8080}},
			"ifIdx == 3 and not ((tcp.SrcPort >= 50000 and tcp.SrcPort <= 50099) or tcp.SrcPort == 8080)"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := Filter(c.idx, c.ips, c.ports)
			if err != nil {
				t.Fatal(err)
			}
			if got != c.want {
				t.Fatalf("\n got %s\nwant %s", got, c.want)
			}
		})
	}
}

func TestFilterErrors(t *testing.T) {
	for name, f := range map[string]func() error{
		"zero ifindex":     func() error { _, err := Filter(0, nil, nil); return err },
		"negative ifindex": func() error { _, err := Filter(-1, nil, nil); return err },
		"invalid prefix":   func() error { _, err := Filter(1, []netip.Prefix{{}}, nil); return err },
		"reversed range":   func() error { _, err := Filter(1, nil, [][2]uint16{{20, 10}}); return err },
		"port zero":        func() error { _, err := Filter(1, nil, [][2]uint16{{0, 10}}); return err },
	} {
		if err := f(); !errors.Is(err, ErrInvalid) {
			t.Errorf("%s: err = %v", name, err)
		}
	}
}

func baseInput() Input {
	return Input{
		Binary:       "C:/app/third_party/winws2.exe",
		LuaDir:       "C:/app/third_party/lua",
		Strategy:     []string{"--filter-l7=tls", "--payload=tls_client_hello", "--out-range=-d10", "--lua-desync=fake:blob=fake_default_tls:tcp_md5"},
		Hostlist:     []string{"youtube.com", "googlevideo.com", "ytimg.com", "youtube.com"},
		IfIndex:      12,
		ExcludeIPs:   []netip.Prefix{pfx("203.0.113.7/32"), pfx("2001:db8::7/128")},
		ExcludePorts: [][2]uint16{{50000, 50099}},
	}
}

func golden(t *testing.T, name string, got []byte) {
	t.Helper()
	path := filepath.Join("testdata", name)
	if *update {
		if err := os.MkdirAll("testdata", 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, got, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(bytes.ReplaceAll(want, []byte("\r\n"), []byte("\n")), got) {
		t.Fatalf("%s differs from golden (run with -update)\n%s", name, got)
	}
}

func TestRenderGolden(t *testing.T) {
	out, err := Render(baseInput())
	if err != nil {
		t.Fatal(err)
	}
	if out.Path != "C:/app/third_party/winws2.exe" {
		t.Fatalf("path = %q", out.Path)
	}
	golden(t, "args.golden", []byte(strings.Join(out.Args, "\n")+"\n"))
	golden(t, "hostlist.golden", out.Hostlist)

	in := baseInput()
	in.Ports = []uint16{8443, 443}
	in.LuaInit = []string{"zapret-lib.lua", "zapret-antidpi.lua", "zapret-auto.lua"}
	in.ExcludeIPs, in.ExcludePorts = nil, nil
	out, err = Render(in)
	if err != nil {
		t.Fatal(err)
	}
	golden(t, "args_custom.golden", []byte(strings.Join(out.Args, "\n")+"\n"))
}

func TestArgOrder(t *testing.T) {
	out, err := Render(baseInput())
	if err != nil {
		t.Fatal(err)
	}
	prefixes := []string{"--wf-tcp-out=", "--wf-raw-filter=", "--lua-init=", "--lua-init=", "--hostlist=hostlist.txt", "--filter-l7=tls"}
	for i, p := range prefixes {
		if !strings.HasPrefix(out.Args[i], p) {
			t.Fatalf("arg %d = %q, want prefix %q (all: %q)", i, out.Args[i], p, out.Args)
		}
	}
	if out.Args[0] != "--wf-tcp-out=80,443" {
		t.Fatalf("default ports = %q", out.Args[0])
	}
	if got := string(out.Hostlist); got != "googlevideo.com\nyoutube.com\nytimg.com\n" {
		t.Fatalf("hostlist = %q", got)
	}
}

func TestDefaultLuaDir(t *testing.T) {
	in := baseInput()
	in.LuaDir = ""
	in.Binary = filepath.Join("opt", "bin", "winws2.exe")
	out, err := Render(in)
	if err != nil {
		t.Fatal(err)
	}
	want := "--lua-init=@" + filepath.Join("opt", "bin", "lua") + "/zapret-lib.lua"
	if out.Args[2] != want {
		t.Fatalf("arg = %q, want %q", out.Args[2], want)
	}
	in.LuaDir = "C:/lua/"
	if out, _ = Render(in); out.Args[2] != "--lua-init=@C:/lua/zapret-lib.lua" {
		t.Fatalf("trailing slash kept: %q", out.Args[2])
	}
}

func TestDeterministic(t *testing.T) {
	want, err := Render(baseInput())
	if err != nil {
		t.Fatal(err)
	}
	rng := rand.New(rand.NewSource(7))
	for range 20 {
		in := baseInput()
		in.Hostlist = append([]string(nil), in.Hostlist...)
		rng.Shuffle(len(in.Hostlist), func(i, j int) { in.Hostlist[i], in.Hostlist[j] = in.Hostlist[j], in.Hostlist[i] })
		in.Ports = []uint16{80, 443, 80}
		got, err := Render(in)
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(got.Hostlist, want.Hostlist) || strings.Join(got.Args, "\x00") != strings.Join(want.Args, "\x00") {
			t.Fatal("output depends on input order")
		}
	}
}

func TestRenderErrors(t *testing.T) {
	cases := map[string]func(*Input){
		"no binary":       func(in *Input) { in.Binary = "" },
		"no domains":      func(in *Input) { in.Hostlist = nil },
		"blank domain":    func(in *Input) { in.Hostlist = []string{"  "} },
		"domain with gap": func(in *Input) { in.Hostlist = []string{"a b"} },
		"zero ifindex":    func(in *Input) { in.IfIndex = 0 },
		"empty lua file":  func(in *Input) { in.LuaInit = []string{""} },
		"lua escape":      func(in *Input) { in.LuaInit = []string{"../evil.lua"} },
		"port zero":       func(in *Input) { in.Ports = []uint16{0} },
		"bad range":       func(in *Input) { in.ExcludePorts = [][2]uint16{{9, 1}} },
	}
	for name, mut := range cases {
		t.Run(name, func(t *testing.T) {
			in := baseInput()
			mut(&in)
			if _, err := Render(in); !errors.Is(err, ErrInvalid) {
				t.Fatalf("err = %v", err)
			}
		})
	}
}
