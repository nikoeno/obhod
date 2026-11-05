package nfqws

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
var ErrInvalid = errors.New("nfqws: invalid input")

// HostlistName is the file name the arguments use for the hostlist; nfqws2
// resolves it relative to its working directory.
const HostlistName = "hostlist.txt"

// Identity is the --uid argument the renderer always passes: nfqws2 keeps the
// uid of the app (root) instead of dropping to uid 2147483647. See API.md.
const Identity = "--uid=0:0"

const (
	// DefaultMark is the mark zapret2 puts on its own packets.
	DefaultMark uint32 = 0x40000000
	// DefaultQueueNum is the NFQUEUE number used when Input.QueueNum is 0.
	DefaultQueueNum uint16 = 200

	table = "splitter"
)

var (
	defaultLuaInit = []string{"zapret-lib.lua", "zapret-antidpi.lua"}
	defaultPorts   = []uint16{443, 80}
	// luaName is what a blob name may be: it becomes a Lua global variable.
	luaName = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)
	// ifaceName is what a Linux interface name may contain here; it keeps
	// the name safe inside an nft string and a shell.
	ifaceName = regexp.MustCompile(`^[A-Za-z0-9_.:@+-]{1,15}$`)
)

// Blob is a binary payload that nfqws2 loads into the Lua variable Name
// (--blob=Name:@Path). Lua instances refer to it by Name.
type Blob struct {
	Name string // a Lua identifier
	Path string // file to load; the caller has checked it is one of its own files
}

// Input is everything needed to render a nfqws2 command line and its nft rules.
type Input struct {
	Binary       string   // path to nfqws2
	LuaDir       string   // default: <dir of Binary>/lua
	LuaInit      []string // default zapret-lib.lua, zapret-antidpi.lua
	Strategy     []string // zapret2 profile args, passed through verbatim (one profile); not with Profiles
	Hostlist     []string
	Uplink       string // oifname
	QueueNum     uint16 // default 200
	ExcludeIPs   []netip.Prefix
	ExcludePorts [][2]uint16
	Ports        []uint16 // TCP destination ports to queue; default 80, 443
	Mark         uint32   // mark nfqws2 puts on its own packets (avoid loops); default 0x40000000 (zapret2 default)

	// Profiles is the multi-profile form: each entry holds the match and
	// instance options of one profile (--filter-tcp=..., --payload=...,
	// --lua-desync=...). The renderer starts every profile with
	// --hostlist=hostlist.txt and separates them with --new, so none of them
	// can leave the hostlist. Not combined with Strategy.
	Profiles [][]string
	UDPPorts []uint16 // UDP destination ports to queue (an extra nft rule); only with Profiles
	Blobs    []Blob   // --blob options placed before the profiles; only with Profiles
}

// Output is the rendered command line, hostlist and nft command lists.
type Output struct {
	Path     string
	Args     []string
	Hostlist []byte
	NFTSetup []string // shell commands (nft rules, then the ip rule exemption), printed for the human
	NFTDown  []string // the matching removal commands
}

// Render builds the nfqws2 arguments, the hostlist and the nft commands.
// Equal input gives equal output.
func Render(in Input) (Output, error) {
	if in.Binary == "" {
		return Output{}, fmt.Errorf("%w: no binary", ErrInvalid)
	}
	if !ifaceName.MatchString(in.Uplink) {
		return Output{}, fmt.Errorf("%w: uplink %q is not a safe interface name", ErrInvalid, in.Uplink)
	}
	hostlist, err := renderHostlist(in.Hostlist)
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
	var udpPorts []uint16
	if len(in.UDPPorts) > 0 {
		if udpPorts, err = portList(in.UDPPorts, nil); err != nil {
			return Output{}, err
		}
	}
	for _, p := range in.ExcludeIPs {
		if !p.IsValid() {
			return Output{}, fmt.Errorf("%w: invalid exclude prefix", ErrInvalid)
		}
	}
	for _, r := range in.ExcludePorts {
		if r[0] == 0 || r[0] > r[1] {
			return Output{}, fmt.Errorf("%w: port range %d-%d", ErrInvalid, r[0], r[1])
		}
	}
	mark, qnum := in.Mark, in.QueueNum
	if mark == 0 {
		mark = DefaultMark
	}
	if qnum == 0 {
		qnum = DefaultQueueNum
	}

	luaDir := strings.TrimRight(in.LuaDir, `/\`)
	if in.LuaDir == "" {
		luaDir = filepath.Join(filepath.Dir(in.Binary), "lua")
	}
	luaInit := in.LuaInit
	if len(luaInit) == 0 {
		luaInit = defaultLuaInit
	}
	args := []string{"--qnum=" + strconv.Itoa(int(qnum))}
	if mark != DefaultMark {
		args = append(args, fmt.Sprintf("--fwmark=0x%08x", mark))
	}
	for _, f := range luaInit {
		if f == "" || strings.ContainsAny(f, `/\`) || strings.Contains(f, "..") {
			return Output{}, fmt.Errorf("%w: LuaInit file name %q", ErrInvalid, f)
		}
		args = append(args, "--lua-init=@"+luaDir+"/"+f)
	}
	if len(in.Profiles) == 0 {
		args = append(args, "--hostlist="+HostlistName, Identity)
		args = append(args, in.Strategy...)
	} else {
		args = append(args, Identity)
		for _, b := range in.Blobs {
			if !luaName.MatchString(b.Name) || b.Path == "" || strings.ContainsAny(b.Path, "\r\n") || strings.HasPrefix(b.Path, "+") {
				return Output{}, fmt.Errorf("%w: blob %q", ErrInvalid, b.Name)
			}
			args = append(args, "--blob="+b.Name+":@"+b.Path)
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
	}

	return Output{
		Path:     in.Binary,
		Args:     args,
		Hostlist: hostlist,
		NFTSetup: append(nftRules(in.Uplink, qnum, mark, ports, udpPorts, in.ExcludeIPs, in.ExcludePorts), ipRuleSetup(mark)...),
		NFTDown:  append(nftDown(), ipRuleDown(mark)...),
	}, nil
}

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

// portList returns the destination ports sorted and deduplicated. An empty
// list becomes def.
func portList(ports, def []uint16) ([]uint16, error) {
	if len(ports) == 0 {
		ports = def
	}
	set := make(map[uint16]struct{}, len(ports))
	for _, p := range ports {
		if p == 0 {
			return nil, fmt.Errorf("%w: port 0", ErrInvalid)
		}
		set[p] = struct{}{}
	}
	list := make([]uint16, 0, len(set))
	for p := range set {
		list = append(list, p)
	}
	sort.Slice(list, func(i, j int) bool { return list[i] < list[j] })
	return list, nil
}
