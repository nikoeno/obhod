//go:build linux && lab

package lab

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/nikoejento/obhod/internal/lib/route"
)

// SplitterOptions configure one RunSplitter.
type SplitterOptions struct {
	Args       []string               // e.g. "run" or "run", "--ui"; --config is added; default "run"
	AppYAML    string                 // merged over the lab defaults (lab DoH, echo, binaries)
	Candidates []string               // the checker's candidate list (the Decisions domains are added)
	Decisions  map[string]route.Route // seeded into decisions.json for the current ISP's network
	Static     map[string]route.Route // proxy/block list entries

	// OtherDecisions seeds the stored decisions of other ISPs' networks (ISP name -> domain ->
	// route), for scenarios that switch the uplink; their domains join the candidates.
	OtherDecisions map[string]map[string]route.Route

	// ReuseRootOf starts a second process on the SAME paths.root as that splitter (the single-instance
	// lock test). Nothing under the root is rewritten (lists, secrets, salt and decisions stay as the
	// first process has them); the config is written to app-<n>.yaml next to app.yaml. Decisions,
	// Candidates, Static and OtherDecisions must be empty; AppYAML is merged over the first process's
	// config and must not move paths.root.
	ReuseRootOf *Splitter
}

// Splitter is one splitter process in the client namespace, with its own paths.root.
type Splitter struct {
	l          *Lab
	root       string // paths.root (a temp dir)
	appYAML    []byte // the merged app.yaml as written
	logFile    string // the app log (Paths.Logs/<log.file>)
	outFile    string // stdout and stderr, in the run dir
	statusAddr string // status.listen
	token      string // status.token, if set
	salt       string // Paths.Data/netwatch.salt, written before the start
	includeGW  bool   // checker.netwatch.includegateway

	cmd  *exec.Cmd
	done chan struct{} // closed when the process has been waited for
	code int           // exit code (-1: killed by a signal)
	werr error         // Wait error other than a non-zero exit

	stopOnce sync.Once
	diags    int // diag files written (guarded by l.mu)
}

// splitterStatusListen is the default status address: loopback of the client namespace, so it
// never collides with anything on the host.
const splitterStatusListen = "127.0.0.1:19091"

// RunSplitter writes app.yaml (the lab defaults merged with o.AppYAML), tunnel.json and proxy.json
// (the VPS's client parameters), the check lists and the seeded decisions.json into a fresh
// paths.root, and starts `splitter <Args> --config <root>/app.yaml` in the client namespace with
// the test CA in SSL_CERT_FILE. Its stdout and stderr go to splitter-<n>.out in the run dir. It is
// stopped (SIGTERM, then SIGKILL) in t.Cleanup, before the lab's teardown.
func (l *Lab) RunSplitter(ctx context.Context, o SplitterOptions) *Splitter {
	l.t.Helper()
	l.mu.Lock()
	l.splitters++
	seq := l.splitters
	l.mu.Unlock()

	var s *Splitter
	var root string
	var dirs map[string]string
	var salt string
	cfgName := "app.yaml"
	if o.ReuseRootOf != nil {
		first := o.ReuseRootOf
		if len(o.Decisions)+len(o.Candidates)+len(o.Static)+len(o.OtherDecisions) != 0 {
			l.fatal("RunSplitter: ReuseRootOf rewrites nothing under the root, so Decisions, Candidates, Static and OtherDecisions must be empty")
		}
		root, salt = first.root, first.salt
		cfgName = fmt.Sprintf("app-%d.yaml", seq)
		appYAML, err := mergeYAML(first.appYAML, []byte(o.AppYAML))
		l.must(err)
		if got := yamlString(appYAML, "paths.root", ""); got != root {
			l.fatal("RunSplitter: AppYAML must not move paths.root (got %q, want %q)", got, root)
		}
		s = &Splitter{appYAML: appYAML}
	} else {
		var err error
		root, err = os.MkdirTemp(l.tmp, "splitter-root-")
		l.must(err)
		dirs = map[string]string{
			"data": filepath.Join(root, "data"), "secrets": filepath.Join(root, "secrets"),
			"lists": filepath.Join(root, "lists"), "logs": filepath.Join(root, "logs"),
		}
		for _, d := range dirs {
			l.must(os.MkdirAll(d, 0o700))
		}

		// Lists: candidates (plus every seeded domain, or the checker would prune its decision), and
		// the static proxy and block lists.
		cands := slices.Clone(o.Candidates)
		for d := range o.Decisions {
			cands = append(cands, d)
		}
		for _, ds := range o.OtherDecisions {
			for d := range ds {
				cands = append(cands, d)
			}
		}
		var proxy, block []string
		for d, r := range o.Static {
			switch r {
			case route.Proxy:
				proxy = append(proxy, d)
			case route.Block:
				block = append(block, d)
			default:
				l.fatal("RunSplitter: static %s: route %q (only proxy and block are static)", d, r)
			}
		}
		files := map[string][]string{"candidates.txt": cands, "proxy.txt": proxy, "block.txt": block}
		for name, ds := range files {
			b, err := listFile(ds)
			l.must(err)
			l.must(os.WriteFile(filepath.Join(dirs["lists"], name), b, 0o600))
		}

		// Secrets: the client side of the VPS's inbounds.
		l.must(os.WriteFile(filepath.Join(dirs["secrets"], "tunnel.json"), l.vpsP.tunnelOutbound(), 0o600))
		l.must(os.WriteFile(filepath.Join(dirs["secrets"], "proxy.json"), l.vpsP.proxyOutbound(), 0o600))

		// app.yaml.
		def := splitterDefaults{
			Root:         root,
			DoH:          "https://" + dohAddr + "/dns-query",
			Echo:         "http://" + echoAddr + "/",
			Controls:     []string{"control-a.test", "control-b.test"},
			Canaries:     []string{"canary.test"},
			Neutral:      []string{"neutral.test"},
			SingBox:      l.singBox,
			StatusListen: splitterStatusListen,
			Candidates:   filepath.Join(dirs["lists"], "candidates.txt"),
			Proxy:        filepath.Join(dirs["lists"], "proxy.txt"),
			Block:        filepath.Join(dirs["lists"], "block.txt"),
		}
		if p, err := thirdParty("nfqws2"); err == nil {
			def.Desync = p
		}
		base, err := def.yaml()
		l.must(err)
		appYAML, err := mergeYAML(base, []byte(o.AppYAML))
		l.must(err)
		if got := yamlString(appYAML, "paths.root", ""); got != root {
			l.fatal("RunSplitter: AppYAML must not move paths.root (got %q): the app must only write below its temp root", got)
		}
		s = &Splitter{appYAML: appYAML}

		// The netwatch salt is written before the start (the app keeps a salt of 32+ bytes it finds),
		// so the lab can compute the gateway network ID the checker derives.
		salt, err = randHex(32)
		l.must(err)
		l.must(os.WriteFile(filepath.Join(dirs["data"], "netwatch.salt"), []byte(salt), 0o600))
	}
	appYAML := s.appYAML
	cfgFile := filepath.Join(root, cfgName)
	l.must(os.WriteFile(cfgFile, appYAML, 0o600))

	logDir := filepath.Join(root, "logs")
	s.l, s.root, s.salt = l, root, salt
	s.logFile = filepath.Join(logDir, yamlString(appYAML, "log.file", "splitter.log"))
	s.outFile = filepath.Join(l.logDir, fmt.Sprintf("splitter-%d.out", seq))
	s.statusAddr = yamlString(appYAML, "status.listen", splitterStatusListen)
	s.token = yamlString(appYAML, "status.token", "")
	s.done = make(chan struct{})
	if v, ok := yamlPath(appYAML, "checker.netwatch.includegateway"); ok {
		s.includeGW, _ = v.(bool)
	}

	if o.ReuseRootOf == nil && len(o.Decisions)+len(o.OtherDecisions) > 0 {
		l.mu.Lock()
		cur := l.current
		l.mu.Unlock()
		sets := []decisionSet{{IDs: s.networkIDs(cur), Decisions: o.Decisions}}
		for isp, ds := range o.OtherDecisions {
			if isp == cur {
				l.fatal("RunSplitter: OtherDecisions names the current ISP %q; use Decisions", isp)
			}
			sets = append(sets, decisionSet{IDs: s.networkIDs(isp), Decisions: ds})
		}
		if len(o.Decisions) == 0 {
			sets = sets[1:]
		}
		b, err := seedDecisionSets(sets, time.Now())
		l.must(err)
		l.must(os.WriteFile(s.decisionsFile(), b, 0o600))
	}

	args := slices.Clone(o.Args)
	if len(args) == 0 {
		args = []string{"run"}
	}
	args = append(args, "--config", cfgFile)
	bin := l.Binary("splitter")
	argv := append(slices.Clone(noProxyEnv), "SSL_CERT_FILE="+l.caFile,
		"sh", "-c", `log=$1; shift; exec "$@" >>"$log" 2>&1`, "sh", s.outFile, bin)
	argv = append(argv, args...)
	// `ip netns exec`, env and sh all exec, so the started process is splitter itself.
	cmd, err := l.topo.Start(ctx, l.cli, nil, argv...)
	l.must(err)
	s.cmd = cmd
	go func() {
		err := cmd.Wait()
		s.code = cmd.ProcessState.ExitCode()
		var ee *exec.ExitError
		if err != nil && !errors.As(err, &ee) {
			s.werr = err
		}
		close(s.done)
	}()
	l.t.Cleanup(func() {
		code, err := s.stop(30 * time.Second)
		l.t.Logf("lab %s: splitter %d stopped: exit %d, %v; output in %s", l.prefix, seq, code, err, s.outFile)
		if b, rerr := os.ReadFile(s.logFile); rerr == nil {
			_ = os.WriteFile(filepath.Join(l.logDir, fmt.Sprintf("splitter-%d.log", seq)), b, 0o644)
		}
	})
	return s
}

// stop sends SIGTERM, waits up to grace for a clean exit, then kills the process group. It returns
// the exit code.
func (s *Splitter) stop(grace time.Duration) (int, error) {
	s.stopOnce.Do(func() {
		select {
		case <-s.done:
			return
		default:
		}
		_ = s.cmd.Process.Signal(syscall.SIGTERM)
		select {
		case <-s.done:
		case <-time.After(grace):
			_ = syscall.Kill(-s.cmd.Process.Pid, syscall.SIGKILL)
			<-s.done
		}
	})
	<-s.done
	return s.code, s.werr
}

// Wait waits for the process to exit and returns its exit code (-1 when a signal ended it).
func (s *Splitter) Wait(ctx context.Context) (exitCode int, err error) {
	select {
	case <-s.done:
		return s.code, s.werr
	case <-ctx.Done():
		return 0, ctx.Err()
	}
}

// exited reports whether the process has ended.
func (s *Splitter) exited() bool {
	select {
	case <-s.done:
		return true
	default:
		return false
	}
}

// Log returns the app log so far (Paths.Logs/<log.file>).
func (s *Splitter) Log() string {
	b, _ := os.ReadFile(s.logFile)
	return string(b)
}

// output returns what the process wrote to stdout and stderr so far.
func (s *Splitter) output() string {
	b, _ := os.ReadFile(s.outFile)
	return string(b)
}

// Status does GET /api/status from the client namespace (where the status page listens on
// loopback) and returns the raw JSON (status.Snapshot with Go field names).
func (s *Splitter) Status(ctx context.Context) ([]byte, error) {
	url := "http://" + s.statusAddr + "/api/status"
	if s.token != "" {
		url += "?token=" + s.token
	}
	tr := &http.Transport{
		Proxy:             nil,
		DisableKeepAlives: true,
		DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
			var c net.Conn
			err := s.l.cli.Do(func() error {
				var err error
				c, err = (&net.Dialer{}).DialContext(ctx, network, addr)
				return err
			})
			return c, err
		},
	}
	defer tr.CloseIdleConnections()
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
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
	body, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		return body, fmt.Errorf("lab: GET /api/status: %s", resp.Status)
	}
	return body, nil
}

func (s *Splitter) decisionsFile() string {
	if f := yamlString(s.appYAML, "checker.store.file", ""); f != "" {
		return f
	}
	return filepath.Join(s.root, "data", "decisions.json")
}

// Decisions returns the raw decisions.json (store schema 1).
func (s *Splitter) Decisions() ([]byte, error) {
	return os.ReadFile(s.decisionsFile())
}

// PID is the splitter process id.
func (s *Splitter) PID() int { return s.cmd.Process.Pid }

// Kill sends SIGKILL to splitter only (not its children), for orphan tests.
func (s *Splitter) Kill() { _ = s.cmd.Process.Signal(syscall.SIGKILL) }

// networkIDs lists the IDs the checker may give isp's network: asn:<n> (the contract), then the
// gateway hash it derives from this run's salt and the ISP's lan MAC.
func (s *Splitter) networkIDs(isp string) []string {
	s.l.t.Helper()
	return networkIDs(s.l.isp(isp).p.ASN, gatewayNetworkID(s.salt, s.l.gatewayMAC(isp), ""), s.includeGW)
}

// gatewayMAC is the MAC address of isp's lan interface: the gateway MAC the client sees.
func (l *Lab) gatewayMAC(isp string) net.HardwareAddr {
	l.t.Helper()
	mac, err := l.gatewayMACErr(isp)
	l.must(err)
	return mac
}

// gatewayMACErr is gatewayMAC without failing the test, for diagnostics from subtests.
func (l *Lab) gatewayMACErr(isp string) (net.HardwareAddr, error) {
	s, ok := l.byName[isp]
	if !ok {
		return nil, fmt.Errorf("unknown ISP %q", isp)
	}
	var mac net.HardwareAddr
	err := s.ns.Do(func() error {
		ifc, err := net.InterfaceByName("lan")
		if err != nil {
			return err
		}
		mac = ifc.HardwareAddr
		return nil
	})
	return mac, err
}

// Diagnostics returns a read-only dump for a failed wait: the raw decisions.json, the raw
// /api/status JSON, the network IDs the lab computed per ISP (salt, gateway MAC), the client
// namespace's links, addresses, routes, neighbours, /proc/net/route and /proc/net/arp, and the tails
// of the app log and of the process output. It is also written to diag-<n>.txt in the run dir.
func (s *Splitter) Diagnostics(ctx context.Context, why string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "=== splitter diagnostics at %s: %s ===\n", time.Now().Format("15:04:05.000"), why)
	fmt.Fprintf(&b, "exited: %v (code %d)\n", s.exited(), s.code)
	if raw, err := s.Decisions(); err != nil {
		fmt.Fprintf(&b, "--- decisions.json: %v\n", err)
	} else {
		fmt.Fprintf(&b, "--- decisions.json\n%s\n", raw)
	}
	sctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	if raw, err := s.Status(sctx); err != nil {
		fmt.Fprintf(&b, "--- /api/status: %v\n", err)
	} else {
		fmt.Fprintf(&b, "--- /api/status\n%s\n", raw)
	}
	cancel()
	fmt.Fprintf(&b, "--- network IDs computed by the lab (salt %s, includegateway %v)\n", s.salt, s.includeGW)
	for _, isp := range s.l.isps {
		mac, err := s.l.gatewayMACErr(isp.p.Name)
		fmt.Fprintf(&b, "%s: gateway MAC %v (%v) ids %q\n", isp.p.Name, mac, err, networkIDs(isp.p.ASN, gatewayNetworkID(s.salt, mac, ""), s.includeGW))
	}
	for _, argv := range [][]string{
		{"ip", "-br", "link"}, {"ip", "-br", "addr"}, {"ip", "route"}, {"ip", "neigh"},
		{"cat", "/proc/net/route"}, {"cat", "/proc/net/arp"},
	} {
		ectx, ecancel := context.WithTimeout(ctx, 5*time.Second)
		out, _, err := s.l.Exec(ectx, argv...)
		ecancel()
		fmt.Fprintf(&b, "--- client ns: %s (%v)\n%s\n", strings.Join(argv, " "), err, out)
	}
	fmt.Fprintf(&b, "--- app log (tail)\n%s\n--- process output (tail)\n%s\n", tail([]byte(s.Log()), 6000), tail([]byte(s.output()), 3000))
	out := b.String()
	s.l.mu.Lock()
	s.diags++
	n := s.diags
	s.l.mu.Unlock()
	_ = os.WriteFile(filepath.Join(s.l.logDir, fmt.Sprintf("diag-%d.txt", n)), []byte(out), 0o644)
	return out
}

// blockanalyzerConfig writes the blockanalyzer config of the lab (lab DoH, controls, canary, neutral
// SNI) into the lab's temp dir and returns its path. firefox: uTLS's Chrome parrot carries a
// post-quantum key share, so its ClientHello spans two segments and NaiveSNI (by design) never sees
// the SNI. Firefox 120's hello fits one segment.
func (l *Lab) blockanalyzerConfig() (string, error) {
	file := filepath.Join(l.tmp, "blockanalyzer.yaml")
	cfg := fmt.Sprintf(`analyzer:
  concurrency: 1
  per_ip_concurrency: 1
  jitter: [0s, 50ms]
  doh: ["https://%s/dns-query"]
  control_domains: [control-a.test, control-b.test]
  canary_domains: [canary.test]
  neutral_sni: [neutral.test]
  bypass_processes: []   # the VPS's sing-box is visible in the shared /proc; not the client's tool
  options:
    stages: tcp,tls,http,bulk
    fingerprint: firefox
    repeats: 2
    sni_swap: true
    timeouts: {resolve: 3s, tcp: 2s, tls: 3s, http: 3s, bulk: 4s}
    bulk: {mode: download, size: 65536, stall_min: 12288, stall_max: 36864}
`, l.DoH().Addr.Addr())
	return file, os.WriteFile(file, []byte(cfg), 0o644)
}

// DiagnosticsDeep is Diagnostics plus two read-only experiments in the client namespace, to tell a
// stale interface binding of the splitter from a topology problem:
//  1. `ip -s link show` of every client link, twice, 20 s apart (across a scheduled check): the tx
//     packet deltas show which interface carries the probe traffic;
//  2. a fresh `blockanalyzer --calibrate-only` (same analyzer config as the blockanalyzer test, a new
//     dialer): if it passes while the splitter's calibration keeps failing, the splitter's dialer is
//     stale; if it fails too, the lab topology is at fault.
func (s *Splitter) DiagnosticsDeep(ctx context.Context, why string) string {
	var b strings.Builder
	links := func(label string) {
		for _, isp := range s.l.isps {
			ectx, cancel := context.WithTimeout(ctx, 5*time.Second)
			out, _, err := s.l.Exec(ectx, "ip", "-s", "link", "show", isp.cliIf)
			cancel()
			fmt.Fprintf(&b, "--- %s: ip -s link show %s (%v)\n%s\n", label, isp.cliIf, err, out)
		}
	}
	links("sample 1 at " + time.Now().Format("15:04:05.000"))
	select {
	case <-time.After(20 * time.Second):
	case <-ctx.Done():
	}
	links("sample 2 at " + time.Now().Format("15:04:05.000"))

	cfg, err := s.l.blockanalyzerConfig()
	if err != nil {
		fmt.Fprintf(&b, "--- blockanalyzer config: %v\n", err)
	} else {
		bctx, cancel := context.WithTimeout(ctx, 90*time.Second)
		stdout, stderr, err := s.l.Exec(bctx, s.l.Binary("blockanalyzer"), "--config", cfg, "--calibrate-only")
		cancel()
		code := 0
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			code = ee.ExitCode()
		} else if err != nil {
			code = -1
		}
		fmt.Fprintf(&b, "--- blockanalyzer --calibrate-only in the client ns: exit %d (%v)\nstdout:\n%s\nstderr:\n%s\n", code, err, stdout, stderr)
	}
	return s.Diagnostics(ctx, why) + b.String()
}
