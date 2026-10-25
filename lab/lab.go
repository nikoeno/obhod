//go:build linux && lab

package lab

import (
	"context"
	"crypto/tls"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nikoejento/obhod/internal/lib/faultserver"
	"github.com/nikoejento/obhod/internal/lib/testca"
	"github.com/nikoejento/obhod/lab/fakeasn"
	"github.com/nikoejento/obhod/lab/fakedns"
	"github.com/nikoejento/obhod/lab/fakedpi"
	"github.com/nikoejento/obhod/lab/netns"
)

// Lab is one running fake internet (see addrs.go for the address plan). Its methods fail the test
// that called Up on a setup error, so call them from that test's goroutine.
type Lab struct {
	t      *testing.T
	topo   *netns.Topology
	prefix string
	tmp    string // t.TempDir: configs, the CA file, Exec stderr files
	logDir string // <run dir>/<test>-<prefix>: process logs

	cli, inet, vps *netns.NS
	isps           []*ispState
	byName         map[string]*ispState

	ctx    context.Context // cancelled first in teardown: servers and queues stop
	cancel context.CancelFunc

	ca     *testca.CA
	caFile string

	mu        sync.Mutex
	current   string
	sites     map[string]Site
	zone      map[string][]netip.Addr
	nextSite  int
	doh       *fakedns.Server
	public    *fakedns.Server // plain resolver for the VPS
	echo      *fakeasn.Server
	vpsP      vpsParams
	singBox   string // pinned sing-box path (the VPS runs a copy; see startVPS)
	splitters int    // RunSplitter calls so far, for file names
}

// ispState is one fake ISP namespace with its fake TSPU and resolver.
type ispState struct {
	p       Profile
	idx     int
	ns      *netns.NS
	cliIf   string     // eth<idx> in cli
	gw      netip.Addr // 192.168.<10(idx+1)>.1: the ISP's lan side and resolver
	cliAddr netip.Addr // 192.168.<10(idx+1)>.2
	public  netip.Addr // 203.0.113.<4idx+1>: the SNAT address (ClientAddr)
	inetGW  netip.Addr // 203.0.113.<4idx+2>
	eng     *fakedpi.Engine
	q       *fakedpi.Queue
	runDone chan error
	dns     *fakedns.Server
	stubs   []*faultserver.Server // the ISP's block page on each poisoned stub address (:80)
}

// Site is a fake site in inet: its address (port 443) and the server with its connection log.
type Site struct {
	Addr   netip.AddrPort
	Server *faultserver.Server // Conns: who connected, with which SNI
}

// Up builds the lab for profiles (the first one is the client's ISP; with none, the profiles in
// lab/profiles are loaded). It skips the test when Preflight fails, removes namespaces of crashed
// runs (obl<4 hex>-*), and registers the teardown with t.Cleanup.
func Up(t *testing.T, profiles ...Profile) *Lab {
	t.Helper()
	if err := Preflight(); err != nil {
		t.Skip(err)
	}
	if len(profiles) == 0 {
		root, err := repoRoot()
		if err != nil {
			t.Fatal(err)
		}
		if profiles, err = LoadProfiles(filepath.Join(root, "lab", "profiles")); err != nil {
			t.Fatal(err)
		}
	}
	if err := validateProfiles(profiles); err != nil {
		t.Fatal(err)
	}
	hexs, err := randHex(2)
	if err != nil {
		t.Fatal(err)
	}
	prefix := labPrefix + hexs
	labRun.setActive(prefix, true)
	t.Cleanup(func() { labRun.setActive(prefix, false) }) // runs after the teardown below
	removeLeftovers(t)
	dir, err := labRun.runDir()
	if err != nil {
		t.Fatal(err)
	}
	l := &Lab{
		t:        t,
		prefix:   prefix,
		tmp:      t.TempDir(), // before the teardown cleanup, so it is removed after it
		byName:   map[string]*ispState{},
		sites:    map[string]Site{},
		zone:     map[string][]netip.Addr{},
		nextSite: siteFirst,
	}
	l.logDir = filepath.Join(dir, unsafeName.ReplaceAllString(t.Name(), "_")+"-"+l.prefix)
	if err := os.MkdirAll(l.logDir, 0o755); err != nil {
		t.Fatal(err)
	}
	l.topo = netns.New(l.prefix)
	l.ctx, l.cancel = context.WithCancel(context.Background())
	t.Cleanup(l.teardown)

	l.buildTopology(profiles)
	l.startCA()
	l.startInet()
	for _, s := range l.isps {
		l.startISP(s)
	}
	// The Reality handshake target must exist before the VPS starts.
	l.AddSite(realityHost, faultserver.Spec{Behavior: faultserver.OK, Bytes: 1024})
	l.startVPS()
	l.UseISP(profiles[0].Name)
	t.Logf("lab %s up; logs in %s", l.prefix, l.logDir)
	return l
}

func (l *Lab) fatal(format string, args ...any) {
	l.t.Helper()
	l.t.Fatalf("lab %s: "+format, append([]any{l.prefix}, args...)...)
}

func (l *Lab) must(err error) {
	l.t.Helper()
	if err != nil {
		l.fatal("%v", err)
	}
}

func pfx(s string) netip.Prefix { return netip.MustParsePrefix(s) }
func addr(s string) netip.Addr  { return netip.MustParseAddr(s) }

func (l *Lab) buildTopology(profiles []Profile) {
	l.t.Helper()
	var err error
	l.cli, err = l.topo.Add("cli")
	l.must(err)
	l.inet, err = l.topo.Add("inet")
	l.must(err)
	l.vps, err = l.topo.Add("vps")
	l.must(err)
	for i, p := range profiles {
		lan := 10 * (i + 1)
		s := &ispState{
			p:       p,
			idx:     i,
			cliIf:   "eth" + strconv.Itoa(i),
			gw:      addr(fmt.Sprintf("192.168.%d.1", lan)),
			cliAddr: addr(fmt.Sprintf("192.168.%d.2", lan)),
			public:  addr(fmt.Sprintf("203.0.113.%d", 4*i+1)),
			inetGW:  addr(fmt.Sprintf("203.0.113.%d", 4*i+2)),
		}
		s.ns, err = l.topo.Add(p.Name)
		l.must(err)
		l.must(l.topo.Link(l.cli, s.ns, s.cliIf, "lan",
			netip.PrefixFrom(s.cliAddr, 24), netip.PrefixFrom(s.gw, 24)))
		l.must(l.topo.Link(s.ns, l.inet, "wan", "isp"+strconv.Itoa(i),
			netip.PrefixFrom(s.public, vpsLinkBits), netip.PrefixFrom(s.inetGW, vpsLinkBits)))
		l.must(l.topo.Route(s.ns, pfx("0.0.0.0/0"), s.inetGW))
		l.must(l.topo.Forwarding(s.ns, true))
		l.isps = append(l.isps, s)
		l.byName[p.Name] = s
	}
	l.must(l.topo.Link(l.vps, l.inet, "eth0", "vps",
		netip.PrefixFrom(addr(vpsAddr), vpsLinkBits), netip.PrefixFrom(addr(vpsInetAddr), vpsLinkBits)))
	l.must(l.topo.Route(l.vps, pfx("0.0.0.0/0"), addr(vpsInetAddr)))
	l.must(l.topo.Forwarding(l.inet, true))
	for _, a := range []string{dohAddr, echoAddr, resolverAddr} {
		l.must(l.topo.AddAddr(l.inet, "lo", netip.PrefixFrom(addr(a), 32)))
	}
}

func (l *Lab) startCA() {
	l.t.Helper()
	ca, err := testca.New()
	l.must(err)
	l.ca = ca
	l.caFile = filepath.Join(l.tmp, "lab-ca.pem")
	l.must(os.WriteFile(l.caFile, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: ca.Cert.Raw}), 0o644))
}

// startInet starts the fake DoH (with the Cymru TXT hook), the ASN echo and the VPS's resolver.
func (l *Lab) startInet() {
	l.t.Helper()
	var maps []fakeasn.Mapping
	for _, s := range l.isps {
		maps = append(maps, fakeasn.Mapping{Prefix: netip.PrefixFrom(s.public, 32), ASN: s.p.ASN, Name: s.p.Name})
	}
	cert, err := l.ca.Issue(dohAddr, "doh.test")
	l.must(err)
	l.must(l.inet.Do(func() error {
		var err error
		if l.doh, err = fakedns.RunDoH(l.ctx, net.JoinHostPort(dohAddr, "443"), cert, fakedns.Config{
			TXT: func(name string) []string { return fakeasn.TXT(maps, name) },
		}); err != nil {
			return err
		}
		if l.echo, err = fakeasn.Run(l.ctx, net.JoinHostPort(echoAddr, "80"), maps); err != nil {
			return err
		}
		l.public, err = fakedns.RunPlain(l.ctx, net.JoinHostPort(resolverAddr, "53"), fakedns.Config{})
		return err
	}))
}

// startISP installs the queue and SNAT rules in the ISP, starts its fake TSPU and its resolver.
func (l *Lab) startISP(s *ispState) {
	l.t.Helper()
	queue := queueBase + s.idx
	// Only traffic between the client link (lan) and the inet link (wan) is queued, in both
	// directions; the resolver on lan is local traffic and never queued. Chain names avoid nft
	// keywords (e.g. "fwd").
	rules := fmt.Sprintf(`table inet obldpi {
	chain dpiq {
		type filter hook forward priority 0; policy accept;
		iifname "lan" oifname "wan" counter queue num %[1]d
		iifname "wan" oifname "lan" counter queue num %[1]d
	}
}
table ip oblnat {
	chain natpost {
		type nat hook postrouting priority 100; policy accept;
		oifname "wan" snat to %[2]s
	}
}
`, queue, s.public)
	file := filepath.Join(l.tmp, "nft-"+s.p.Name+".conf")
	l.must(os.WriteFile(file, []byte(rules), 0o644))

	s.eng = fakedpi.New(s.p.Rules)
	s.runDone = make(chan error, 1)
	l.must(s.ns.Do(func() error {
		var err error
		s.q, err = s.eng.Listen(uint16(queue))
		return err
	}))
	go func() { s.runDone <- s.q.Run(l.ctx) }()
	ctx, cancel := context.WithTimeout(l.ctx, 30*time.Second)
	defer cancel()
	if out, err := l.topo.Exec(ctx, s.ns, "nft", "-f", file); err != nil {
		l.fatal("%s: %v\n%s", s.p.Name, err, out)
	}
	l.must(s.ns.Do(func() error {
		var err error
		s.dns, err = fakedns.RunPlain(l.ctx, net.JoinHostPort(s.gw.String(), "53"), fakedns.Config{Poisoned: s.p.Poisoned})
		return err
	}))
	l.startStubs(s)
}

// startStubs serves the ISP's block page on every stub address its resolver answers for a poisoned
// name: the address on the ISP's loopback and plain HTTP on :80 answering 302 to
// http://block.<isp>.test/, like a real ISP stub. The client reaches it through its default route
// (local traffic in the ISP, never queued to fakedpi).
func (l *Lab) startStubs(s *ispState) {
	l.t.Helper()
	var stubs []netip.Addr
	for _, a := range s.p.Poisoned {
		if !slices.Contains(stubs, a) {
			stubs = append(stubs, a)
		}
	}
	slices.SortFunc(stubs, func(a, b netip.Addr) int { return a.Compare(b) })
	host := "block." + s.p.Name + ".test"
	for _, a := range stubs {
		l.must(l.topo.AddAddr(s.ns, "lo", netip.PrefixFrom(a, a.BitLen())))
		l.must(s.ns.Do(func() error {
			srv, err := faultserver.Run(l.ctx, net.JoinHostPort(a.String(), "80"), faultserver.Spec{
				Behavior: faultserver.HTTPStub, StubURL: "http://" + host + "/", Hosts: []string{host},
			}, l.ca)
			if err == nil {
				s.stubs = append(s.stubs, srv)
			}
			return err
		}))
	}
}

// startVPS renders and checks the sing-box server config, starts it in vps and waits for both ports.
func (l *Lab) startVPS() {
	l.t.Helper()
	pinned, err := thirdParty("sing-box")
	l.must(err)
	l.singBox = pinned
	// The VPS runs a copy from its own folder: splitter's pre-flight (procman.CleanupStale) kills
	// every sing-box/nfqws2/winws2 whose executable lies below the folder of its sing-box (the
	// pinned one), and the PID namespace is shared, so a VPS started from that folder would die.
	bin := filepath.Join(l.tmp, "vps-bin", "sing-box")
	l.must(copyExecutable(pinned, bin))
	ctx, cancel := context.WithTimeout(l.ctx, 60*time.Second)
	defer cancel()
	if out, err := l.topo.Exec(ctx, l.vps, bin, "version"); err == nil {
		first, _, _ := strings.Cut(string(out), "\n")
		labRun.setSingBox(strings.TrimSpace(first))
	}
	keys, err := l.topo.Exec(ctx, l.vps, bin, "generate", "reality-keypair")
	l.must(err)
	priv, pub, err := parseRealityKeypair(keys)
	l.must(err)
	uuid, err := newUUID()
	l.must(err)
	sid, err := randHex(8)
	l.must(err)
	pass, err := randHex(8)
	l.must(err)
	l.vpsP = vpsParams{
		Addr:       addr(vpsAddr),
		VLESSPort:  vlessPort,
		SOCKSPort:  socksPort,
		UUID:       uuid,
		Flow:       vlessFlow,
		PrivateKey: priv,
		PublicKey:  pub,
		ShortID:    sid,
		ServerName: realityHost,
		Handshake:  l.Site(realityHost).Addr,
		Resolver:   netip.AddrPortFrom(addr(resolverAddr), 53),
		SOCKSUser:  "lab",
		SOCKSPass:  pass,
	}
	body, err := l.vpsP.serverConfig()
	l.must(err)
	cfg := filepath.Join(l.tmp, "vps-sing-box.json")
	l.must(os.WriteFile(cfg, body, 0o600))
	if out, err := l.topo.Exec(ctx, l.vps, bin, "check", "-c", cfg); err != nil {
		l.fatal("sing-box check of the VPS config: %v\n%s", err, out)
	}
	logFile := filepath.Join(l.logDir, "vps-sing-box.log")
	cmd, err := l.topo.Start(l.ctx, l.vps, nil, "sh", "-c", `exec "$0" run -c "$1" >>"$2" 2>&1`, bin, cfg, logFile)
	l.must(err)
	exited := make(chan error, 1)
	go func() { exited <- cmd.Wait() }()

	for _, port := range []uint16{vlessPort, socksPort} {
		target := netip.AddrPortFrom(addr(vpsAddr), port)
		err := poll(ctx, func() error {
			select {
			case err := <-exited:
				exited <- err
				return &stopError{fmt.Errorf("sing-box exited: %v", err)}
			default:
			}
			return l.inet.Do(func() error {
				c, err := net.DialTimeout("tcp", target.String(), time.Second)
				if err == nil {
					c.Close()
				}
				return err
			})
		})
		if err != nil {
			log, _ := os.ReadFile(logFile)
			l.fatal("VPS sing-box on %v: %v\n%s", target, err, tail(log, 4096))
		}
	}
}

// teardown stops every server and queue, then removes the namespaces (which also kills sing-box).
func (l *Lab) teardown() {
	l.cancel()
	for _, s := range l.isps {
		if s.q == nil {
			continue
		}
		select {
		case err := <-s.runDone:
			if err != nil {
				l.t.Logf("lab %s: %s queue: %v", l.prefix, s.p.Name, err)
			}
		case <-time.After(10 * time.Second):
			l.t.Errorf("lab %s: %s queue did not stop", l.prefix, s.p.Name)
		}
		if err := s.q.Close(); err != nil {
			l.t.Errorf("lab %s: %s queue close: %v", l.prefix, s.p.Name, err)
		}
		if s.dns != nil {
			s.dns.Close()
		}
		for _, srv := range s.stubs {
			srv.Close()
		}
	}
	l.mu.Lock()
	for _, s := range l.sites {
		s.Server.Close()
	}
	l.mu.Unlock()
	for _, s := range []*fakedns.Server{l.doh, l.public} {
		if s != nil {
			s.Close()
		}
	}
	if err := l.topo.Teardown(); err != nil {
		l.t.Errorf("lab %s: teardown: %v", l.prefix, err)
	}
}

// binaryName is a cmd/<name> folder name; it never contains a path separator.
var binaryName = regexp.MustCompile(`^[a-z][a-z0-9-]*$`)

// leftover matches namespaces and /etc/netns folders of a lab run: obl + exactly 4 hex + "-" + name.
var leftover = regexp.MustCompile(`^(obl[0-9a-f]{4})-[a-z][a-z0-9]*$`)

// removeLeftovers tears down the prefixes of crashed runs, found by listing `ip netns list` and
// /etc/netns (read only), through netns' own Teardown. Labs of this process that are still up are
// kept; a lab of another process running at the same time is not (run one lab process at a time).
func removeLeftovers(t *testing.T) {
	t.Helper()
	var names []string
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "ip", "netns", "list").Output()
	if err != nil {
		t.Fatalf("lab: ip netns list: %v", err)
	}
	for _, line := range strings.Split(string(out), "\n") {
		if f := strings.Fields(line); len(f) > 0 {
			names = append(names, f[0])
		}
	}
	if entries, err := os.ReadDir("/etc/netns"); err == nil {
		for _, e := range entries {
			names = append(names, e.Name())
		}
	}
	var prefixes []string
	for _, n := range names {
		if m := leftover.FindStringSubmatch(n); m != nil && !slices.Contains(prefixes, m[1]) && !labRun.isActive(m[1]) {
			prefixes = append(prefixes, m[1])
		}
	}
	for _, p := range prefixes {
		t.Logf("lab: removing leftovers of a crashed run: %s-*", p)
		if err := netns.New(p).Teardown(); err != nil {
			t.Fatalf("lab: remove leftovers %s: %v", p, err)
		}
	}
}

// UseISP makes name the client's uplink: its link up, the default route through it, the other
// client links down, and the system resolver switched to that ISP's plain DNS.
func (l *Lab) UseISP(name string) {
	l.t.Helper()
	s := l.isp(name)
	l.must(l.topo.SetLink(l.cli, s.cliIf, true))
	ctx, cancel := context.WithTimeout(l.ctx, 10*time.Second)
	defer cancel()
	// The connected route appears with the link; retry the gateway route until it does.
	if err := poll(ctx, func() error { return l.topo.Route(l.cli, pfx("0.0.0.0/0"), s.gw) }); err != nil {
		l.fatal("UseISP %s: %v", name, err)
	}
	for _, o := range l.isps {
		if o != s {
			l.must(l.topo.SetLink(l.cli, o.cliIf, false))
		}
	}
	l.must(l.topo.SetResolver(l.cli, s.gw))
	l.mu.Lock()
	l.current = name
	l.mu.Unlock()
}

// SetISPLink brings the ISP's client-facing link (lan, in the ISP namespace) down or up. Down is a
// dead modem: the client keeps its interface, address and routes, but nothing it sends gets through.
func (l *Lab) SetISPLink(isp string, up bool) {
	l.t.Helper()
	l.must(l.topo.SetLink(l.isp(isp).ns, "lan", up))
}

// SetRules replaces the fake TSPU rules of isp (a TSPU update; flows keep their state).
func (l *Lab) SetRules(isp string, r ...fakedpi.Rule) {
	l.t.Helper()
	l.isp(isp).eng.SetRules(r)
}

// DPI returns the fake TSPU of isp (Seen, Stats).
func (l *Lab) DPI(isp string) *fakedpi.Engine {
	l.t.Helper()
	return l.isp(isp).eng
}

// DoH returns the fake DoH server (query log).
func (l *Lab) DoH() *fakedns.Server { return l.doh }

func (l *Lab) isp(name string) *ispState {
	l.t.Helper()
	s, ok := l.byName[name]
	if !ok {
		l.fatal("unknown ISP %q", name)
	}
	return s
}

// AddSite starts a fake site in inet with its own address (port 443) and adds its names (s.Hosts,
// or name when empty) to the DoH zone, the ISP resolvers and the VPS's resolver. An empty
// Behavior means OK.
func (l *Lab) AddSite(name string, s faultserver.Spec) Site {
	l.t.Helper()
	if s.Behavior == "" {
		s.Behavior = faultserver.OK
	}
	if len(s.Hosts) == 0 {
		s.Hosts = []string{name}
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if _, dup := l.sites[name]; dup {
		l.fatal("site %s added twice", name)
	}
	if l.nextSite > siteLast {
		l.fatal("no site addresses left")
	}
	a := addr(sitePrefix + strconv.Itoa(l.nextSite))
	l.nextSite++
	l.must(l.topo.AddAddr(l.inet, "lo", netip.PrefixFrom(a, 32)))
	var srv *faultserver.Server
	l.must(l.inet.Do(func() error {
		var err error
		srv, err = faultserver.Run(l.ctx, net.JoinHostPort(a.String(), "443"), s, l.ca)
		return err
	}))
	site := Site{Addr: srv.Addr, Server: srv}
	l.sites[name] = site
	for _, h := range s.Hosts {
		if _, err := netip.ParseAddr(h); err == nil {
			continue // an IP host in the certificate, not a name
		}
		h = strings.TrimSuffix(strings.ToLower(h), ".")
		l.zone[h] = append(l.zone[h], a)
	}
	z := make(map[string][]netip.Addr, len(l.zone))
	for k, v := range l.zone {
		z[k] = slices.Clone(v)
	}
	for _, d := range []*fakedns.Server{l.doh, l.public} {
		d.SetZone(z)
	}
	for _, i := range l.isps {
		i.dns.SetZone(z)
	}
	return site
}

// Site returns a site added with AddSite.
func (l *Lab) Site(name string) Site {
	l.t.Helper()
	l.mu.Lock()
	s, ok := l.sites[name]
	l.mu.Unlock()
	if !ok {
		l.fatal("unknown site %q", name)
	}
	return s
}

// ClientAddr is the client's address as a site sees it through isp (the ISP's SNAT address).
func (l *Lab) ClientAddr(isp string) netip.Addr {
	l.t.Helper()
	return l.isp(isp).public
}

// VPSAddr is the VPS's address; sites see it for the tunnel and the proxy route.
func (l *Lab) VPSAddr() netip.Addr { return addr(vpsAddr) }

// noProxyEnv removes the host's proxy settings, so lab clients never use them.
var noProxyEnv = []string{"env",
	"-u", "http_proxy", "-u", "https_proxy", "-u", "all_proxy", "-u", "no_proxy",
	"-u", "HTTP_PROXY", "-u", "HTTPS_PROXY", "-u", "ALL_PROXY", "-u", "NO_PROXY"}

// Exec runs argv in the client namespace with SSL_CERT_FILE set to the test CA (and no proxy
// variables) and returns its stdout and stderr.
func (l *Lab) Exec(ctx context.Context, argv ...string) (stdout, stderr []byte, err error) {
	if len(argv) == 0 {
		return nil, nil, errors.New("lab: exec: empty argv")
	}
	f, err := os.CreateTemp(l.tmp, "stderr-*")
	if err != nil {
		return nil, nil, fmt.Errorf("lab: %w", err)
	}
	errFile := f.Name()
	f.Close()
	defer os.Remove(errFile) // a file in our own temp dir
	full := append(slices.Clone(noProxyEnv), "SSL_CERT_FILE="+l.caFile, "sh", "-c", `"$@" 2>"$0"`, errFile)
	stdout, err = l.topo.Exec(ctx, l.cli, append(full, argv...)...)
	stderr, _ = os.ReadFile(errFile)
	if err != nil {
		if msg := strings.TrimSpace(string(stderr)); msg != "" {
			err = fmt.Errorf("%w: %s", err, msg)
		}
	}
	return stdout, stderr, err
}

// Curl runs curl in the client namespace with the test CA trusted, no proxy variables and a
// 10 s limit: curl --cacert <ca> --silent --show-error --max-time 10 args...
func (l *Lab) Curl(ctx context.Context, args ...string) ([]byte, error) {
	argv := append(slices.Clone(noProxyEnv), "curl", "--cacert", l.caFile, "--silent", "--show-error", "--max-time", "10")
	return l.topo.Exec(ctx, l.cli, append(argv, args...)...)
}

// HTTPGet does a Go GET from the client namespace, trusting the test CA. Host names are resolved
// from the lab zone (sites), not through DNS; IP hosts are used as is. A non-2xx status is an error.
func (l *Lab) HTTPGet(ctx context.Context, url string) ([]byte, error) {
	tr := &http.Transport{
		Proxy:             nil,
		TLSClientConfig:   &tls.Config{RootCAs: l.ca.Pool},
		DisableKeepAlives: true,
		DialContext: func(ctx context.Context, network, hostport string) (net.Conn, error) {
			host, port, err := net.SplitHostPort(hostport)
			if err != nil {
				return nil, err
			}
			ip, err := netip.ParseAddr(host)
			if err != nil {
				l.mu.Lock()
				addrs := l.zone[strings.ToLower(host)]
				l.mu.Unlock()
				if len(addrs) == 0 {
					return nil, fmt.Errorf("lab: %s is not a lab name", host)
				}
				ip = addrs[0]
			}
			var c net.Conn
			err = l.cli.Do(func() error {
				var err error
				c, err = (&net.Dialer{}).DialContext(ctx, "tcp", net.JoinHostPort(ip.String(), port))
				return err
			})
			return c, err
		},
	}
	defer tr.CloseIdleConnections()
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	resp, err := (&http.Client{Transport: tr}).Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 16<<20))
	if err != nil {
		return body, err
	}
	if resp.StatusCode/100 != 2 {
		return body, fmt.Errorf("lab: GET %s: %s", url, resp.Status)
	}
	return body, nil
}

// Binary returns $LAB_BIN_DIR/<name> when LAB_BIN_DIR is set (the release artifacts), else builds
// ./cmd/<name> once per test process into a temp dir.
func (l *Lab) Binary(name string) string {
	l.t.Helper()
	if !binaryName.MatchString(name) {
		l.fatal("binary name %q", name)
	}
	if dir := os.Getenv("LAB_BIN_DIR"); dir != "" {
		p := filepath.Join(dir, name)
		if _, err := os.Stat(p); err != nil {
			l.fatal("LAB_BIN_DIR: %v", err)
		}
		return p
	}
	labRun.mu.Lock()
	defer labRun.mu.Unlock()
	if p, ok := labRun.built[name]; ok {
		return p
	}
	root, err := repoRoot()
	l.must(err)
	if labRun.buildDir == "" {
		d, err := os.MkdirTemp("", "lab-bin-")
		l.must(err)
		labRun.buildDir, labRun.built = d, map[string]string{}
	}
	out := filepath.Join(labRun.buildDir, name)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, "go", "build", "-o", out, "./cmd/"+name)
	cmd.Dir = root
	if b, err := cmd.CombinedOutput(); err != nil {
		l.fatal("go build ./cmd/%s: %v\n%s", name, err, b)
	}
	labRun.built[name] = out
	return out
}

// Report records one scenario result for the release report (report.md in the run dir).
func (l *Lab) Report(scenario string, pass bool, note string) {
	labRun.add(result{test: l.t.Name(), scenario: scenario, pass: pass, note: note, at: time.Now()})
}

// stopError ends poll at once.
type stopError struct{ err error }

func (e *stopError) Error() string { return e.err.Error() }

// poll calls fn every 100 ms until it succeeds, returns a stopError, or ctx is done.
func poll(ctx context.Context, fn func() error) error {
	for {
		err := fn()
		if err == nil {
			return nil
		}
		var stop *stopError
		if errors.As(err, &stop) {
			return stop.err
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("%w (last error: %v)", ctx.Err(), err)
		case <-time.After(100 * time.Millisecond):
		}
	}
}

// copyExecutable copies src to dst (creating dst's folder) with mode 0755.
func copyExecutable(src, dst string) error {
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return fmt.Errorf("lab: %w", err)
	}
	in, err := os.Open(src)
	if err != nil {
		return fmt.Errorf("lab: %w", err)
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o755)
	if err != nil {
		return fmt.Errorf("lab: %w", err)
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return fmt.Errorf("lab: copy %s: %w", src, err)
	}
	if err := out.Close(); err != nil {
		return fmt.Errorf("lab: %w", err)
	}
	return nil
}

func tail(b []byte, n int) []byte {
	if len(b) > n {
		return b[len(b)-n:]
	}
	return b
}
