package desync

import (
	"errors"
	"fmt"
	"net/netip"
	"runtime"

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
)

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
	Setup    []string          // commands a human must run once (nfqws: nftables rules); never run by us
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
			ExcludeIPs:   cfg.ExcludeIPs,
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
			ExcludeIPs:   cfg.ExcludeIPs,
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
