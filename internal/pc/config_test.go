package pc

import (
	"bytes"
	"encoding/json"
	"errors"
	"flag"
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"

	"github.com/nikoejento/obhod/internal/lib/paths"
	"github.com/nikoejento/obhod/internal/lib/route"
)

var update = flag.Bool("update", false, "rewrite golden files")

func goldenYAML(t *testing.T, name string, cfg Config) {
	t.Helper()
	got, err := yaml.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	got = bytes.ReplaceAll(got, []byte(`\`), []byte("/")) // the same golden on every OS
	path := filepath.Join("testdata", name)
	if *update {
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

func TestLoadMinimalIsDefaults(t *testing.T) {
	cfg, err := LoadConfig(filepath.Join("testdata", "minimal.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	goldenYAML(t, "minimal.golden.yaml", cfg)
	if !cfg.SingBox.BlockQUIC || cfg.SingBox.DNS.DirectDoH != "https://1.1.1.1/dns-query" || cfg.Secrets.Tunnel != "tunnel.json" {
		t.Fatalf("defaults not applied: %+v", cfg.SingBox)
	}
	if cfg.SingBox.ClashSecret != "" {
		t.Fatal("a clash secret appeared in the config")
	}
}

func TestLoadFull(t *testing.T) {
	cfg, err := LoadConfig(filepath.Join("testdata", "full.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	// Relative list paths are resolved against the config's directory; absolute ones stay.
	if got, want := cfg.Checker.Lists.Candidates, filepath.Join("testdata", "lists", "candidates.txt"); len(got) != 1 || got[0] != want {
		t.Fatalf("candidates = %v, want [%s]", got, want)
	}
	checks := map[string]bool{
		"root":        cfg.Paths.Root == "/srv/splitter",
		"proxy file":  cfg.Secrets.Proxy == "corp-proxy.json",
		"concurrency": cfg.Analyzer.Concurrency == 4 && cfg.Analyzer.DoH[0] == "https://9.9.9.9/dns-query",
		"schedule":    cfg.Checker.Schedule.Interval.Hours() == 2 && cfg.Checker.Schedule.FailThreshold == 4,
		"quic":        !cfg.SingBox.BlockQUIC,
		"tun":         cfg.SingBox.TUN.Name == "splitter0" && cfg.SingBox.TUN.Stack == "gvisor",
		"exclude":     len(cfg.Desync.ExcludeIPs) == 2 && cfg.Desync.ExcludeIPs[0] == netip.MustParsePrefix("203.0.113.7/32"),
		"ports":       len(cfg.Desync.ExcludePorts) == 1 && cfg.Desync.ExcludePorts[0] == [2]uint16{50000, 50099},
		"procman":     cfg.Procman.MaxRestarts == 3 && cfg.Procman.RestartWindow.Minutes() == 5,
		"monitor":     cfg.Monitor.PollInterval.Seconds() == 5,
		"status":      cfg.Status.Listen == "127.0.0.1:9100" && cfg.Status.Token == "abc",
		"log":         cfg.Log.Level == "debug" && cfg.Log.MaxFiles == 2,
		"own":         len(cfg.SingBox.OwnProcesses) == 1,
	}
	for name, ok := range checks {
		if !ok {
			t.Errorf("%s not loaded", name)
		}
	}
	goldenYAML(t, "full.golden.yaml", cfg)
}

func TestAbsoluteListPathsStay(t *testing.T) {
	abs, err := filepath.Abs(filepath.Join("some", "dir", "list.txt"))
	if err != nil {
		t.Fatal(err)
	}
	f := filepath.Join(t.TempDir(), "app.yaml")
	body := "checker:\n  lists:\n    block: ['" + strings.ReplaceAll(abs, "'", "''") + "']\n"
	if err := os.WriteFile(f, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := LoadConfig(f)
	if err != nil || len(cfg.Checker.Lists.Block) != 1 || cfg.Checker.Lists.Block[0] != abs {
		t.Fatalf("block = %v, err %v", cfg.Checker.Lists.Block, err)
	}
}

func TestLoadRejects(t *testing.T) {
	cases := map[string]string{
		"unknown_key":         "bogus",
		"duration":            "time.Duration",
		"secret_in_config":    "clashsecret",
		"secret_name":         "secrets.tunnel",
		"non_loopback_status": "loopback",
		"unknown_default":     "unknowndefault",
		"log_level":           "log.level",
		"salt_set":            "salt",
		"unknown_engine":      "desync.engine",
	}
	for name, want := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := LoadConfig(filepath.Join("testdata", "bad_"+name+".yaml"))
			if err == nil || !strings.Contains(strings.ToLower(err.Error()), strings.ToLower(want)) {
				t.Fatalf("err = %v, want it to mention %q", err, want)
			}
		})
	}
	if _, err := LoadConfig(filepath.Join("testdata", "missing.yaml")); err == nil {
		t.Fatal("a missing file is an error")
	}
}

func TestValidateReportsEveryProblem(t *testing.T) {
	c := DefaultConfig()
	c.Log.Level = "loud"
	c.Secrets.Tunnel = "a/b"
	err := c.Validate()
	if err == nil || !strings.Contains(err.Error(), "log.level") || !strings.Contains(err.Error(), "secrets.tunnel") {
		t.Fatalf("err = %v", err)
	}
}

func writeSecret(t *testing.T, p paths.Paths, name, content string) {
	t.Helper()
	if err := os.MkdirAll(p.Secrets, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(p.Secrets, name), []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestLoadSecrets(t *testing.T) {
	p := paths.FromRoot(t.TempDir())
	cfg := SecretsConfig{Tunnel: "tunnel.json", Proxy: "proxy.json"}

	if _, err := loadSecrets(cfg, p); err == nil {
		t.Fatal("missing tunnel file accepted")
	}
	writeSecret(t, p, "tunnel.json", `{"type":"vless","server":"203.0.113.7","uuid":"TOP-SECRET-UUID"}`)
	ob, err := loadSecrets(SecretsConfig{Tunnel: "tunnel.json"}, p)
	if err != nil || ob.Proxy != nil || !strings.Contains(string(ob.Tunnel), "vless") {
		t.Fatalf("ob = %+v, err %v", ob, err)
	}
	if _, err := loadSecrets(cfg, p); err == nil {
		t.Fatal("missing proxy file accepted")
	}
	writeSecret(t, p, "proxy.json", `{"type":"socks","server":"127.0.0.1"}`)
	if ob, err = loadSecrets(cfg, p); err != nil || ob.Proxy == nil {
		t.Fatalf("proxy not loaded: %v", err)
	}

	// An unusable file is reported by name; its content never appears in the error.
	writeSecret(t, p, "tunnel.json", `["TOP-SECRET-UUID"`)
	_, err = loadSecrets(cfg, p)
	if err == nil || !strings.Contains(err.Error(), "tunnel.json") || strings.Contains(err.Error(), "TOP-SECRET") {
		t.Fatalf("err = %v", err)
	}
	writeSecret(t, p, "tunnel.json", `[1,2]`)
	if _, err = loadSecrets(cfg, p); err == nil || strings.Contains(err.Error(), "1,2") {
		t.Fatalf("err = %v", err)
	}
}

func TestExcludeIPsAddsTunnelServer(t *testing.T) {
	configured := []netip.Prefix{netip.MustParsePrefix("198.51.100.0/24")}
	got := excludeIPs(configured, json.RawMessage(`{"server":"203.0.113.7"}`))
	if len(got) != 2 || got[1] != netip.MustParsePrefix("203.0.113.7/32") {
		t.Fatalf("got %v", got)
	}
	if len(configured) != 1 {
		t.Fatal("the configured slice was modified")
	}
	if got := excludeIPs(got, json.RawMessage(`{"server":"203.0.113.7"}`)); len(got) != 2 {
		t.Fatalf("duplicate added: %v", got)
	}
	if got := excludeIPs(nil, json.RawMessage(`{"server":"2001:db8::7"}`)); len(got) != 1 || got[0].Bits() != 128 {
		t.Fatalf("ipv6 server: %v", got)
	}
	if got := excludeIPs(nil, json.RawMessage(`{"server":"vpn.example.org"}`)); len(got) != 0 {
		t.Fatalf("a hostname cannot be excluded: %v", got)
	}
	if got := excludeIPs(nil, json.RawMessage(`not json`)); len(got) != 0 {
		t.Fatalf("got %v", got)
	}
}

func TestFindBinary(t *testing.T) {
	exists := map[string]bool{}
	has := func(p string) bool { return exists[filepath.ToSlash(p)] }
	if _, err := findBinary("sing-box", "/x/custom", "/app/splitter", has); err == nil {
		t.Fatal("a configured but missing binary must be an error")
	}
	custom, _ := filepath.Abs(filepath.FromSlash("/x/custom"))
	exists[filepath.ToSlash(custom)] = true
	if got, err := findBinary("sing-box", "/x/custom", "/app/splitter", has); err != nil || got != custom {
		t.Fatalf("got %q, %v", got, err)
	}
	// A relative path is resolved against the current directory now: procman
	// starts the child in the run directory, where it would not resolve.
	rel, _ := filepath.Abs(filepath.Join("bin", "sing-box"))
	exists[filepath.ToSlash(rel)] = true
	if got, err := findBinary("sing-box", filepath.Join("bin", "sing-box"), "/app/splitter", has); err != nil || got != rel {
		t.Fatalf("relative: got %q, %v", got, err)
	}
	if _, err := findBinary("sing-box", "", filepath.FromSlash("/app/splitter"), has); err == nil {
		t.Fatal("found a binary that does not exist")
	}
	for _, name := range []string{"sing-box", "sing-box.exe"} {
		exists["/app/"+name] = true
	}
	got, err := findBinary("sing-box", "", filepath.FromSlash("/app/splitter"), has)
	if err != nil || !strings.HasPrefix(filepath.ToSlash(got), "/app/sing-box") {
		t.Fatalf("got %q, %v", got, err)
	}
}

func TestRotator(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "app.log")
	r, err := newRotator(path, 20, 2)
	if err != nil {
		t.Fatal(err)
	}
	for _, line := range []string{"aaaaaaaaaaaa\n", "bbbbbbbbbbbb\n", "cccccccccccc\n", "dddddddddddd\n"} {
		if _, err := r.Write([]byte(line)); err != nil {
			t.Fatal(err)
		}
	}
	if err := r.Close(); err != nil {
		t.Fatal(err)
	}
	read := func(n string) string { b, _ := os.ReadFile(filepath.Join(dir, n)); return string(b) }
	if read("app.log") != "dddddddddddd\n" || read("app.log.1") != "cccccccccccc\n" || read("app.log.2") != "bbbbbbbbbbbb\n" {
		t.Fatalf("files: %q %q %q", read("app.log"), read("app.log.1"), read("app.log.2"))
	}
	if _, err := os.Stat(filepath.Join(dir, "app.log.3")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("more rotated files than max_files")
	}
	if _, err := r.Write([]byte("x")); err == nil {
		t.Fatal("write after close")
	}
}

func TestRouteTypesSurviveYAML(t *testing.T) {
	// UnknownDefault is a route.Route in the checker config; make sure the YAML decoder accepts it.
	cfg, err := ParseConfig(strings.NewReader("checker:\n  unknowndefault: tunnel\n"))
	if err != nil || cfg.Checker.UnknownDefault != route.Tunnel {
		t.Fatalf("err %v, default %q", err, cfg.Checker.UnknownDefault)
	}
}
