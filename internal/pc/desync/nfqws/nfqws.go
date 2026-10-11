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
	// ifaceName is what a Linux interface name may contain here; it keeps
	// the name safe inside an nft string and a shell.
	ifaceName = regexp.MustCompile(`^[A-Za-z0-9_.:@+-]{1,15}$`)
)

// Input is everything needed to render a nfqws2 command line and its nft rules.
type Input struct {
	Binary       string   // path to nfqws2
	LuaDir       string   // default: <dir of Binary>/lua
	LuaInit      []string // default zapret-lib.lua, zapret-antidpi.lua
	Strategy     []string // zapret2 profile args, passed through verbatim
	Hostlist     []string
	Uplink       string // oifname
	QueueNum     uint16 // default 200
	ExcludeIPs   []netip.Prefix
	ExcludePorts [][2]uint16
	Ports        []uint16
	Mark         uint32 // mark nfqws2 puts on its own packets (avoid loops); default 0x40000000 (zapret2 default)
}

// Output is the rendered command line, hostlist and nft command lists.
type Output struct {
	Path     string
	Args     []string
	Hostlist []byte
	NFTSetup []string // shell commands, printed for the human
	NFTDown  []string
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
	ports, err := portList(in.Ports)
	if err != nil {
		return Output{}, err
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
	args = append(args, "--hostlist="+HostlistName)
	args = append(args, in.Strategy...)

	return Output{
		Path:     in.Binary,
		Args:     args,
		Hostlist: hostlist,
		NFTSetup: nftRules(in.Uplink, qnum, mark, ports, in.ExcludeIPs, in.ExcludePorts),
		NFTDown:  nftDown(),
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

// portList returns the destination ports sorted and deduplicated.
func portList(ports []uint16) ([]uint16, error) {
	if len(ports) == 0 {
		ports = defaultPorts
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
