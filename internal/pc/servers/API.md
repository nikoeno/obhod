# Module: servers

| Field | Value |
|---|---|
| Path | internal/pc/servers |
| Kind | child |
| Binary | – |
| Purpose | parses, validates, stores and activates tunnel servers (share links and raw sing-box outbound JSON) |
| Consumers | internal/pc |
| Children | – |
| Uses libs | – |
| Build tags | – |
| Status | implemented (ADR 0004 milestone 4; contract approved by the human 2026-10-31, decisions 12-23 in `api/desktop/API.md`) |

## Responsibility

The UI must add a tunnel server without hand-editing files. This module **parses** an input into a sing-box outbound, **validates** it, **stores** it and keeps the **active** mark. It never touches the network, the running sing-box or the `tunnel` secret file by itself: `internal/pc` wires it (the dry run, the copy to the `tunnel` file, the reload) and `api/desktop` maps HTTP onto `pc`. It imports no sibling; the dry run is injected.

Engines: only `singbox` in v1. The `engine` field is reserved for `xray` (transports sing-box lacks, for example XHTTP): the Xray sidecar is lab plan L11, a new third-party binary, and **not** part of this milestone. Anything that needs it is rejected as `unsupported_protocol`.

## Public API

```go
package servers

const (
    MaxServers    = 20
    MaxInputBytes = 32 << 10
    MaxNameRunes  = 60
    EngineSingBox = "singbox"
)

// Server is the non-secret summary. It never has a key, UUID, password, the link or the outbound.
type Server struct {
    ID, Name string
    Protocol string // sing-box outbound "type": vless | vmess | trojan | shadowsocks | hysteria2 | tuic
    Host     string
    Port     int
    Engine   string // "singbox" now; "xray" later
    AddedAt  time.Time
    Active   bool
}

// Parse is pure: a share link or one raw outbound JSON object -> outbound + non-secret summary.
func Parse(input string) (Parsed, error)
type Parsed struct {
    Outbound json.RawMessage // what singbox.Outbounds.Tunnel takes; the "tag" is dropped (singbox.Generate sets it)
    Protocol, Host string
    Port     int    // for hysteria2 with only port ranges: the first range's start
    Engine   string
    Name     string // from the link's fragment (cut to 60 characters); "" when there is none
}

// Checker is the dry run: pc renders a full config with the candidate as the tunnel outbound
// (singbox.Generate) and runs singbox.Check. nil skips it. Its error text is dropped, never shown or logged here.
type Checker func(ctx context.Context, outbound json.RawMessage) error

// FS is the file system the store writes to (tests may replace it); OSFS() is the real one.
type FS interface {
    MkdirAll(dir string, perm fs.FileMode) error
    ReadFile(path string) ([]byte, error)
    WriteFileAtomic(path string, data []byte, perm fs.FileMode) error // temp file next to path, chmod, rename
    Remove(path string) error
    ReadDir(dir string) ([]string, error)
}
func OSFS() FS

type Store struct{ /* directory, index, fs; safe for concurrent use */ }
type Option func(*Store)
func WithClock(now func() time.Time) Option // stamps AddedAt; default time.Now
func New(dir string, fs FS, check Checker, newID func() string, opts ...Option) (*Store, error) // dir = Paths.Secrets/servers; newID nil = RandomID
func RandomID() string // 16 hex characters from crypto/rand, never derived from content

func (s *Store) List() ([]Server, error)                                   // oldest first, never nil
func (s *Store) Get(id string) (Server, error)
func (s *Store) Active() string                                            // "" when none
func (s *Store) Add(ctx context.Context, input, name string) (Server, error) // not activated, not even the first one
func (s *Store) Outbound(id string) (json.RawMessage, error)               // for pc only; the gateway never gets it
func (s *Store) Verify(ctx context.Context, id string) error               // the dry run again on a stored server
func (s *Store) Test(ctx context.Context, id, input string) (protocol string, err error) // parse (or load) + dry run; touches nothing
func (s *Store) SetActive(id string) error                                 // "" clears; pc copies the file (ExportTo)
func (s *Store) ExportTo(id, file string) error                            // atomic copy of the outbound to file, 0600 (the tunnel secret)
func (s *Store) Delete(id string) error                                    // deleting the active one clears the mark
func ValidName(s string) bool                                              // 1 to 60 printable characters

// Errors: test with errors.Is. Every error these functions return for bad input is an *Error whose
// Error() and UserMessage() are one fixed sentence that names the kind of problem and never a value.
var (
    ErrInvalidInput, ErrUnsupportedProtocol, ErrDuplicate, ErrLimit, ErrNotFound, ErrTooLarge error
)
```

Additions to the contract text in `api/desktop/API.md` (all additive): `Parsed.Name`, `Get`, `Active`, `Verify`, `Test`, `ExportTo`, `ValidName`, `RandomID`, `OSFS`, `WithClock` and `ErrTooLarge`.

## Behavior / contract

### Storage

- `<dir>/<id>.json`, mode 0600 (directory 0700): the outbound JSON, exactly what `singbox.Outbounds.Tunnel` takes, written with temp-file-and-rename. `<id>` is 16 random hex characters; ids from callers are validated against that shape, so an id can never be a path.
- `<dir>/index.json`, 0600, atomic: `{ "active": "<id>|''", "servers": [ { "id", "name", "protocol", "host", "port", "engine", "addedAt" } ] }`. No keys, UUIDs or passwords. It sits in the secrets directory because host and port are still sensitive.
- `New` removes leftover `*.tmp*` files of an interrupted write. A corrupt index is an error (never silently reset).
- `Add` writes the server file first, then the index; if the index cannot be written the file is removed again. `Delete` writes the index first, then removes the file.
- Activation is a **copy** (`ExportTo`), not a link (Windows needs rights for links). `internal/pc`, `singbox` and the lab keep reading the `tunnel` file unchanged. A hand-made `tunnel` file stays valid: with no index nothing changes.

### Inputs

Share links (own parsers, standard library only; no URL library, so a malformed link never produces an error text that quotes it):

| Scheme | Notes |
|---|---|
| `vless://uuid@host:port?...#name` | `security=none\|tls\|reality`, `sni`, `alpn`, `fp`, `allowInsecure`, `pbk` + `sid` + `sni` (Reality: public key and server name are required, uTLS defaults to chrome), `flow=xtls-rprx-vision`, `packetEncoding`, `type=tcp\|ws\|grpc\|http\|h2\|httpupgrade` with `host`, `path` (`?ed=N` becomes early data), `serviceName`. `encryption` other than `none` (post-quantum) is unsupported |
| `trojan://password@host:port?...` | TLS on by default; the same TLS and transport options |
| `ss://` | SIP002 (`base64(method:password)@host:port` or plain percent-encoded `method:password@...`, also AEAD-2022 keys) and the legacy `base64(method:password@host:port)`; `plugin=obfs-local;...` and `v2ray-plugin;...` only; the cipher must be one of sing-box 1.14's |
| `hy2://`, `hysteria2://` | `password@host[:port[,ranges]]`; no port means 443; ranges and `mport=` become `server_ports` (`a:b`); `obfs=salamander` + `obfs-password`; `sni`, `insecure`, `alpn`. `pinSHA256` is rejected (cannot be applied) |
| `vmess://base64(json)` | the v2rayN JSON (`add`, `port`, `id`, `aid`, `scy`, `net`, `type`, `host`, `path`, `tls`, `sni`, `alpn`, `fp`, `ps`); also the URL form `vmess://uuid@host:port?...` |
| `tuic://uuid:password@host:port?...` | `congestion_control` (cubic, new_reno, bbr), `udp_relay_mode`, `reduce_rtt`, `sni`, `alpn`, `insecure` (included in v1: it cost one small parser) |

Raw JSON: one object with a supported `type`; its `tag` is dropped. Keys that could take traffic around the app's routing are rejected at any depth (decision 23): `detour`, `bind_interface`, `inet4_bind_address`, `inet6_bind_address`, `routing_mark`, `netns`, `network_strategy`, `network_type`, `fallback_network_type`. Shape limits: at most 6 levels and 500 values.

Subscription URLs are not part of v1. The input exists only in memory, in the request and in the stored file; this module never logs.

### Validation (in this order)

1. At most 32 KiB, else `ErrTooLarge`. Empty input, several lines of links, or a string that is neither a link nor a JSON object: `ErrInvalidInput`.
2. Known protocol: scheme or `type` in vless, vmess, trojan, shadowsocks, hysteria2, tuic; a transport only Xray has (xhttp, splithttp, mKCP) or a quic transport; an unknown cipher, plugin, flow or `security`: `ErrUnsupportedProtocol`.
3. Required fields (server address, port 1-65535, UUID, password, method; Reality needs public key and server name), shape and forbidden keys: `ErrInvalidInput`.
4. Name 1 to 60 printable characters (or absent), else `ErrInvalidInput`.
5. Duplicate (same protocol, host, port and credential, compared in memory from the stored files): `ErrDuplicate`. More than 20 servers: `ErrLimit`.
6. The injected dry run: any failure becomes `ErrInvalidInput` with a fixed sentence; the checker's text is dropped. A cancelled context is returned as is.

### Names

Default name: the link's fragment (cleaned, cut to 60 characters), else `Server N` (N = count + 1, skipping names in use).

## Config

None.

## Errors

See the Public API block. No error, summary, index file or log line contains a value from the input (a leak test plants a marker in every secret field).

## Testing

- Golden fixtures in `testdata/*.in` with `*.golden.json` per protocol (VLESS plain, TLS+WS, Reality, gRPC, Trojan, Shadowsocks in four forms, Hysteria2 with obfs and with ports, VMess, TUIC, raw JSON), fake credentials only (`example.com`, documentation addresses, the all-zero UUID). Updated only with `-update`.
- A negative table: missing or bad port, empty credential, unknown scheme, bad base64, oversized input, JSON with `detour` or a bind key, two objects, Xray-only transports.
- Store tests: modes 0600/0700 (skipped on Windows), atomic writes (a failing FS leaves nothing), leftover temp files, copy to the tunnel file, delete rules, limit, duplicate, ids that are not paths.
- A secret-leak test: a marker in every secret field of every protocol; no error, `UserMessage`, summary or `index.json` may contain it.
- The round trip of every fixture through `singbox.Generate` (and, with the `integration` tag, the pinned `sing-box check`) lives in `internal/pc`, which may import both modules.

## Out of scope

Subscription URLs, the Xray sidecar, groups, failover, latency tests, the `proxy` outbound, the network.

## Known limits (v1, fixed 2026-10-31)

Decisions and limits are kept in `api/desktop/API.md` ("Server management decisions", items 12-26, and "Known limits"). In short: share links and raw sing-box JSON only; Xray-only transports, subscriptions and the `proxy` outbound are not supported; hy2 `pinSHA256` is rejected; one active server; 20 servers, 32 KiB input, 60-character names; the dry run skips `sing-box check` when the binary is missing; hostname servers are re-resolved at the next connect.
