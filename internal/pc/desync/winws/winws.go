package winws

import (
	"errors"
	"fmt"
	"net/netip"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

// ErrInvalid is returned (wrapped) for input that cannot be rendered.
var ErrInvalid = errors.New("winws: invalid input")

// HostlistName is the file name the arguments use for the hostlist; winws2
// resolves it relative to its working directory.
const HostlistName = "hostlist.txt"

var (
	defaultLuaInit = []string{"zapret-lib.lua", "zapret-antidpi.lua"}
	defaultPorts   = []uint16{443, 80}
)

// Input is everything needed to render a winws2 command line.
type Input struct {
	Binary       string   // path to winws2.exe (WinDivert.dll, WinDivert64.sys, cygwin1.dll next to it)
	LuaDir       string   // default: <dir of Binary>/lua
	LuaInit      []string // Lua files in LuaDir to load, in order; default zapret-lib.lua, zapret-antidpi.lua
	Strategy     []string // zapret2 profile args, passed through verbatim
	Hostlist     []string // domains
	IfIndex      int      // uplink interface index
	ExcludeIPs   []netip.Prefix
	ExcludePorts [][2]uint16
	Ports        []uint16
}

// Output is the rendered command line and hostlist file.
type Output struct {
	Path     string
	Args     []string
	Hostlist []byte // file content; args reference it by name "hostlist.txt"
}

// Render builds the winws2 arguments and the hostlist. Equal input gives equal output.
func Render(in Input) (Output, error) {
	if in.Binary == "" {
		return Output{}, fmt.Errorf("%w: no binary", ErrInvalid)
	}
	hostlist, err := renderHostlist(in.Hostlist)
	if err != nil {
		return Output{}, err
	}
	filter, err := Filter(in.IfIndex, in.ExcludeIPs, in.ExcludePorts)
	if err != nil {
		return Output{}, err
	}
	ports, err := portList(in.Ports)
	if err != nil {
		return Output{}, err
	}
	luaDir := strings.TrimRight(in.LuaDir, `/\`)
	if in.LuaDir == "" {
		luaDir = filepath.Join(filepath.Dir(in.Binary), "lua")
	}
	luaInit := in.LuaInit
	if len(luaInit) == 0 {
		luaInit = defaultLuaInit
	}

	args := []string{"--wf-tcp-out=" + ports, "--wf-raw-filter=" + filter}
	for _, f := range luaInit {
		if f == "" || strings.ContainsAny(f, `/\`) || strings.Contains(f, "..") {
			return Output{}, fmt.Errorf("%w: LuaInit file name %q", ErrInvalid, f)
		}
		args = append(args, "--lua-init=@"+luaDir+"/"+f)
	}
	args = append(args, "--hostlist="+HostlistName)
	args = append(args, in.Strategy...)
	return Output{Path: in.Binary, Args: args, Hostlist: hostlist}, nil
}

// renderHostlist returns the domains sorted, deduplicated, one per line.
func renderHostlist(domains []string) ([]byte, error) {
	seen := make(map[string]struct{}, len(domains))
	for _, d := range domains {
		d = strings.TrimSpace(d)
		if d == "" || strings.ContainsAny(d, " \t\r\n") {
			return nil, fmt.Errorf("%w: hostlist entry %q", ErrInvalid, d)
		}
		seen[d] = struct{}{}
	}
	if len(seen) == 0 {
		return nil, fmt.Errorf("%w: no domains", ErrInvalid)
	}
	list := make([]string, 0, len(seen))
	for d := range seen {
		list = append(list, d)
	}
	sort.Strings(list)
	return []byte(strings.Join(list, "\n") + "\n"), nil
}

// portList renders --wf-tcp-out: sorted, deduplicated, comma separated.
func portList(ports []uint16) (string, error) {
	if len(ports) == 0 {
		ports = defaultPorts
	}
	set := make(map[uint16]struct{}, len(ports))
	for _, p := range ports {
		if p == 0 {
			return "", fmt.Errorf("%w: port 0", ErrInvalid)
		}
		set[p] = struct{}{}
	}
	list := make([]int, 0, len(set))
	for p := range set {
		list = append(list, int(p))
	}
	sort.Ints(list)
	parts := make([]string, len(list))
	for i, p := range list {
		parts[i] = strconv.Itoa(p)
	}
	return strings.Join(parts, ","), nil
}
