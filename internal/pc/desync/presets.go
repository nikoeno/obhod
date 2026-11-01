package desync

import (
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// Preset names that are not catalogue entries.
const (
	// PresetCustom means "use Config.Strategy" (one hostlist-only profile).
	PresetCustom = "custom"
	// PresetAuto is reserved for the automatic choice (planned stage 2); Check
	// refuses it until it exists.
	PresetAuto = "auto"
	// DefaultPreset is used when Config.Preset and Config.Strategy are both empty.
	DefaultPreset = "alt2-multisplit"
	// FakeDir is the folder next to the engine binary that holds the fake payload files.
	FakeDir = "fake"
)

// Proto is the transport protocol a profile matches.
type Proto string

// The transports a profile can match.
const (
	TCP Proto = "tcp"
	UDP Proto = "udp"
)

// Instance is one Lua desync call of a profile.
type Instance struct {
	// Payload limits the call to these payload types (--payload=...); "" means
	// all (the antidpi functions then act on known payloads only).
	Payload string
	// Spec is the function and its arguments as written after --lua-desync=.
	// {name} marks a blob: a catalogue payload (see BlobFiles) or a built-in
	// one (fake_default_tls, fake_default_http, fake_default_quic).
	Spec string
}

// Profile is one match (transport and destination ports) and the calls made
// for it. The hostlist, the interface and the exclusions are added by the
// renderer, never by a profile.
type Profile struct {
	Name      string // short id, unique within a preset
	Proto     Proto
	Ports     []uint16
	Instances []Instance
}

// Preset is a named, ready-made strategy: an ordered list of profiles.
type Preset struct {
	ID          string // stable id, the value of desync.preset
	Name        string // display name
	Description string // one plain sentence for the user
	Source      string // the zapret1 pack file it was translated from
	Note        string // non-empty when the translation differs from the pack (see PRESETS.md)
	Profiles    []Profile
}

// Presets returns the catalogue in display order. The result is a copy.
func Presets() []Preset {
	out := make([]Preset, len(catalogue))
	for i, p := range catalogue {
		out[i] = p.clone()
	}
	return out
}

// LookupPreset returns the preset with the given id.
func LookupPreset(id string) (Preset, bool) {
	for _, p := range catalogue {
		if p.ID == id {
			return p.clone(), true
		}
	}
	return Preset{}, false
}

// PresetIDs returns the catalogue ids in display order.
func PresetIDs() []string {
	ids := make([]string, len(catalogue))
	for i, p := range catalogue {
		ids[i] = p.ID
	}
	return ids
}

func (p Preset) clone() Preset {
	p.Profiles = append([]Profile(nil), p.Profiles...)
	for i, pr := range p.Profiles {
		pr.Ports = append([]uint16(nil), pr.Ports...)
		pr.Instances = append([]Instance(nil), pr.Instances...)
		p.Profiles[i] = pr
	}
	return p
}

// blobFiles maps the logical blob names presets use to file names inside the
// fake folder. A preset can name nothing else: a path or an @file never comes
// from configuration.
var blobFiles = map[string]string{
	"fake_tls_google":  "tls_clienthello_www_google_com.bin",
	"fake_tls_4pda":    "tls_clienthello_4pda_to.bin",
	"fake_tls_max_ru":  "tls_clienthello_max_ru.bin",
	"fake_quic_google": "quic_initial_www_google_com.bin",
	"fake_stun2":       "stun2.bin",
}

// builtinBlobs are the blobs the engine defines itself (zapret2 manual,
// "Standard blobs"); they need no file.
var builtinBlobs = map[string]bool{
	"fake_default_tls":  true,
	"fake_default_http": true,
	"fake_default_quic": true,
}

// BlobFiles returns the catalogue payloads: logical blob name to file name in
// the fake folder. The result is a copy.
func BlobFiles() map[string]string {
	out := make(map[string]string, len(blobFiles))
	for k, v := range blobFiles {
		out[k] = v
	}
	return out
}

var blobRef = regexp.MustCompile(`\{([a-z0-9_]+)\}`)

// renderedPreset is a preset turned into engine arguments.
type renderedPreset struct {
	profiles [][]string
	tcp      []uint16
	udp      []uint16
	blobs    []blobUse // sorted by name
}

type blobUse struct{ name, file string }

// renderPreset turns p into profile argument lists and the port and blob
// sets they need. It fails on a blob that is neither a catalogue nor a
// built-in one, a profile with no ports or an unknown transport.
func renderPreset(p Preset) (renderedPreset, error) {
	var r renderedPreset
	used := map[string]bool{}
	tcp, udp := map[uint16]bool{}, map[uint16]bool{}
	for _, pr := range p.Profiles {
		if len(pr.Ports) == 0 || len(pr.Instances) == 0 {
			return r, fmt.Errorf("preset %s profile %s: no ports or no calls", p.ID, pr.Name)
		}
		ports := make([]string, len(pr.Ports))
		for i, port := range pr.Ports {
			ports[i] = strconv.Itoa(int(port))
			switch pr.Proto {
			case TCP:
				tcp[port] = true
			case UDP:
				udp[port] = true
			}
		}
		var args []string
		switch pr.Proto {
		case TCP:
			args = append(args, "--filter-tcp="+strings.Join(ports, ","))
		case UDP:
			args = append(args, "--filter-udp="+strings.Join(ports, ","))
		default:
			return r, fmt.Errorf("preset %s profile %s: transport %q", p.ID, pr.Name, pr.Proto)
		}
		payload := "all"
		for _, in := range pr.Instances {
			want := in.Payload
			if want == "" {
				want = "all"
			}
			if want != payload {
				args = append(args, "--payload="+want)
				payload = want
			}
			var bad string
			spec := blobRef.ReplaceAllStringFunc(in.Spec, func(m string) string {
				name := m[1 : len(m)-1]
				switch {
				case builtinBlobs[name]:
				case blobFiles[name] != "":
					used[name] = true
				default:
					bad = name
				}
				return name
			})
			if bad != "" {
				return r, fmt.Errorf("preset %s profile %s: unknown blob %q", p.ID, pr.Name, bad)
			}
			if strings.ContainsAny(spec, "{}@ ") {
				return r, fmt.Errorf("preset %s profile %s: bad call %q", p.ID, pr.Name, in.Spec)
			}
			// Whatever a call names as a blob must be a catalogue or built-in
			// payload or a hex literal: never a path.
			for _, f := range strings.Split(spec, ":")[1:] {
				k, v, _ := strings.Cut(f, "=")
				if (k == "blob" || k == "seqovl_pattern" || k == "pattern") && !builtinBlobs[v] && blobFiles[v] == "" && !strings.HasPrefix(v, "0x") {
					return r, fmt.Errorf("preset %s profile %s: %s=%q is not a blob name", p.ID, pr.Name, k, v)
				}
			}
			args = append(args, "--lua-desync="+spec)
		}
		r.profiles = append(r.profiles, args)
	}
	r.tcp, r.udp = sortedPorts(tcp), sortedPorts(udp)
	names := make([]string, 0, len(used))
	for n := range used {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		r.blobs = append(r.blobs, blobUse{name: n, file: blobFiles[n]})
	}
	return r, nil
}

func sortedPorts(set map[uint16]bool) []uint16 {
	out := make([]uint16, 0, len(set))
	for p := range set {
		out = append(out, p)
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}

// fakeDirOf returns the fake folder next to the engine binary, joined with
// "/" (the renderers do the same for the Lua folder; a pure string operation,
// so a plan does not depend on the OS it is built on).
func fakeDirOf(binary string) string {
	i := strings.LastIndexAny(binary, `/\`)
	if i < 0 {
		return FakeDir
	}
	return binary[:i] + "/" + FakeDir
}

// ---- the catalogue ---------------------------------------------------------
//
// Translated from the zapret1 "general*.bat" pack (winws.exe) to zapret2 Lua
// calls; internal/pc/desync/PRESETS.md has the option-by-option table and
// lists what could not be translated faithfully.

// Fooling arguments (zapret2 names). The increments are the zapret1 defaults.
const (
	foolTS  = "tcp_ts=-600000" // fooling=ts: stale timestamp (--dpi-desync-ts-increment default)
	foolMD5 = "tcp_md5"        // fooling=md5sig
	foolSeq = "tcp_seq=-10000" // fooling=badseq without --dpi-desync-badseq-increment
	modG    = "tls_mod=rnd,dupsid,sni=www.google.com"
	modYa   = "tls_mod=rnd,dupsid,sni=ya.ru"
	tlsPl   = "tls_client_hello"
	httpPl  = "http_req"
	blobG   = "{fake_tls_google}"
	blob4   = "{fake_tls_4pda}"
	blobM   = "{fake_tls_max_ru}"
	blobT   = "{fake_default_tls}"
	blobS2  = "{fake_stun2}"
)

func seq(n int) string { return "tcp_seq=" + strconv.Itoa(n) }

func spec(fn string, args ...string) string {
	for _, a := range args {
		if a != "" {
			fn += ":" + a
		}
	}
	return fn
}

func repeats(n int) string {
	if n > 1 {
		return "repeats=" + strconv.Itoa(n)
	}
	return ""
}

// fake is a fake packet for one payload type.
func fake(payload, blob string, rep int, extra ...string) Instance {
	return Instance{payload, spec("fake", append([]string{"blob=" + blob, repeats(rep)}, extra...)...)}
}

func multisplit(pos string, seqovl int, pattern string) Instance {
	return Instance{"", spec("multisplit", "pos="+pos, "seqovl="+strconv.Itoa(seqovl), "seqovl_pattern="+pattern)}
}

func multisplitPlain(pos string) Instance { return Instance{"", spec("multisplit", "pos="+pos)} }

func disorder(pos string) Instance { return Instance{"", spec("multidisorder_legacy", "pos="+pos)} }

func fakedsplit(pos string, rep int, fool string) Instance {
	return Instance{"", spec("fakedsplit", "pos="+pos, "pattern=0x00", repeats(rep), fool)}
}

func hostfake(host string, rep int, extra ...string) Instance {
	return Instance{"", spec("hostfakesplit", append([]string{"host=" + host, repeats(rep)}, extra...)...)}
}

func quic(rep int) Profile {
	return Profile{"quic", UDP, []uint16{443}, []Instance{fake("quic_initial", "{fake_quic_google}", rep)}}
}

// alt is the profile for the other TLS ports the pack uses for Discord's voice
// hosts (discord.media): 2053, 2083, 2087, 2096, 8443.
func alt(in ...Instance) Profile {
	return Profile{"alt-ports", TCP, []uint16{2053, 2083, 2087, 2096, 8443}, in}
}

// web is the profile for HTTP and HTTPS.
func web(in ...Instance) Profile {
	return Profile{"web", TCP, []uint16{80, 443}, in}
}

// preset assembles the usual three profiles: QUIC, other TLS ports, web.
func preset(id, name, source, note, desc string, q, a, w Profile) Preset {
	return Preset{ID: id, Name: name, Description: desc, Source: source, Note: note, Profiles: []Profile{q, a, w}}
}

const noteStun = "The pack prepends a STUN payload to the fake TLS; zapret2 cannot join two payloads, so the fake is the TLS part only."

var catalogue = []Preset{
	preset("alt2-multisplit", "ALT2 (multisplit)", "general (ALT2).bat", "",
		"Splits the first packet after its second byte and hides a decoy prefix in front of it. Sends no fake packets.",
		quic(6),
		alt(multisplit("2", 652, blobG)),
		web(multisplit("2", 652, blobG))),

	preset("alt3-hostfakesplit", "ALT3 (host fake split)", "general (ALT3).bat",
		"altorder=1 has no zapret2 option; nofake1 is used as the closest (unverified).",
		"Sends a fake hello that names www.google.com (ya.ru for the web), then splits the real one around a fake host name.",
		quic(6),
		alt(fake(tlsPl, blobT, 1, foolTS, modG), hostfake("www.google.com", 1, "nofake1", foolTS)),
		web(fake(tlsPl, blobT, 1, foolTS, modYa), fake(httpPl, blobM, 1, foolTS), hostfake("ya.ru", 1, "nofake1", foolTS))),

	preset("alt-fakedsplit", "ALT (fake and fake split)", "general (ALT).bat", noteStun,
		"Sends fake hellos with a stale timestamp, then splits the real packet with fakes between the pieces.",
		quic(6),
		alt(fake(tlsPl, blobG, 6, foolTS), fakedsplit("2", 6, foolTS)),
		web(fake(tlsPl, blobG, 6, foolTS), fake(httpPl, blobM, 6, foolTS), fakedsplit("2", 6, foolTS))),

	preset("general-multisplit", "General (multisplit)", "general.bat", "",
		"Splits the first packet after its first byte and hides a decoy prefix in front of it. Sends no fake packets.",
		quic(6),
		alt(multisplit("1", 681, blobG)),
		web(multisplit("1", 568, blob4))),

	preset("alt4-fake-multisplit", "ALT4 (fake and multisplit)", "general (ALT4).bat", noteStun,
		"Sends fake hellos with a shifted sequence number, then splits the real packet in two.",
		quic(6),
		alt(fake(tlsPl, blobG, 6, seq(1000)), multisplitPlain("2")),
		web(fake(tlsPl, blobG, 6, seq(1000)), fake(httpPl, blobM, 6, seq(1000)), multisplitPlain("2"))),

	preset("alt5-multidisorder", "ALT5 (disorder)", "general (ALT5).bat",
		"The pack also sends data in the SYN packet for every site; that cannot be limited to a hostlist and is left out.",
		"Splits the first packet in two and sends the pieces in reverse order.",
		quic(6),
		alt(disorder("2")),
		web(disorder("2"))),

	preset("alt6-multisplit-681", "ALT6 (multisplit, long decoy)", "general (ALT6).bat", "",
		"Splits the first packet after its first byte and hides a 681-byte decoy prefix in front of it.",
		quic(6),
		alt(multisplit("1", 681, blobG)),
		web(multisplit("1", 681, blobG))),

	preset("alt7-multisplit-sniext", "ALT7 (multisplit at SNI)", "general (ALT7).bat", "",
		"Splits the first packet at two places, one just inside the server-name field, with a decoy prefix.",
		quic(6),
		alt(multisplit("2,sniext+1", 679, blobG)),
		web(multisplit("2,sniext+1", 679, blobG))),

	preset("alt8-fake-badseq", "ALT8 (fake, shifted sequence)", "general (ALT8).bat", "",
		"Sends built-in fake hellos with a slightly shifted sequence number before the real packet.",
		quic(6),
		alt(fake(tlsPl, blobT, 6, seq(2))),
		web(fake(tlsPl, blobT, 6, seq(2)), fake(httpPl, blobM, 6, seq(2)))),

	preset("alt9-hostfakesplit-ozon", "ALT9 (host fake split, ozon.ru)", "general (ALT9).bat", "",
		"Splits the real packet around a fake host name (ozon.ru for the web) sent with a stale timestamp.",
		quic(6),
		alt(hostfake("www.google.com", 4, foolTS)),
		web(hostfake("ozon.ru", 4, foolTS, foolMD5))),

	preset("alt10-fake-ts", "ALT10 (fake, stale timestamp)", "general (ALT10).bat", noteStun,
		"Sends fake hellos with a stale timestamp before the real packet.",
		quic(6),
		alt(fake(tlsPl, blobG, 6, foolTS)),
		web(fake(tlsPl, blob4, 6, foolTS), fake(httpPl, blobM, 6, foolTS))),

	preset("alt11-fake-multisplit", "ALT11 (fake and multisplit)", "general (ALT11).bat", noteStun,
		"Sends several fake hellos with a stale timestamp, then splits the real packet with a decoy prefix.",
		quic(11),
		alt(fake(tlsPl, blobG, 8, foolTS), multisplit("1", 681, blobG)),
		web(fake(tlsPl, blobM, 8, foolTS), fake(httpPl, blobM, 8, foolTS), multisplit("1", 664, blobM))),

	preset("alt12-fake-multisplit-google", "ALT12 (fake and multisplit, Google variant)", "general (ALT12).bat",
		noteStun+" The pack's difference from ALT11 is a separate Google profile (host fake split), which is merged into the web profile, so the two render the same.",
		"Same as ALT11 here; the pack differs only for Google and YouTube hosts.",
		quic(11),
		alt(fake(tlsPl, blobG, 8, foolTS), multisplit("1", 681, blobG)),
		web(fake(tlsPl, blobM, 8, foolTS), fake(httpPl, blobM, 8, foolTS), multisplit("1", 664, blobM))),

	preset("exp-fake-multisplit", "EXP (experimental)", "general (EXP).bat", "",
		"Sends fake hellos with a stale timestamp, then splits the real packet with a STUN-shaped decoy prefix.",
		quic(11),
		alt(fake(tlsPl, blobG, 8, foolTS), multisplit("1", 681, blobG)),
		web(fake(tlsPl, blobM, 4, foolTS), fake(httpPl, blobM, 4, foolTS), multisplit("1", 480, blobS2))),

	preset("faketls-auto", "Fake TLS auto (disorder)", "general (FAKE TLS AUTO).bat",
		"The pack's fake is 4 zero bytes followed by a second payload (\"^!\"); zapret2 cannot join payloads, so the built-in fake TLS with a random session and the SNI mod is used.",
		"Sends built-in fake hellos with a wrong sequence number, then splits the real packet and sends the pieces in reverse order.",
		quic(11),
		alt(fake(tlsPl, blobT, 11, foolSeq, modG), disorder("1,midsld")),
		web(fake(tlsPl, blobT, 11, foolSeq, modG), fake(httpPl, blobM, 11, foolSeq), disorder("1,midsld"))),

	preset("faketls-auto-alt-fakedsplit", "Fake TLS auto ALT (fake split)", "general (FAKE TLS AUTO ALT).bat", "",
		"Sends built-in fake hellos with a shifted sequence number, then splits the real packet with fakes between the pieces.",
		quic(11),
		alt(fake(tlsPl, blobT, 8, seq(2), modG), fakedsplit("1", 8, seq(2))),
		web(fake(tlsPl, blobT, 8, seq(2), modG), fake(httpPl, blobM, 8, seq(2)), fakedsplit("1", 8, seq(2)))),

	preset("faketls-auto-alt2-multisplit", "Fake TLS auto ALT2 (multisplit)", "general (FAKE TLS AUTO ALT2).bat", "",
		"Sends built-in fake hellos with a far-off sequence number, then splits the real packet with a decoy prefix.",
		quic(11),
		alt(fake(tlsPl, blobT, 8, seq(10000000), modG), multisplit("1", 681, blobG)),
		web(fake(tlsPl, blobT, 8, seq(10000000), modG), fake(httpPl, blobM, 8, seq(10000000)), multisplit("1", 681, blobG))),

	preset("faketls-auto-alt3-multisplit-ts", "Fake TLS auto ALT3 (multisplit, timestamp)", "general (FAKE TLS AUTO ALT3).bat", "",
		"Sends built-in fake hellos with a stale timestamp, then splits the real packet with a decoy prefix.",
		quic(11),
		alt(fake(tlsPl, blobT, 8, foolTS, modG), multisplit("1", 681, blobG)),
		web(fake(tlsPl, blobT, 8, foolTS, modG), fake(httpPl, blobM, 8, foolTS), multisplit("1", 681, blobG))),

	preset("simple-fake", "Simple fake", "general (SIMPLE FAKE).bat", "",
		"Sends fake hellos with a stale timestamp before the real packet. No splitting.",
		quic(6),
		alt(fake(tlsPl, blobG, 6, foolTS)),
		web(fake(tlsPl, blobG, 6, foolTS), fake(httpPl, blobM, 6, foolTS))),

	preset("simple-fake-alt", "Simple fake ALT (shifted sequence)", "general (SIMPLE FAKE ALT).bat", noteStun,
		"Sends fake hellos with a slightly shifted sequence number before the real packet. No splitting.",
		quic(6),
		alt(fake(tlsPl, blobG, 6, seq(2))),
		web(fake(tlsPl, blobG, 6, seq(2)), fake(httpPl, blobM, 6, seq(2)))),

	preset("simple-fake-alt2", "Simple fake ALT2 (stale timestamp)", "general (SIMPLE FAKE ALT2).bat", noteStun,
		"Sends fake hellos modelled on max.ru with a stale timestamp before the real packet. No splitting.",
		quic(6),
		alt(fake(tlsPl, blobG, 6, foolTS)),
		web(fake(tlsPl, blobM, 6, foolTS), fake(httpPl, blobM, 6, foolTS))),
}
