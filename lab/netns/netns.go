//go:build linux

package netns

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"net/netip"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"
)

// NS is a network namespace created by a Topology. Name is the full namespace name
// ("<prefix>-<name>"), as shown by `ip netns list`.
type NS struct{ Name string }

// Topology creates namespaces and links that all carry one prefix, and removes them in Teardown.
//
// An invalid prefix never panics: New stores it, and every method (including Teardown) returns an
// error without running anything.
type Topology struct {
	prefix string
	etcDir string // "/etc/netns"; resolv.conf folders

	mu    sync.Mutex
	procs []*exec.Cmd
}

const (
	defaultEtcDir = "/etc/netns"
	defaultRunDir = "/run/netns"
	cmdTimeout    = 30 * time.Second
	maxIfName     = 15 // IFNAMSIZ - 1
	minPrefix     = 3
	maxPrefix     = 8
	maxName       = 15
	tmpHexLen     = 4
)

// New returns a Topology whose objects all carry prefix: 3–8 characters, a lowercase letter
// followed by lowercase letters or digits (e.g. "obl" + random hex). Namespaces are named
// "<prefix>-<name>". An invalid prefix is reported by every method; New itself never fails.
func New(prefix string) *Topology {
	return &Topology{prefix: prefix, etcDir: defaultEtcDir}
}

// Add creates the namespace "<prefix>-<name>" and brings its loopback up. name follows the same
// rules as the prefix ([a-z][a-z0-9]*), 1–15 characters.
func (t *Topology) Add(name string) (*NS, error) {
	if err := t.check(); err != nil {
		return nil, err
	}
	if err := validName(name); err != nil {
		return nil, err
	}
	full := t.prefix + "-" + name
	if _, err := t.run("ip", "netns", "add", full); err != nil {
		return nil, err
	}
	if _, err := t.run("ip", "-n", full, "link", "set", "lo", "up"); err != nil {
		_, derr := t.run("ip", "netns", "del", full)
		return nil, errors.Join(err, derr)
	}
	return &NS{Name: full}, nil
}

// Link creates a veth pair between a and b and names each end inside its namespace
// (aIf, bIf; e.g. "eth0" on the client side so netif counts it as physical).
// Both ends get their address and are brought up. The pair exists in the root namespace only
// under temporary prefixed names, between creation and the move into a and b; on any failure the
// pair is deleted again.
func (t *Topology) Link(a, b *NS, aIf, bIf string, aAddr, bAddr netip.Prefix) error {
	if err := t.own(a, b); err != nil {
		return err
	}
	if err := validIfName(aIf); err != nil {
		return err
	}
	if err := validIfName(bIf); err != nil {
		return err
	}
	if !aAddr.IsValid() || !bAddr.IsValid() {
		return fmt.Errorf("netns: link %s-%s: invalid address", a.Name, b.Name)
	}
	tmpA, tmpB, err := tempVethNames(t.prefix)
	if err != nil {
		return err
	}
	if _, err := t.run("ip", "link", "add", tmpA, "type", "veth", "peer", "name", tmpB); err != nil {
		return err
	}
	// del removes the pair through one end; deleting one end of a veth deletes both.
	fail := func(err error, del ...string) error {
		_, derr := t.run(del...)
		return errors.Join(err, derr)
	}
	if _, err := t.run("ip", "link", "set", tmpA, "netns", a.Name); err != nil {
		return fail(err, "ip", "link", "del", tmpA)
	}
	if _, err := t.run("ip", "link", "set", tmpB, "netns", b.Name); err != nil {
		return fail(err, "ip", "-n", a.Name, "link", "del", tmpA)
	}
	if _, err := t.run("ip", "-n", a.Name, "link", "set", tmpA, "name", aIf); err != nil {
		return fail(err, "ip", "-n", a.Name, "link", "del", tmpA)
	}
	if _, err := t.run("ip", "-n", b.Name, "link", "set", tmpB, "name", bIf); err != nil {
		return fail(err, "ip", "-n", a.Name, "link", "del", aIf)
	}
	for _, step := range [][]string{
		addrArgv(a.Name, aIf, aAddr),
		addrArgv(b.Name, bIf, bAddr),
		{"ip", "-n", a.Name, "link", "set", aIf, "up"},
		{"ip", "-n", b.Name, "link", "set", bIf, "up"},
	} {
		if _, err := t.run(step...); err != nil {
			return fail(err, "ip", "-n", a.Name, "link", "del", aIf)
		}
	}
	return nil
}

// AddAddr adds an extra address to ifname in ns (one per fake site). IPv6 addresses skip
// duplicate address detection so they are usable at once.
func (t *Topology) AddAddr(ns *NS, ifname string, addr netip.Prefix) error {
	if err := t.own(ns); err != nil {
		return err
	}
	if err := validIfName(ifname); err != nil {
		return err
	}
	if !addr.IsValid() {
		return fmt.Errorf("netns: %s: invalid address", ns.Name)
	}
	_, err := t.run(addrArgv(ns.Name, ifname, addr)...)
	return err
}

// SetLink brings ifname in ns up or down.
func (t *Topology) SetLink(ns *NS, ifname string, up bool) error {
	if err := t.own(ns); err != nil {
		return err
	}
	if err := validIfName(ifname); err != nil {
		return err
	}
	state := "down"
	if up {
		state = "up"
	}
	_, err := t.run("ip", "-n", ns.Name, "link", "set", ifname, state)
	return err
}

// Route sets the route to dst via the gateway via in ns, replacing an existing route to dst
// (so switching the default route is one call). dst and via must be of the same family.
func (t *Topology) Route(ns *NS, dst netip.Prefix, via netip.Addr) error {
	if err := t.own(ns); err != nil {
		return err
	}
	if !dst.IsValid() || !via.IsValid() || dst.Addr().Is4() != via.Unmap().Is4() {
		return fmt.Errorf("netns: %s: route %v via %v: invalid or mixed families", ns.Name, dst, via)
	}
	_, err := t.run("ip", "-n", ns.Name, "route", "replace", dst.Masked().String(), "via", via.String())
	return err
}

// DelRoute deletes the route to dst in ns.
func (t *Topology) DelRoute(ns *NS, dst netip.Prefix) error {
	if err := t.own(ns); err != nil {
		return err
	}
	if !dst.IsValid() {
		return fmt.Errorf("netns: %s: invalid route destination", ns.Name)
	}
	_, err := t.run("ip", "-n", ns.Name, "route", "del", dst.Masked().String())
	return err
}

// Forwarding turns IPv4 and IPv6 forwarding of ns on or off. Both sysctls are per network
// namespace, so the root namespace is never affected.
func (t *Topology) Forwarding(ns *NS, on bool) error {
	if err := t.own(ns); err != nil {
		return err
	}
	v := "0"
	if on {
		v = "1"
	}
	_, err := t.run("ip", "netns", "exec", ns.Name, "sysctl", "-w",
		"net.ipv4.ip_forward="+v, "net.ipv6.conf.all.forwarding="+v)
	return err
}

// SetResolver writes /etc/netns/<ns>/resolv.conf with "nameserver <addr>"; `ip netns exec`
// bind-mounts it over /etc/resolv.conf for processes started in ns. Teardown removes the folder.
func (t *Topology) SetResolver(ns *NS, addr netip.Addr) error {
	if err := t.own(ns); err != nil {
		return err
	}
	if !addr.IsValid() {
		return fmt.Errorf("netns: %s: invalid resolver address", ns.Name)
	}
	dir, err := t.etcPath(ns.Name)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("netns: %w", err)
	}
	if err := os.Chmod(dir, 0o755); err != nil {
		return fmt.Errorf("netns: %w", err)
	}
	file := filepath.Join(dir, "resolv.conf")
	if err := os.WriteFile(file, []byte("nameserver "+addr.String()+"\n"), 0o644); err != nil {
		return fmt.Errorf("netns: %w", err)
	}
	if err := os.Chmod(file, 0o644); err != nil {
		return fmt.Errorf("netns: %w", err)
	}
	return nil
}

// Exec runs argv in ns (ip netns exec) and returns stdout; Start runs a long-lived process there.
func (t *Topology) Exec(ctx context.Context, ns *NS, argv ...string) ([]byte, error) {
	if err := t.own(ns); err != nil {
		return nil, err
	}
	if len(argv) == 0 {
		return nil, errors.New("netns: exec: empty argv")
	}
	return runCmd(ctx, append([]string{"ip", "netns", "exec", ns.Name}, argv...))
}

// Start runs argv in ns (ip netns exec) as a long-lived process in its own process group, with
// env added to the current environment, and returns the started command. Stdin, stdout and
// stderr are not connected (redirect inside argv, e.g. through "sh -c", to keep output).
// The caller may Wait for it. When ctx is done, or in Teardown if it is still running, the whole
// process group is killed; it is also killed if the parent dies (Pdeathsig).
func (t *Topology) Start(ctx context.Context, ns *NS, env []string, argv ...string) (*exec.Cmd, error) {
	if err := t.own(ns); err != nil {
		return nil, err
	}
	if len(argv) == 0 {
		return nil, errors.New("netns: start: empty argv")
	}
	full := append([]string{"ip", "netns", "exec", ns.Name}, argv...)
	cmd := exec.CommandContext(ctx, full[0], full[1:]...)
	cmd.Env = append(os.Environ(), env...)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true, Pdeathsig: syscall.SIGKILL}
	cmd.Cancel = func() error { return killGroup(cmd) }
	t.mu.Lock()
	defer t.mu.Unlock()
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("netns: %s: %w", strings.Join(full, " "), err)
	}
	t.procs = append(t.procs, cmd)
	return cmd, nil
}

// Teardown kills the processes from Start that are still running, deletes every namespace named
// "<prefix>-<name>" (also leftovers of crashed runs with the same prefix) and removes
// /etc/netns/<prefix>-<name> folders. It is idempotent and keeps going after an error; the
// errors are joined.
func (t *Topology) Teardown() error {
	if err := t.check(); err != nil {
		return err
	}
	var errs []error

	t.mu.Lock()
	procs := t.procs
	t.procs = nil
	t.mu.Unlock()
	for _, cmd := range procs {
		if err := killGroup(cmd); err != nil {
			errs = append(errs, err)
		}
	}

	out, err := t.run("ip", "netns", "list")
	if err != nil {
		errs = append(errs, err)
	} else {
		for _, name := range ownedNamespaces(out, t.prefix) {
			if _, err := t.run("ip", "netns", "del", name); err != nil {
				errs = append(errs, err)
			}
		}
	}

	// Temporary veth ends left in the root namespace by a crash inside Link. Listed after the
	// namespaces are gone, since deleting a namespace also destroys veths with an end inside it.
	out, err = t.run("ip", "-o", "link", "show", "type", "veth")
	if err != nil {
		errs = append(errs, err)
	} else {
		for _, name := range ownedTempLinks(out, t.prefix) {
			if _, err := t.run("ip", "link", "del", name); err != nil {
				errs = append(errs, err)
			}
		}
	}

	if err := removeEtcLeftovers(t.etcDir, t.prefix); err != nil {
		errs = append(errs, err)
	}
	return errors.Join(errs...)
}

// killGroup sends SIGKILL to the process group of a started command unless it was already waited
// for (then its pid may belong to someone else). An unwaited child keeps its pid as a zombie, so
// the group id cannot be reused before the signal is sent.
func killGroup(cmd *exec.Cmd) error {
	p := cmd.Process
	if p == nil || p.Pid <= 1 { // kill(-0) or kill(-1) would hit far more than this group
		return nil
	}
	if err := p.Signal(syscall.Signal(0)); errors.Is(err, os.ErrProcessDone) {
		return nil
	}
	if err := syscall.Kill(-p.Pid, syscall.SIGKILL); err != nil && !errors.Is(err, syscall.ESRCH) {
		return fmt.Errorf("netns: kill process group %d: %w", p.Pid, err)
	}
	return nil
}

// check refuses a Topology with an invalid prefix, including a zero Topology not made by New.
func (t *Topology) check() error {
	if t == nil || t.etcDir == "" {
		return errors.New("netns: Topology not created by New")
	}
	return validPrefix(t.prefix)
}

// own checks that every ns is non-nil and belongs to this topology's prefix.
func (t *Topology) own(nss ...*NS) error {
	if err := t.check(); err != nil {
		return err
	}
	for _, ns := range nss {
		if ns == nil {
			return errors.New("netns: nil namespace")
		}
		p, _, err := splitFullName(ns.Name)
		if err != nil {
			return err
		}
		if p != t.prefix {
			return fmt.Errorf("netns: namespace %q does not carry prefix %q", ns.Name, t.prefix)
		}
	}
	return nil
}

// etcPath returns /etc/netns/<full> for a valid full name of this topology's prefix.
func (t *Topology) etcPath(full string) (string, error) {
	return etcPath(t.etcDir, t.prefix, full)
}

// etcPath builds dir/<full>, refusing any name that is not "<prefix>-<valid name>", so the result
// is always strictly below dir.
func etcPath(dir, prefix, full string) (string, error) {
	if err := validPrefix(prefix); err != nil {
		return "", err
	}
	p, _, err := splitFullName(full)
	if err != nil {
		return "", err
	}
	if p != prefix {
		return "", fmt.Errorf("netns: %q does not carry prefix %q", full, prefix)
	}
	return filepath.Join(dir, full), nil
}

// removeEtcLeftovers removes dir/<prefix>-<name> folders. A missing dir is not an error.
func removeEtcLeftovers(dir, prefix string) error {
	entries, err := os.ReadDir(dir)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("netns: %w", err)
	}
	var errs []error
	for _, e := range entries {
		path, err := etcPath(dir, prefix, e.Name())
		if err != nil {
			continue // not ours
		}
		if err := os.RemoveAll(path); err != nil {
			errs = append(errs, fmt.Errorf("netns: %w", err))
		}
	}
	return errors.Join(errs...)
}

// ownedNamespaces parses `ip netns list` output ("name" or "name (id: 3)" per line) and returns
// the names that are exactly "<prefix>-<valid name>".
func ownedNamespaces(out []byte, prefix string) []string {
	var names []string
	for _, line := range strings.Split(string(out), "\n") {
		f := strings.Fields(line)
		if len(f) == 0 {
			continue
		}
		p, _, err := splitFullName(f[0])
		if err == nil && p == prefix {
			names = append(names, f[0])
		}
	}
	return names
}

// ownedTempLinks parses `ip -o link show` output ("5: name@peer: <...> ..." per line) and returns
// the temporary veth names of this prefix ("<prefix><4 hex>a" or "b"). When both ends of a pair
// are listed only the "a" end is returned, because deleting one end deletes the pair.
func ownedTempLinks(out []byte, prefix string) []string {
	var found []string
	seen := map[string]bool{}
	for _, line := range strings.Split(string(out), "\n") {
		f := strings.Fields(line)
		if len(f) < 2 {
			continue
		}
		name, _, _ := strings.Cut(strings.TrimSuffix(f[1], ":"), "@")
		if isTempVethName(name, prefix) && !seen[name] {
			seen[name] = true
			found = append(found, name)
		}
	}
	var names []string
	for _, n := range found {
		if strings.HasSuffix(n, "b") && seen[strings.TrimSuffix(n, "b")+"a"] {
			continue
		}
		names = append(names, n)
	}
	return names
}

// isTempVethName reports whether name is exactly "<prefix><4 lowercase hex>a" or "...b".
func isTempVethName(name, prefix string) bool {
	if validPrefix(prefix) != nil || len(name) != len(prefix)+tmpHexLen+1 || !strings.HasPrefix(name, prefix) {
		return false
	}
	for _, c := range name[len(prefix) : len(name)-1] {
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return false
		}
	}
	end := name[len(name)-1]
	return end == 'a' || end == 'b'
}

// splitFullName splits "<prefix>-<name>" and validates both parts.
func splitFullName(full string) (prefix, name string, err error) {
	prefix, name, ok := strings.Cut(full, "-")
	if !ok {
		return "", "", fmt.Errorf("netns: namespace name %q: want <prefix>-<name>", full)
	}
	if err := validPrefix(prefix); err != nil {
		return "", "", err
	}
	if err := validName(name); err != nil {
		return "", "", err
	}
	return prefix, name, nil
}

// tempVethNames returns two random root-namespace names for a new veth pair:
// "<prefix><4 hex>a" and "...b", at most 8+4+1 = 13 characters.
func tempVethNames(prefix string) (string, string, error) {
	if err := validPrefix(prefix); err != nil {
		return "", "", err
	}
	var b [tmpHexLen / 2]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", "", fmt.Errorf("netns: %w", err)
	}
	base := prefix + hex.EncodeToString(b[:])
	return base + "a", base + "b", nil
}

func validPrefix(s string) error {
	if len(s) < minPrefix || len(s) > maxPrefix || !isIdent(s) {
		return fmt.Errorf("netns: invalid prefix %q: want %d-%d chars [a-z][a-z0-9]*", s, minPrefix, maxPrefix)
	}
	return nil
}

func validName(s string) error {
	if len(s) < 1 || len(s) > maxName || !isIdent(s) {
		return fmt.Errorf("netns: invalid namespace name %q: want 1-%d chars [a-z][a-z0-9]*", s, maxName)
	}
	return nil
}

func validIfName(s string) error {
	if len(s) < 1 || len(s) > maxIfName || !isIdent(s) {
		return fmt.Errorf("netns: invalid interface name %q: want 1-%d chars [a-z][a-z0-9]*", s, maxIfName)
	}
	return nil
}

// isIdent reports whether s matches ^[a-z][a-z0-9]*$.
func isIdent(s string) bool {
	if s == "" || s[0] < 'a' || s[0] > 'z' {
		return false
	}
	for i := 1; i < len(s); i++ {
		c := s[i]
		if (c < 'a' || c > 'z') && (c < '0' || c > '9') {
			return false
		}
	}
	return true
}

func addrArgv(ns, ifname string, addr netip.Prefix) []string {
	argv := []string{"ip", "-n", ns, "addr", "add", addr.String(), "dev", ifname}
	if addr.Addr().Is6() && !addr.Addr().Is4In6() {
		argv = append(argv, "nodad")
	}
	return argv
}

// run runs a short command with a timeout.
func (t *Topology) run(argv ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(context.Background(), cmdTimeout)
	defer cancel()
	return runCmd(ctx, argv)
}

// runCmd runs argv and returns stdout; errors include argv and the trimmed stderr.
func runCmd(ctx context.Context, argv []string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		msg := strings.TrimSpace(stderr.String())
		if msg == "" {
			return stdout.Bytes(), fmt.Errorf("netns: %s: %w", strings.Join(argv, " "), err)
		}
		return stdout.Bytes(), fmt.Errorf("netns: %s: %w: %s", strings.Join(argv, " "), err, msg)
	}
	return stdout.Bytes(), nil
}
