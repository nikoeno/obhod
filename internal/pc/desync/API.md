# Module: desync

| Field | Value |
|---|---|
| Path | internal/pc/desync |
| Kind | child |
| Binary | – |
| Purpose | builds the local desync engine plan (zapret2: winws2 or nfqws2) for the desync domains, from a named preset or a custom strategy |
| Consumers | internal/pc |
| Children | internal/pc/desync/winws, internal/pc/desync/nfqws |
| Uses libs | internal/lib/route, internal/lib/netif |
| Build tags | – |
| Status | implemented |

## Responsibility

One interface for "mangle packets for these domains only", with the engine chosen per OS. It enforces the safety exclusions: never touch the TUN, the VPS IP, or probe ports.

## Public API

```go
package desync

type Engine string

const (
    WinWS Engine = "winws2" // Windows (zapret2)
    NFQWS Engine = "nfqws2" // Linux (zapret2)
)

type Config struct {
    Engine       Engine   // default by GOOS
    Binary       string
    Preset       string   // catalogue id, "custom" (use Strategy) or "auto" (reserved); "" = custom if Strategy is set, else DefaultPreset
    Strategy     []string // zapret2 profile args (e.g. --filter-l7=tls --payload=tls_client_hello --lua-desync=...); only with preset custom
    LuaInit      []string // Lua libraries to load; default zapret-lib.lua, zapret-antidpi.lua
    ExcludeIPs   []netip.Prefix // VPS IP(s); required
    ExcludePorts []PortRange    // probe source-port ranges (future)
    Ports        []uint16       // TCP ports to act on; default 443, 80; only with preset custom
}

// PresetID returns the preset the config selects (Preset, else custom when Strategy is set, else DefaultPreset).
func (c Config) PresetID() string

// PortRange is an inclusive port range: [0] is the first port, [1] the last.
// The children (winws, nfqws) take the same shape as [][2]uint16.
type PortRange [2]uint16

type Plan struct {
    Engine   Engine
    Path     string
    Args     []string
    Files    map[string][]byte // name → content (hostlist), written to Run dir by the app
    Requires []string          // files the arguments name (a preset's fake payloads, slash-separated); the app checks they exist before it starts the engine
    Setup    []string          // commands a human must run once (nfqws: nftables rules, then an `ip rule` exemption for the engine's fwmark); never run by us
    Teardown []string
}

// Build creates the plan for the desync domains in t, pinned to the physical uplink.
func Build(cfg Config, uplink netif.Interface, t route.Table) (Plan, error)
```

## Behavior / contract

- Only `t.Domains(route.Desync)` go into the hostlist. An empty list → a valid plan that the app doesn't start.
- Uplink-only: winws gets a WinDivert filter limited to the uplink interface index; nfqws gets nftables rules limited to the uplink `oifname`.
- nfqws on Linux: `Setup` ends with `ip rule add fwmark 0x40000000/0x40000000 lookup main priority 8999` and `Teardown` with the matching `ip rule del` (see `nfqws/API.md`); the app checks it with `ip rule show` before it starts the engine. winws has no such command.
- `ExcludeIPs` must not be empty (it protects the tunnel). Missing → error.

- Check order in `Build`: `ExcludeIPs` empty → `ErrNoExcludeIP` (before everything, even for an empty hostlist); unknown engine → `ErrUnsupported`; `Config.Check` → `ErrInvalidConfig`; no desync domains → `Plan{Engine}` (nothing to run); virtual uplink → `ErrVirtualUplink`; then the child renders.
- An explicit `Config.Engine` works on any OS (the renderers are pure); only the default depends on `runtime.GOOS`.
- Exported beyond the contract: `ErrVirtualUplink`, `ErrInvalidConfig`, `DefaultEngine()`, `HostlistFile`, `func (Config) Check() error`, and the preset API below.
- `Config.Check` rejects a `Strategy` that carries an option the app sets itself (any `--wf*` option, `--new`, `--skip`, `--hostlist*`, `--ipset*`, `--qnum`, `--fwmark`, `--lua-init`, `--uid`, `--user` (nfqws2 runs with `--uid=0:0`, set by the renderer), or an `@file` argument): each could undo the uplink restriction, the tunnel exclusion or the hostlist-only rule. A named preset has no such arguments (the catalogue is code); `Check` refuses `Strategy` and `Ports` with it. It also rejects port 0, an empty or reversed exclude port range, an invalid prefix and a `LuaInit` name with a path. The app calls it when it loads its config.
- IPv4-mapped IPv6 excludes (`::ffff:a.b.c.d/n`, n ≥ 96) are turned into IPv4 prefixes before rendering; otherwise they would become IPv6 clauses that never match IPv4 packets.

## Presets (added 2026-11-01)

A preset is a named, ready-made strategy: an ordered list of profiles (a match on TCP or UDP and destination ports, plus Lua calls). The catalogue is the 21 strategies of the zapret1 "general*.bat" pack translated to zapret2; `PRESETS.md` has the option-by-option table, the list of what could not be translated faithfully, the payloads and the lists. Exported:

```go
const (
    PresetCustom  = "custom"          // use Config.Strategy
    PresetAuto    = "auto"            // reserved; Check refuses it ("not available yet")
    DefaultPreset = "alt2-multisplit" // when Preset and Strategy are both empty
    FakeDir       = "fake"            // folder next to the engine binary that holds the fake payloads
)

type Proto string // TCP | UDP
type Instance struct{ Payload, Spec string } // --payload filter ("" = all) and the text after --lua-desync=; {name} is a blob
type Profile struct {
    Name  string
    Proto Proto
    Ports []uint16
    Instances []Instance
}
type Preset struct {
    ID, Name, Description string // stable id (the value of desync.preset), display name, one plain sentence
    Source string               // the pack file it was translated from
    Note   string               // non-empty when the translation differs from the pack
    Profiles []Profile
}

func Presets() []Preset                  // the catalogue in display order (a copy)
func LookupPreset(id string) (Preset, bool)
func PresetIDs() []string
func BlobFiles() map[string]string       // logical blob name → file name inside FakeDir (a copy)
```

Behavior:

- `Preset` is validated by `Check`: a catalogue id, `custom` or empty; `auto` and unknown names are `ErrInvalidConfig`. A named preset refuses `Strategy` and `Ports` (they are the custom form; silently ignoring them would hide a mistake). An older config that sets only `strategy` stays custom (`PresetID`); a config with neither gets `DefaultPreset`. The default is `alt2-multisplit` (chosen in PRESETS.md: the most faithful translation, no fake packets, two payload files).
- `Build` renders a preset into profiles: for each, `--filter-tcp=` or `--filter-udp=`, the `--payload` filters and the `--lua-desync=` calls. The child renderer starts each profile with `--hostlist=hostlist.txt` and separates them with `--new`, then adds the interface restriction, the exclusions and (Linux) the nft rules around all of them. **A preset cannot undo any of these**: the renderer owns every option in the reserved list, and the tests assert, for every preset and both engines, that no argument outside the profile filters, `--payload`, `--lua-desync` and the renderer's own options appears.
- The intercepted TCP ports are the union of the profiles' TCP ports (`80,443,2053,2083,2087,2096,8443` for every catalogue preset) and the UDP ports the union of their UDP ports (`443`). UDP 443 reaches the engine only if the TUN rule that rejects QUIC (`singbox.block_quic`, default true) is off; with it on the QUIC profile simply never sees a packet.
- Payloads: a call names a payload as `{fake_tls_google}` (catalogue, resolved to a file in `FakeDir` next to `Config.Binary`, rendered as `--blob=fake_tls_google:@<dir>/tls_clienthello_www_google_com.bin`) or `{fake_default_tls}` / `fake_default_http` / `fake_default_quic` (built into the engine; no file). Nothing else can be a blob name: a path, an `@file` or an unknown name makes the preset not render, and config never supplies blob names or paths. `Plan.Requires` lists the files (sorted, slash-separated); the renderers do not read the disk (pure); `internal/pc` checks them at connect and reports a missing one as `program_missing`.
- An empty desync list still gives `Plan{Engine}` (no `Requires`): nothing is checked or started.

## Planned: automatic selection (not implemented)

`desync.preset: auto` is reserved. Stage 2 runs the presets in turn against the domains that fail direct, with the engine started on the physical path and the existing direct probes measuring the effect, keeps the first or best that works and stores the choice per network; stage 3 adds a picker and an "Automatic" entry to the Settings page. Details and the constraints (admin, the Linux nft table covers the same ports for every preset) are in `PRESETS.md`, "Planned: automatic selection".

## Config

See above.

## Errors

`ErrNoExcludeIP`, `ErrUnsupported` (engine not available on this OS), `ErrVirtualUplink`, `ErrInvalidConfig`.

## Testing

Delegation to children, exclusion enforcement, empty-list behavior.

## Out of scope

Choosing a preset automatically (planned, see above), running processes.

## Bootstrap (M0)

`doc.go` only. (Implemented in M6.)
