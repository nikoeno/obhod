# Module: singbox

| Field | Value |
|---|---|
| Path | internal/pc/singbox |
| Kind | child |
| Binary | – |
| Purpose | generates and validates the sing-box config from a RouteTable |
| Consumers | internal/pc |
| Children | – |
| Uses libs | internal/lib/route |
| Build tags | – |
| Status | planned |

## Responsibility

The routing layer's config: TUN inbound, sniffing, DNS, outbounds per route, and rules per domain. Deterministic output.

## Public API

```go
package singbox

type Config struct {
    Binary          string          // path to sing-box
    TUN             TUNConfig       // interface name, address, auto_route, strict_route, stack
    DNS             DNSConfig       // DoH servers, FakeIP range
    ClashAPI        string          // listen, default "127.0.0.1:9090"
    ClashSecret     string          // filled from secrets by the app
    OwnProcesses    []string        // routed direct (our binaries + desync engine)
    BlockQUIC       bool            // default true (force TCP fallback)
    LogLevel        string
}

type Outbounds struct {
    Tunnel json.RawMessage // VLESS+Reality outbound, from secrets
    Proxy  json.RawMessage // optional SOCKS/HTTP outbound
}

// Generate returns the full sing-box JSON config.
func Generate(cfg Config, ob Outbounds, t route.Table) ([]byte, error)

// Check runs "<binary> check -c <file>" and returns its output on failure.
func Check(ctx context.Context, binary, file string) error

// Command returns how to run sing-box with the given config file.
func Command(cfg Config, file string) (path string, args []string)
```

## Behavior / contract

Route mapping:

| Route | Outbound |
|---|---|
| direct, desync | `direct` (desync domains are mangled by the desync engine on the physical adapter) |
| tunnel | `tunnel` (VLESS+Reality) |
| proxy | `proxy` |
| block | reject |

- Rule order: own processes → direct; QUIC block; block; proxy; tunnel; desync/direct; final → the configured default (tunnel for unknown).
- Output is byte-identical for equal input (sorted keys and lists) → golden tests work.
- Field names and options must be verified against the **pinned sing-box version** docs, and every generated config must pass `sing-box check` in CI when the binary is available.

## Config

See above.

## Errors

`Generate`: missing tunnel outbound, invalid table. `Check`: the checker's stderr.

## Testing

Golden files per table fixture; `sing-box check` in an integration job.

## Out of scope

Running the process (procman), the desync engine.

## Bootstrap (M0)

`doc.go` only.
