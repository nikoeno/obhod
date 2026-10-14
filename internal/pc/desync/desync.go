package desync

import (
	"errors"
	"fmt"
	"net/netip"
	"runtime"
	"strings"

	"github.com/nikoejento/obhod/internal/lib/netif"
	"github.com/nikoejento/obhod/internal/lib/route"
	"github.com/nikoejento/obhod/internal/pc/desync/nfqws"
	"github.com/nikoejento/obhod/internal/pc/desync/winws"
)

// Errors returned by Build.
var (
	// ErrNoExcludeIP means Config.ExcludeIPs is empty; the exclusion protects the tunnel.
	ErrNoExcludeIP = errors.New("desync: ExcludeIPs must not be empty (it protects the tunnel)")
	// ErrUnsupported means the engine is not available on this OS.
	ErrUnsupported = errors.New("desync: engine not available on this OS")
	// ErrVirtualUplink means the uplink is a TUN/TAP/loopback interface; the engine must never touch the TUN.
	ErrVirtualUplink = errors.New("desync: uplink is a virtual interface")
	// ErrInvalidConfig means a Config field cannot be used (a Strategy option
	// the app controls itself, port 0, a bad range or prefix).
	ErrInvalidConfig = errors.New("desync: invalid config")
)

// reservedOptions are engine options the app sets itself. A Strategy that
// carries one could undo the uplink restriction, the tunnel exclusion or the
// hostlist-only rule (zapret2 allows a single --wf-raw-filter; --new starts a
// profile without our hostlist), so Check rejects them. An option matches by
// its name (the part before "="), and also covers its "-suffixed" variants
// (--hostlist matches --hostlist-domains).
var reservedOptions = []string{
	"--wf",       // every WinDivert filter option (--wf-raw-filter, --wf-iface, --wf-save, --wf-tcp-out ...)
	"--new",      // a new profile: our hostlist would not apply to it
	"--skip",     // disables our profile
	"--hostlist", // --hostlist, --hostlist-domains, --hostlist-auto ...; the hostlist is ours
	"--ipset",    // address sets change what the profile matches
	"--qnum",     // the queue must match the nft rules
	"--fwmark",   // the mark must match the nft rules
	"--lua-init", // set from LuaInit
	"--uid",      // nfqws2 identity: the app keeps root (--uid=0:0), see nfqws/API.md
	"--user",     // same: no privilege drop to another user
}

// Check validates the parts of c that do not depend on the table or the
// uplink. Build calls it; the app also calls it when it loads its config.
func (c Config) Check() error {
	for _, a := range c.Strategy {
		if strings.HasPrefix(a, "@") {
			return fmt.Errorf("%w: strategy argument %q reads options from a file", ErrInvalidConfig, a)
		}
		name, _, _ := strings.Cut(a, "=")
		for _, r := range reservedOptions {
			if name == r || strings.HasPrefix(name, r+"-") {
				return fmt.Errorf("%w: strategy option %q is set by the app", ErrInvalidConfig, name)
			}
		}
	}
	for _, p := range c.Ports {
		if p == 0 {
			return fmt.Errorf("%w: port 0", ErrInvalidConfig)
		}
	}
	for _, r := range c.ExcludePorts {
		if r[0] == 0 || r[0] > r[1] {
			return fmt.Errorf("%w: exclude port range %d-%d", ErrInvalidConfig, r[0], r[1])
		}
	}
	for _, p := range c.ExcludeIPs {
		if !p.IsValid() {
			return fmt.Errorf("%w: invalid exclude prefix", ErrInvalidConfig)
		}
	}
	for _, f := range c.LuaInit {
		if f == "" || strings.ContainsAny(f, `/\`) || strings.Contains(f, "..") {
			return fmt.Errorf("%w: LuaInit file name %q", ErrInvalidConfig, f)
		}
	}
	return nil
}

// unmapped returns the prefixes with IPv4-mapped IPv6 prefixes
// (::ffff:a.b.c.d/n, n >= 96) turned into IPv4 prefixes: the engines compare
// IPv4 packets with IPv4 clauses only, so a mapped form would never match.
func unmapped(in []netip.Prefix) []netip.Prefix {
	out := make([]netip.Prefix, len(in))
	for i, p := range in {
		if a := p.Addr(); a.Is4In6() && p.Bits() >= 96 {
			p = netip.PrefixFrom(a.Unmap(), p.Bits()-96)
		}
		out[i] = p
	}
	return out
}

// HostlistFile is the key of Plan.Files that holds the hostlist.
const HostlistFile = "hostlist.txt"

// Engine names a desync engine.
type Engine string

// The known engines.
const (
	WinWS Engine = "winws2" // Windows (zapret2)
	NFQWS Engine = "nfqws2" // Linux (zapret2)
)

// PortRange is an inclusive port range: [0] is the first port, [1] the last.
// The children (winws, nfqws) take the same shape as [][2]uint16.
type PortRange [2]uint16

// Config describes the engine; Strategy comes from the user's config.
type Config struct {
	Engine       Engine         `yaml:"engine"`        // default by GOOS
	Binary       string         `yaml:"binary"`        // path to the engine binary
	Strategy     []string       `yaml:"strategy"`      // zapret2 profile args (e.g. --filter-l7=tls --payload=tls_client_hello --lua-desync=...)
	LuaInit      []string       `yaml:"lua_init"`      // Lua libraries to load; default zapret-lib.lua, zapret-antidpi.lua
	ExcludeIPs   []netip.Prefix `yaml:"exclude_ips"`   // VPS IP(s); required
	ExcludePorts []PortRange    `yaml:"exclude_ports"` // probe source-port ranges (future)
	Ports        []uint16       `yaml:"ports"`         // TCP ports to act on; default 443, 80
}

// Plan is what the app needs to run the engine. A Plan without Path means
// "nothing to run".
type Plan struct {
	Engine   Engine
	Path     string
	Args     []string
	Files    map[string][]byte // name → content (hostlist), written to the Run dir by the app
	Setup    []string          // commands a human must run once (nfqws: nftables rules, then an ip rule exemption for the mark); never run by us
	Teardown []string
}

// DefaultEngine returns the engine of the running OS, or "" if there is none.
func DefaultEngine() Engine {
	switch runtime.GOOS {
	case "windows":
		return WinWS
	case "linux":
		return NFQWS
	}
	return ""
}

// Build creates the plan for the desync domains in t, pinned to the physical uplink.
func Build(cfg Config, uplink netif.Interface, t route.Table) (Plan, error) {
	if len(cfg.ExcludeIPs) == 0 {
		return Plan{}, ErrNoExcludeIP
	}
	engine := cfg.Engine
	if engine == "" {
		engine = DefaultEngine()
	}
	if engine != WinWS && engine != NFQWS {
		return Plan{}, fmt.Errorf("%w: %q on %s", ErrUnsupported, engine, runtime.GOOS)
	}
	if err := cfg.Check(); err != nil {
		return Plan{}, err
	}
	excludeIPs := unmapped(cfg.ExcludeIPs)

	domains := t.Domains(route.Desync)
	if len(domains) == 0 {
		return Plan{Engine: engine}, nil
	}
	if uplink.Virtual {
		return Plan{}, fmt.Errorf("%w: %s", ErrVirtualUplink, uplink.Name)
	}

	ranges := make([][2]uint16, len(cfg.ExcludePorts))
	for i, r := range cfg.ExcludePorts {
		ranges[i] = r
	}
	switch engine {
	case WinWS:
		out, err := winws.Render(winws.Input{
			Binary:       cfg.Binary,
			LuaInit:      cfg.LuaInit,
			Strategy:     cfg.Strategy,
			Hostlist:     domains,
			IfIndex:      uplink.Index,
			ExcludeIPs:   excludeIPs,
			ExcludePorts: ranges,
			Ports:        cfg.Ports,
		})
		if err != nil {
			return Plan{}, err
		}
		return Plan{
			Engine: engine, Path: out.Path, Args: out.Args,
			Files: map[string][]byte{HostlistFile: out.Hostlist},
		}, nil
	default:
		out, err := nfqws.Render(nfqws.Input{
			Binary:       cfg.Binary,
			LuaInit:      cfg.LuaInit,
			Strategy:     cfg.Strategy,
			Hostlist:     domains,
			Uplink:       uplink.Name,
			ExcludeIPs:   excludeIPs,
			ExcludePorts: ranges,
			Ports:        cfg.Ports,
		})
		if err != nil {
			return Plan{}, err
		}
		return Plan{
			Engine: engine, Path: out.Path, Args: out.Args,
			Files:    map[string][]byte{HostlistFile: out.Hostlist},
			Setup:    out.NFTSetup,
			Teardown: out.NFTDown,
		}, nil
	}
}
