//go:build linux

package netns

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"net"
	"net/netip"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"
)

// --- pure tests (no root) ---

func TestValidation(t *testing.T) {
	for _, p := range []string{"obl", "obt1a2b", "abcdefgh"} {
		if err := validPrefix(p); err != nil {
			t.Errorf("validPrefix(%q) = %v", p, err)
		}
	}
	for _, p := range []string{"", "ob", "abcdefghi", "1ab", "Obl", "ob-l", "ob_l", "ob l", "obl/", "../x", "obé"} {
		if validPrefix(p) == nil {
			t.Errorf("validPrefix(%q) accepted", p)
		}
	}
	for _, n := range []string{"a", "client", "ispa", "inet", "abcdefghijklmno"} {
		if err := validName(n); err != nil {
			t.Errorf("validName(%q) = %v", n, err)
		}
	}
	for _, n := range []string{"", "isp-a", "1a", "A", "a/b", "..", "abcdefghijklmnop"} {
		if validName(n) == nil {
			t.Errorf("validName(%q) accepted", n)
		}
	}
	for _, n := range []string{"eth0", "lo", "e", "abcdefghijklmno"} {
		if err := validIfName(n); err != nil {
			t.Errorf("validIfName(%q) = %v", n, err)
		}
	}
	for _, n := range []string{"", "0eth", "eth-0", "veth0@x", "abcdefghijklmnop", "eth 0"} {
		if validIfName(n) == nil {
			t.Errorf("validIfName(%q) accepted", n)
		}
	}
}

func TestInvalidPrefixRefusesEverything(t *testing.T) {
	topo := New("bad-prefix")
	if _, err := topo.Add("a"); err == nil {
		t.Error("Add accepted an invalid prefix")
	}
	ns := &NS{Name: "bad-prefix-a"}
	if err := topo.Link(ns, ns, "eth0", "eth1", netip.MustParsePrefix("10.0.0.1/24"), netip.MustParsePrefix("10.0.0.2/24")); err == nil {
		t.Error("Link accepted an invalid prefix")
	}
	if err := topo.Teardown(); err == nil {
		t.Error("Teardown accepted an invalid prefix")
	}
	var zero Topology
	if _, err := zero.Add("a"); err == nil {
		t.Error("zero Topology: Add accepted")
	}
	if err := zero.Teardown(); err == nil {
		t.Error("zero Topology: Teardown accepted")
	}
}

func TestOwnRefusesForeignNamespaces(t *testing.T) {
	topo := New("obt")
	for _, name := range []string{"obx-a", "obt", "obt-", "obt-a-b", "obta-a", "", "obt-../x"} {
		if err := topo.own(&NS{Name: name}); err == nil {
			t.Errorf("own(%q) accepted", name)
		}
		if _, err := topo.Exec(context.Background(), &NS{Name: name}, "true"); err == nil {
			t.Errorf("Exec(%q) accepted", name)
		}
		if err := topo.SetResolver(&NS{Name: name}, netip.MustParseAddr("192.0.2.1")); err == nil {
			t.Errorf("SetResolver(%q) accepted", name)
		}
	}
	if err := topo.own(nil); err == nil {
		t.Error("own(nil) accepted")
	}
	if err := topo.own(&NS{Name: "obt-client"}); err != nil {
		t.Errorf("own(obt-client) = %v", err)
	}
}

func TestOwnedNamespaces(t *testing.T) {
	out := []byte("obt-client (id: 3)\nobt-ispa (id: 2)\nobta-x (id: 1)\nobx-client\nobt\nobt-\nobt-bad-name (id: 9)\nother\n\n  obt-inet  \nobt-vps\n")
	got := ownedNamespaces(out, "obt")
	want := []string{"obt-client", "obt-ispa", "obt-inet", "obt-vps"}
	if !slices.Equal(got, want) {
		t.Errorf("ownedNamespaces = %q, want %q", got, want)
	}
	if got := ownedNamespaces(nil, "obt"); len(got) != 0 {
		t.Errorf("empty output: %q", got)
	}
}

func TestOwnedTempLinks(t *testing.T) {
	out := []byte(strings.Join([]string{
		"1: lo: <LOOPBACK,UP,LOWER_UP> mtu 65536",
		"5: obt1a2ba@obt1a2bb: <BROADCAST> mtu 1500",
		"6: obt1a2bb@obt1a2ba: <BROADCAST> mtu 1500",
		"7: obtffffb@if9: <BROADCAST> mtu 1500",
		"8: obt12345a@x: <BROADCAST>",
		"9: obtzzzza: <BROADCAST>",
		"10: obx1a2ba@y: <BROADCAST>",
		"11: veth0@if2: <BROADCAST>",
		"12: obt1A2Ba: <BROADCAST>",
	}, "\n"))
	got := ownedTempLinks(out, "obt")
	want := []string{"obt1a2ba", "obtffffb"}
	if !slices.Equal(got, want) {
		t.Errorf("ownedTempLinks = %q, want %q", got, want)
	}
}

func TestEtcPath(t *testing.T) {
	dir := t.TempDir()
	got, err := etcPath(dir, "obt", "obt-client")
	if err != nil || got != filepath.Join(dir, "obt-client") {
		t.Errorf("etcPath = %q, %v", got, err)
	}
	for _, full := range []string{"", ".", "..", "obt", "obt-", "obt-..", "obt-a/b", "obt-../..", "obx-client", "obta-client", "/", "obt-a b"} {
		if p, err := etcPath(dir, "obt", full); err == nil {
			t.Errorf("etcPath(%q) = %q, want error", full, p)
		}
	}
	if _, err := etcPath(dir, "", "-client"); err == nil {
		t.Error("etcPath accepted an empty prefix")
	}
}

func TestRemoveEtcLeftovers(t *testing.T) {
	dir := t.TempDir()
	mk := func(name string) {
		if err := os.MkdirAll(filepath.Join(dir, name), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, name, "resolv.conf"), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	for _, n := range []string{"obt-a", "obt-client", "obta-a", "obx-a", "obt", "obt-bad-name", "keep"} {
		mk(n)
	}
	if err := os.WriteFile(filepath.Join(dir, "obt-file"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := removeEtcLeftovers(dir, "obt"); err != nil {
		t.Fatal(err)
	}
	if err := removeEtcLeftovers(dir, "obt"); err != nil {
		t.Fatalf("second run: %v", err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var left []string
	for _, e := range entries {
		left = append(left, e.Name())
	}
	want := []string{"keep", "obt", "obt-bad-name", "obta-a", "obx-a"}
	if !slices.Equal(left, want) {
		t.Errorf("left %q, want %q", left, want)
	}
	if _, err := os.Stat(dir); err != nil {
		t.Errorf("dir itself removed: %v", err)
	}
	if err := removeEtcLeftovers(filepath.Join(dir, "missing"), "obt"); err != nil {
		t.Errorf("missing dir: %v", err)
	}
	if err := removeEtcLeftovers(dir, "bad-"); err != nil {
		t.Errorf("invalid prefix: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "keep")); err != nil {
		t.Errorf("invalid prefix removed something: %v", err)
	}
}

func TestTempVethNames(t *testing.T) {
	for _, p := range []string{"obl", "abcdefgh"} {
		a, b, err := tempVethNames(p)
		if err != nil {
			t.Fatal(err)
		}
		for _, n := range []string{a, b} {
			if len(n) > maxIfName || !strings.HasPrefix(n, p) || !isTempVethName(n, p) || validIfName(n) != nil {
				t.Errorf("temp name %q (prefix %q) invalid", n, p)
			}
		}
		if a[:len(a)-1] != b[:len(b)-1] || !strings.HasSuffix(a, "a") || !strings.HasSuffix(b, "b") {
			t.Errorf("names %q %q not a pair", a, b)
		}
	}
	if _, _, err := tempVethNames("x"); err == nil {
		t.Error("tempVethNames accepted an invalid prefix")
	}
}

func TestRunCmdErrorHasArgvAndStderr(t *testing.T) {
	if _, err := exec.LookPath("sh"); err != nil {
		t.Skip("no sh")
	}
	_, err := runCmd(context.Background(), []string{"sh", "-c", "echo boom >&2; exit 3"})
	if err == nil || !strings.Contains(err.Error(), "sh -c echo boom >&2; exit 3") || !strings.Contains(err.Error(), "boom") {
		t.Errorf("err = %v", err)
	}
}

// --- root tests ---

// rootSnapshot is what must not change in the root namespace.
type rootSnapshot struct {
	route, route6, nft string
	links              []string
}

func takeSnapshot(t *testing.T, ignorePrefix string) rootSnapshot {
	t.Helper()
	run := func(argv ...string) string {
		out, err := runCmd(context.Background(), argv)
		if err != nil {
			t.Fatalf("snapshot: %v", err)
		}
		return string(out)
	}
	s := rootSnapshot{route: run("ip", "route"), route6: run("ip", "-6", "route")}
	if _, err := exec.LookPath("nft"); err == nil {
		s.nft = run("nft", "list", "ruleset")
	}
	for _, line := range strings.Split(run("ip", "-o", "link", "show"), "\n") {
		f := strings.Fields(line)
		if len(f) < 2 {
			continue
		}
		name, _, _ := strings.Cut(strings.TrimSuffix(f[1], ":"), "@")
		// Transient temp veths of other lab packages running in parallel.
		if isOtherLabTemp(name, ignorePrefix) {
			continue
		}
		s.links = append(s.links, name)
	}
	slices.Sort(s.links)
	return s
}

// isOtherLabTemp reports a temp veth name of another test prefix ("obt"/"obl" + 4 hex + 4 hex + a|b).
func isOtherLabTemp(name, own string) bool {
	if len(name) < tmpHexLen+1+3 || strings.HasPrefix(name, own) {
		return false
	}
	p := name[:len(name)-tmpHexLen-1]
	return (strings.HasPrefix(p, "obt") || strings.HasPrefix(p, "obl")) && isTempVethName(name, p)
}

func randHex(t *testing.T, n int) string {
	b := make([]byte, n/2)
	if _, err := rand.Read(b); err != nil {
		t.Fatal(err)
	}
	return hex.EncodeToString(b)
}

// newRootTopology skips without root or ip, and returns a Topology with a unique prefix whose
// Teardown runs in cleanup, followed by a check that the root namespace is unchanged.
func newRootTopology(t *testing.T) *Topology {
	t.Helper()
	if os.Geteuid() != 0 {
		t.Skip("needs root")
	}
	if _, err := exec.LookPath("ip"); err != nil {
		t.Skip("needs ip (iproute2)")
	}
	prefix := "obt" + randHex(t, 4)
	before := takeSnapshot(t, prefix)
	topo := New(prefix)
	t.Cleanup(func() {
		if err := topo.Teardown(); err != nil {
			t.Errorf("Teardown: %v", err)
		}
		out, err := runCmd(context.Background(), []string{"ip", "netns", "list"})
		if err != nil {
			t.Errorf("ip netns list: %v", err)
		} else if left := ownedNamespaces(out, prefix); len(left) != 0 {
			t.Errorf("namespaces left: %q", left)
		}
		after := takeSnapshot(t, prefix)
		if before.route != after.route {
			t.Errorf("root ip route changed:\n%s\n---\n%s", before.route, after.route)
		}
		if before.route6 != after.route6 {
			t.Errorf("root ip -6 route changed:\n%s\n---\n%s", before.route6, after.route6)
		}
		if before.nft != after.nft {
			t.Errorf("root nft ruleset changed:\n%s\n---\n%s", before.nft, after.nft)
		}
		if !slices.Equal(before.links, after.links) {
			t.Errorf("root links changed: %q -> %q", before.links, after.links)
		}
	})
	return topo
}

func linkedPair(t *testing.T, topo *Topology) (a, b *NS) {
	t.Helper()
	a, err := topo.Add("a")
	if err != nil {
		t.Fatal(err)
	}
	b, err = topo.Add("b")
	if err != nil {
		t.Fatal(err)
	}
	if err := topo.Link(a, b, "eth0", "eth0", netip.MustParsePrefix("192.168.77.1/24"), netip.MustParsePrefix("192.168.77.2/24")); err != nil {
		t.Fatal(err)
	}
	return a, b
}

func TestRootLinkPing(t *testing.T) {
	topo := newRootTopology(t)
	if _, err := exec.LookPath("ping"); err != nil {
		t.Skip("needs ping")
	}
	a, b := linkedPair(t, topo)
	ctx := context.Background()
	if out, err := topo.Exec(ctx, a, "ping", "-c1", "-W2", "192.168.77.2"); err != nil {
		t.Fatalf("ping a->b: %v\n%s", err, out)
	}
	if err := topo.AddAddr(b, "eth0", netip.MustParsePrefix("203.0.113.10/32")); err != nil {
		t.Fatal(err)
	}
	if err := topo.Route(a, netip.MustParsePrefix("203.0.113.0/24"), netip.MustParseAddr("192.168.77.2")); err != nil {
		t.Fatal(err)
	}
	if out, err := topo.Exec(ctx, a, "ping", "-c1", "-W2", "203.0.113.10"); err != nil {
		t.Fatalf("ping a->routed address: %v\n%s", err, out)
	}
	if err := topo.DelRoute(a, netip.MustParsePrefix("203.0.113.0/24")); err != nil {
		t.Fatal(err)
	}
	if _, err := topo.Exec(ctx, a, "ping", "-c1", "-W1", "203.0.113.10"); err == nil {
		t.Error("ping after DelRoute succeeded")
	}
	if err := topo.Forwarding(b, true); err != nil {
		t.Fatal(err)
	}
	if out, err := topo.Exec(ctx, b, "sysctl", "-n", "net.ipv4.ip_forward"); err != nil || strings.TrimSpace(string(out)) != "1" {
		t.Errorf("forwarding in b: %q, %v", out, err)
	}
	if err := topo.SetLink(b, "eth0", false); err != nil {
		t.Fatal(err)
	}
	if _, err := topo.Exec(ctx, a, "ping", "-c1", "-W1", "192.168.77.2"); err == nil {
		t.Error("ping with b's link down succeeded")
	}
	// Errors carry argv and stderr.
	err := topo.SetLink(b, "nosuchif", true)
	if err == nil || !strings.Contains(err.Error(), "ip -n "+b.Name+" link set nosuchif up") {
		t.Errorf("error without argv: %v", err)
	}
}

func TestRootDoListener(t *testing.T) {
	topo := newRootTopology(t)
	a, b := linkedPair(t, topo)
	var ln net.Listener
	if err := a.Do(func() error {
		var err error
		ln, err = net.Listen("tcp", "0.0.0.0:0")
		return err
	}); err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			c.Close()
		}
	}()
	port := strconv.Itoa(ln.Addr().(*net.TCPAddr).Port)

	if err := b.Do(func() error {
		c, err := net.DialTimeout("tcp", net.JoinHostPort("192.168.77.1", port), 2*time.Second)
		if err != nil {
			return err
		}
		return c.Close()
	}); err != nil {
		t.Errorf("dial from b: %v", err)
	}
	if c, err := net.DialTimeout("tcp", net.JoinHostPort("127.0.0.1", port), 500*time.Millisecond); err == nil {
		c.Close()
		t.Error("listener in a reachable from the root namespace")
	}
	// The thread was switched back: this goroutine still sees the root namespace's loopback only.
	if err := b.Do(func() error {
		c, err := net.DialTimeout("tcp", net.JoinHostPort("127.0.0.1", port), 500*time.Millisecond)
		if err == nil {
			c.Close()
			return errors.New("listener in a reachable on b's loopback")
		}
		return nil
	}); err != nil {
		t.Error(err)
	}
	if err := (&NS{Name: topo.prefix + "-missing"}).Do(func() error { return nil }); err == nil {
		t.Error("Do in a missing namespace succeeded")
	}
}

func TestRootResolverStartLeftovers(t *testing.T) {
	topo := newRootTopology(t)
	a, err := topo.Add("a")
	if err != nil {
		t.Fatal(err)
	}
	if err := topo.SetResolver(a, netip.MustParseAddr("192.168.77.2")); err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(defaultEtcDir, a.Name)
	got, err := os.ReadFile(filepath.Join(dir, "resolv.conf"))
	if err != nil || string(got) != "nameserver 192.168.77.2\n" {
		t.Fatalf("resolv.conf = %q, %v", got, err)
	}
	if out, err := topo.Exec(context.Background(), a, "cat", "/etc/resolv.conf"); err != nil || string(out) != string(got) {
		t.Errorf("resolv.conf inside ns = %q, %v", out, err)
	}

	cmd, err := topo.Start(context.Background(), a, []string{"OBT_TEST=1"}, "sleep", "60")
	if err != nil {
		t.Fatal(err)
	}
	waited := make(chan error, 1)
	go func() { waited <- cmd.Wait() }()

	// A leftover with the same prefix and an unrelated namespace with another prefix.
	left := topo.prefix + "-left"
	other := "obx" + randHex(t, 4) + "-keep"
	for _, n := range []string{left, other} {
		if _, err := runCmd(context.Background(), []string{"ip", "netns", "add", n}); err != nil {
			t.Fatal(err)
		}
	}
	t.Cleanup(func() { runCmd(context.Background(), []string{"ip", "netns", "del", other}) })
	if err := os.MkdirAll(filepath.Join(defaultEtcDir, left), 0o755); err != nil {
		t.Fatal(err)
	}

	if err := topo.Teardown(); err != nil {
		t.Fatal(err)
	}
	if err := topo.Teardown(); err != nil {
		t.Fatalf("second Teardown: %v", err)
	}
	select {
	case <-waited:
	case <-time.After(5 * time.Second):
		t.Error("process from Start still running after Teardown")
	}
	for _, d := range []string{dir, filepath.Join(defaultEtcDir, left)} {
		if _, err := os.Stat(d); !errors.Is(err, os.ErrNotExist) {
			t.Errorf("%s still exists: %v", d, err)
		}
	}
	out, err := runCmd(context.Background(), []string{"ip", "netns", "list"})
	if err != nil {
		t.Fatal(err)
	}
	names := ownedNamespaces(out, topo.prefix)
	if len(names) != 0 {
		t.Errorf("namespaces left: %q", names)
	}
	if !slices.Contains(strings.Fields(string(out)), other) {
		t.Errorf("namespace %s with another prefix was removed", other)
	}
}
