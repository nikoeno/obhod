package nfqws

import (
	"bytes"
	"errors"
	"flag"
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

var update = flag.Bool("update", false, "rewrite golden files")

func pfx(s string) netip.Prefix { return netip.MustParsePrefix(s) }

func baseInput() Input {
	return Input{
		Binary:       "/opt/splitter/nfqws2",
		LuaDir:       "/opt/splitter/lua",
		Strategy:     []string{"--filter-l7=tls", "--payload=tls_client_hello", "--lua-desync=fake:blob=fake_default_tls:tcp_md5"},
		Hostlist:     []string{"youtube.com", "googlevideo.com", "youtube.com"},
		Uplink:       "eth0",
		ExcludeIPs:   []netip.Prefix{pfx("203.0.113.7/32"), pfx("2001:db8::7/128")},
		ExcludePorts: [][2]uint16{{50000, 50099}},
	}
}

func golden(t *testing.T, name string, lines []string) {
	t.Helper()
	got := []byte(strings.Join(lines, "\n") + "\n")
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

func TestGolden(t *testing.T) {
	out, err := Render(baseInput())
	if err != nil {
		t.Fatal(err)
	}
	golden(t, "args.golden", out.Args)
	golden(t, "nft_setup.golden", out.NFTSetup)
	golden(t, "nft_down.golden", out.NFTDown)
	if got := string(out.Hostlist); got != "googlevideo.com\nyoutube.com\n" {
		t.Fatalf("hostlist = %q", got)
	}

	in := baseInput()
	in.ExcludeIPs = []netip.Prefix{pfx("203.0.113.0/24")}
	in.ExcludePorts = nil
	in.Ports = []uint16{8443, 443}
	in.QueueNum = 300
	in.Mark = 0x20000000
	out, err = Render(in)
	if err != nil {
		t.Fatal(err)
	}
	golden(t, "args_custom.golden", out.Args)
	golden(t, "nft_setup_ipv4_only.golden", out.NFTSetup)

	in.ExcludeIPs = []netip.Prefix{pfx("2001:db8::/32")}
	out, err = Render(in)
	if err != nil {
		t.Fatal(err)
	}
	golden(t, "nft_setup_ipv6_only.golden", out.NFTSetup)
}

func TestArgs(t *testing.T) {
	out, err := Render(baseInput())
	if err != nil {
		t.Fatal(err)
	}
	if out.Args[0] != "--qnum=200" || out.Args[1] != "--lua-init=@/opt/splitter/lua/zapret-lib.lua" {
		t.Fatalf("args = %q", out.Args)
	}
	for _, a := range out.Args {
		if strings.HasPrefix(a, "--fwmark") {
			t.Fatalf("default mark must not be passed: %q", out.Args)
		}
	}
	in := baseInput()
	in.LuaDir = ""
	if out, _ = Render(in); out.Args[1] != "--lua-init=@"+filepath.Join("/opt/splitter", "lua")+"/zapret-lib.lua" {
		t.Fatalf("default lua dir: %q", out.Args[1])
	}
}

func TestNFTShape(t *testing.T) {
	out, err := Render(baseInput())
	if err != nil {
		t.Fatal(err)
	}
	all := strings.Join(out.NFTSetup, "\n")
	for _, want := range []string{
		"nft add table inet splitter",
		"priority 101", "priority -401",
		`oifname "eth0"`, "tcp dport { 80, 443 }",
		"meta mark and 0x40000000 == 0", "ct original packets 1-12", "queue num 200 bypass",
		"meta mark and 0x40000000 != 0 notrack",
	} {
		if !strings.Contains(all, want) {
			t.Errorf("setup lacks %q:\n%s", want, all)
		}
	}
	// The queue rule must come after the excludes so they win.
	queue, ret := strings.Index(all, "queue num"), strings.LastIndex(all, " return")
	if ret < 0 || ret > queue {
		t.Errorf("exclude returns must precede the queue rule:\n%s", all)
	}
	// Nothing but our own table is ever touched.
	for _, c := range append(append([]string(nil), out.NFTSetup...), out.NFTDown...) {
		if !strings.Contains(c, "inet splitter") {
			t.Errorf("command outside our table: %s", c)
		}
	}
	if len(out.NFTDown) != 1 || out.NFTDown[0] != "nft delete table inet splitter" {
		t.Fatalf("down = %q", out.NFTDown)
	}
	for _, c := range out.NFTSetup {
		if strings.Contains(c, "flush") || strings.Contains(c, "sysctl") {
			t.Errorf("forbidden command: %s", c)
		}
	}
}

func TestNoExcludes(t *testing.T) {
	in := baseInput()
	in.ExcludeIPs, in.ExcludePorts = nil, nil
	out, err := Render(in)
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range out.NFTSetup {
		if strings.Contains(c, " return") {
			t.Errorf("unexpected exclude rule: %s", c)
		}
	}
}

func TestDeterministic(t *testing.T) {
	want, _ := Render(baseInput())
	in := baseInput()
	in.Hostlist = []string{"youtube.com", "youtube.com", "googlevideo.com"}
	in.ExcludeIPs = []netip.Prefix{pfx("2001:db8::7/128"), pfx("203.0.113.7/32")}
	in.Ports = []uint16{443, 80, 80}
	got, _ := Render(in)
	if strings.Join(got.NFTSetup, "\n") != strings.Join(want.NFTSetup, "\n") ||
		strings.Join(got.Args, "\n") != strings.Join(want.Args, "\n") ||
		!bytes.Equal(got.Hostlist, want.Hostlist) {
		t.Fatal("output depends on input order")
	}
}

func TestErrors(t *testing.T) {
	cases := map[string]func(*Input){
		"no binary":       func(in *Input) { in.Binary = "" },
		"no domains":      func(in *Input) { in.Hostlist = nil },
		"blank domain":    func(in *Input) { in.Hostlist = []string{""} },
		"empty uplink":    func(in *Input) { in.Uplink = "" },
		"quote in uplink": func(in *Input) { in.Uplink = `eth0"; flush ruleset; "` },
		"space in uplink": func(in *Input) { in.Uplink = "my eth" },
		"long uplink":     func(in *Input) { in.Uplink = strings.Repeat("a", 16) },
		"invalid prefix":  func(in *Input) { in.ExcludeIPs = []netip.Prefix{{}} },
		"bad range":       func(in *Input) { in.ExcludePorts = [][2]uint16{{9, 1}} },
		"port zero":       func(in *Input) { in.Ports = []uint16{0} },
		"lua escape":      func(in *Input) { in.LuaInit = []string{"../x.lua"} },
		"empty lua file":  func(in *Input) { in.LuaInit = []string{""} },
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
