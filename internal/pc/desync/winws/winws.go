package winws

import (
	"errors"
	"fmt"
	"net/netip"
	"path/filepath"
	"regexp"
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
	// luaName is what a blob name may be: it becomes a Lua global variable.
	luaName = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)
)

// Blob is a binary payload that winws2 loads into the Lua variable Name
// (--blob=Name:@Path). Lua instances refer to it by Name.
type Blob struct {
	Name string // a Lua identifier
	Path string // file to load; the caller has checked it is one of its own files
}

// Input is everything needed to render a winws2 command line.
type Input struct {
	Binary       string   // path to winws2.exe (WinDivert.dll, WinDivert64.sys, cygwin1.dll next to it)
	LuaDir       string   // default: <dir of Binary>/lua
	LuaInit      []string // Lua files in LuaDir to load, in order; default zapret-lib.lua, zapret-antidpi.lua
	Strategy     []string // zapret2 profile args, passed through verbatim (one profile); not with Profiles
	Hostlist     []string // domains
	IfIndex      int      // uplink interface index
	ExcludeIPs   []netip.Prefix
	ExcludePorts [][2]uint16
	Ports        []uint16 // TCP destination ports to intercept; default 80, 443

	// Profiles is the multi-profile form: each entry holds the match and
	// instance options of one profile (--filter-tcp=..., --payload=...,
	// --lua-desync=...). The renderer starts every profile with
	// --hostlist=hostlist.txt and separates them with --new, so none of them
	// can leave the hostlist. Not combined with Strategy.
	Profiles [][]string
	UDPPorts []uint16 // UDP destination ports to intercept (--wf-udp-out); only with Profiles
	Blobs    []Blob   // --blob options placed before the profiles; only with Profiles
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
	if len(in.Profiles) > 0 && len(in.Strategy) > 0 {
		return Output{}, fmt.Errorf("%w: Strategy and Profiles are exclusive", ErrInvalid)
	}
	if len(in.Profiles) == 0 && (len(in.UDPPorts) > 0 || len(in.Blobs) > 0) {
		return Output{}, fmt.Errorf("%w: UDPPorts and Blobs need Profiles", ErrInvalid)
	}
	ports, err := portList(in.Ports, defaultPorts)
	if err != nil {
		return Output{}, err
	}
	udpPorts := ""
	if len(in.UDPPorts) > 0 {
		if udpPorts, err = portList(in.UDPPorts, nil); err != nil {
			return Output{}, err
		}
	}
	luaDir := strings.TrimRight(in.LuaDir, `/\`)
	if in.LuaDir == "" {
		luaDir = filepath.Join(filepath.Dir(in.Binary), "lua")
	}
	luaInit := in.LuaInit
	if len(luaInit) == 0 {
		luaInit = defaultLuaInit
	}

	args := []string{"--wf-tcp-out=" + ports}
	if udpPorts != "" {
		args = append(args, "--wf-udp-out="+udpPorts)
	}
	args = append(args, "--wf-raw-filter="+filter)
	for _, f := range luaInit {
		if f == "" || strings.ContainsAny(f, `/\`) || strings.Contains(f, "..") {
			return Output{}, fmt.Errorf("%w: LuaInit file name %q", ErrInvalid, f)
		}
		args = append(args, "--lua-init=@"+luaDir+"/"+f)
	}
	for _, b := range in.Blobs {
		if !luaName.MatchString(b.Name) || b.Path == "" || strings.ContainsAny(b.Path, "\r\n") || strings.HasPrefix(b.Path, "+") {
			return Output{}, fmt.Errorf("%w: blob %q", ErrInvalid, b.Name)
		}
		args = append(args, "--blob="+b.Name+":@"+b.Path)
	}
	if len(in.Profiles) == 0 {
		args = append(args, "--hostlist="+HostlistName)
		args = append(args, in.Strategy...)
		return Output{Path: in.Binary, Args: args, Hostlist: hostlist}, nil
	}
	for i, p := range in.Profiles {
		if len(p) == 0 {
			return Output{}, fmt.Errorf("%w: empty profile %d", ErrInvalid, i)
		}
		if i > 0 {
			args = append(args, "--new")
		}
		args = append(args, "--hostlist="+HostlistName)
		args = append(args, p...)
	}
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

// portList renders a --wf-*-out port list: sorted, deduplicated, comma
// separated. An empty list becomes def.
func portList(ports, def []uint16) (string, error) {
	if len(ports) == 0 {
		ports = def
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
