package pc

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nikoejento/obhod/internal/lib/netif"
	"github.com/nikoejento/obhod/internal/lib/paths"
)

type fakeRunner struct {
	err error
	ran bool
}

func (f *fakeRunner) Run(context.Context) error { f.ran = true; return f.err }

type cliHarness struct {
	t      *testing.T
	cli    *cli
	out    *bytes.Buffer
	errOut *bytes.Buffer
	p      paths.Paths
	runner *fakeRunner
	opts   int
	cfgIn  Config
	checks []string
	sbErr  error
}

func newCLIHarness(t *testing.T) *cliHarness {
	t.Helper()
	h := &cliHarness{t: t, out: &bytes.Buffer{}, errOut: &bytes.Buffer{}, runner: &fakeRunner{}}
	h.p = paths.FromRoot(t.TempDir())
	h.cli = &cli{
		stdout:       h.out,
		stderr:       h.errOut,
		defaultPaths: func() (paths.Paths, error) { return h.p, nil },
		newApp: func(cfg Config, p paths.Paths, opts ...Option) (runner, error) {
			h.cfgIn, h.opts = cfg, len(opts)
			return h.runner, nil
		},
		signalCtx: func() (context.Context, context.CancelFunc) { return context.WithCancel(context.Background()) },
		check: checkEnv{
			findBinary: func(name, _ string) (string, error) { return "/bin/" + name, nil },
			uplink:     func(context.Context) (netif.Interface, error) { return netif.Interface{Name: "eth0", Index: 2}, nil },
			singCheck: func(_ context.Context, bin, file string) error {
				h.checks = append(h.checks, bin)
				if b, err := os.ReadFile(file); err != nil || !strings.Contains(string(b), "tunnel") {
					return errors.New("no config to check")
				}
				return h.sbErr
			},
		},
	}
	return h
}

func (h *cliHarness) writeConfig(content string) {
	h.t.Helper()
	if err := os.MkdirAll(h.p.Config, 0o700); err != nil {
		h.t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(h.p.Config, "app.yaml"), []byte(content), 0o600); err != nil {
		h.t.Fatal(err)
	}
}

func (h *cliHarness) run(args ...string) int { return h.cli.run(args) }

func TestCLIBasics(t *testing.T) {
	h := newCLIHarness(t)
	if code := h.run("--version"); code != 0 || !strings.HasPrefix(h.out.String(), "splitter") {
		t.Fatalf("--version: %d %q", code, h.out.String())
	}
	h.out.Reset()
	for _, a := range []string{"--help", "-h", "help"} {
		h.out.Reset()
		if code := h.run(a); code != 0 || !strings.Contains(h.out.String(), "check-config") {
			t.Fatalf("%s: %d", a, code)
		}
	}
	if code := h.run(); code != 1 || !strings.Contains(h.errOut.String(), "usage") {
		t.Fatalf("no args: %d", code)
	}
	h.errOut.Reset()
	if code := h.run("frobnicate"); code != 1 || !strings.Contains(h.errOut.String(), "unknown command") {
		t.Fatalf("unknown command: %d", code)
	}
	for _, args := range [][]string{{"run", "--nope"}, {"run", "extra"}, {"run", "--parent-pid", "-3"}, {"check-config", "--ui"}} {
		if code := h.run(args...); code != 1 {
			t.Fatalf("%v: exit %d", args, code)
		}
	}
}

func TestRunExitCodes(t *testing.T) {
	h := newCLIHarness(t)
	h.writeConfig("log:\n  level: info\n")

	if code := h.run("run"); code != 0 || !h.runner.ran || h.opts < 3 {
		t.Fatalf("clean run: exit %d, ran %v", code, h.runner.ran)
	}
	for name, c := range map[string]struct {
		err  error
		want int
	}{
		"config":  {fail(exitConfig, "bad"), 1},
		"manual":  {fail(exitManual, "needs admin"), 2},
		"runtime": {errors.New("boom"), 3},
		"wrapped": {errors.Join(errors.New("x"), fail(exitManual, "y")), 2},
	} {
		h.runner.err = c.err
		h.errOut.Reset()
		if code := h.run("run"); code != c.want {
			t.Errorf("%s: exit %d, want %d", name, code, c.want)
		}
		if h.errOut.Len() == 0 {
			t.Errorf("%s: nothing printed to stderr", name)
		}
	}
}

func TestRunFlagsAndConfigErrors(t *testing.T) {
	h := newCLIHarness(t)
	if code := h.run("run"); code != 1 || !strings.Contains(h.errOut.String(), "app.yaml") {
		t.Fatalf("missing default config: exit %d, %q", code, h.errOut.String())
	}
	h.errOut.Reset()
	h.writeConfig("bogus: 1\n")
	if code := h.run("run"); code != 1 || !strings.Contains(h.errOut.String(), "bogus") {
		t.Fatalf("unknown key: exit %d, %q", code, h.errOut.String())
	}
	other := filepath.Join(t.TempDir(), "other.yaml")
	if err := os.WriteFile(other, []byte("log:\n  level: warn\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if code := h.run("run", "--config", other, "--ui", "--parent-pid", "99"); code != 0 {
		t.Fatalf("explicit config: exit %d, %q", code, h.errOut.String())
	}
	if h.cfgIn.Log.Level != "warn" {
		t.Fatalf("--config ignored: %+v", h.cfgIn.Log)
	}
	// The log file is created under the logs directory.
	if _, err := os.Stat(filepath.Join(h.p.Logs, "splitter.log")); err != nil {
		t.Fatalf("log file: %v", err)
	}
}

const goodTunnel = `{"type":"vless","server":"203.0.113.7","server_port":443,"uuid":"00000000-0000-0000-0000-000000000000"}`

func TestCheckConfig(t *testing.T) {
	h := newCLIHarness(t)
	h.writeConfig("secrets:\n  tunnel: tunnel.json\n")

	if code := h.run("check-config"); code != 1 || !strings.Contains(h.errOut.String(), "tunnel.json") {
		t.Fatalf("missing secret file: exit %d, %q", code, h.errOut.String())
	}
	h.errOut.Reset()

	writeSecret(t, h.p, "tunnel.json", goodTunnel)
	if code := h.run("check-config"); code != 0 {
		t.Fatalf("exit %d: %s", code, h.errOut.String())
	}
	if len(h.checks) != 1 || h.checks[0] != "/bin/sing-box" {
		t.Fatalf("sing-box check calls: %v", h.checks)
	}
	out := h.out.String()
	for _, want := range []string{"sing-box check: ok", "desync plan: engine", "config ok"} {
		if !strings.Contains(out, want) {
			t.Errorf("output lacks %q:\n%s", want, out)
		}
	}
	if strings.Contains(out+h.errOut.String(), "00000000-0000") {
		t.Fatal("secret content printed")
	}

	// sing-box rejects the config.
	h.out.Reset()
	h.sbErr = errors.New("unknown field")
	if code := h.run("check-config"); code != 1 || !strings.Contains(h.errOut.String(), "unknown field") {
		t.Fatalf("rejected config: exit %d, %q", code, h.errOut.String())
	}
}

func TestCheckConfigWithoutBinariesOrNetwork(t *testing.T) {
	h := newCLIHarness(t)
	h.writeConfig("")
	writeSecret(t, h.p, "tunnel.json", goodTunnel)
	h.cli.check.findBinary = func(name, _ string) (string, error) { return "", errors.New(name + " not found") }
	h.cli.check.uplink = func(context.Context) (netif.Interface, error) { return netif.Interface{}, errors.New("offline") }
	if code := h.run("check-config"); code != 0 {
		t.Fatalf("exit %d: %s", code, h.errOut.String())
	}
	out := h.out.String()
	for _, want := range []string{"skipped sing-box check", "no uplink interface", "config ok"} {
		if !strings.Contains(out, want) {
			t.Errorf("output lacks %q:\n%s", want, out)
		}
	}
	if len(h.checks) != 0 {
		t.Fatal("sing-box check ran without a binary")
	}
}

func TestCheckConfigNeedsAnAddressToProtect(t *testing.T) {
	h := newCLIHarness(t)
	h.writeConfig("")
	writeSecret(t, h.p, "tunnel.json", `{"type":"vless","server":"vpn.example.org","server_port":443}`)
	if code := h.run("check-config"); code != 1 || !strings.Contains(h.errOut.String(), "exclude_ips") {
		t.Fatalf("exit %d: %q", code, h.errOut.String())
	}
	h.errOut.Reset()
	h.writeConfig("desync:\n  exclude_ips: [198.51.100.9/32]\n")
	if code := h.run("check-config"); code != 0 {
		t.Fatalf("exit %d: %q", code, h.errOut.String())
	}
}

func TestCheckConfigWithProxy(t *testing.T) {
	h := newCLIHarness(t)
	h.writeConfig("secrets:\n  proxy: proxy.json\n")
	writeSecret(t, h.p, "tunnel.json", goodTunnel)
	if code := h.run("check-config"); code != 1 {
		t.Fatalf("a configured but missing proxy file must fail: exit %d", code)
	}
	writeSecret(t, h.p, "proxy.json", `{"type":"socks","server":"127.0.0.1","server_port":1080}`)
	if code := h.run("check-config"); code != 0 {
		t.Fatalf("exit %d: %s", code, h.errOut.String())
	}
}
