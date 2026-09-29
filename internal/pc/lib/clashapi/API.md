# Module: clashapi

| Field | Value |
|---|---|
| Path | internal/pc/lib/clashapi |
| Kind | lib |
| Binary | – |
| Purpose | a minimal client for sing-box's Clash-compatible API |
| Consumers | internal/pc, internal/pc/monitor, internal/pc/status |
| Children | – |
| Uses libs | – |
| Build tags | – |
| Status | planned |

## Responsibility

Typed access to the few Clash API endpoints we need.

## Public API

```go
package clashapi

type Connection struct {
    ID       string
    Host     string   // metadata.host (sniffed SNI/Host)
    DstIP    string
    DstPort  string
    Network  string   // tcp | udp
    Process  string   // metadata.processPath (if available)
    Upload   int64
    Download int64
    Start    time.Time
    Chains   []string // outbound chain, last = outbound tag
    Rule     string
}

type Snapshot struct {
    At            time.Time
    UploadTotal   int64
    DownloadTotal int64
    Connections   []Connection
}

type Client interface {
    Connections(ctx context.Context) (Snapshot, error)
    Version(ctx context.Context) (string, error)
}

func New(baseURL, secret string, hc *http.Client) Client
func NewFake(snapshots ...Snapshot) Client // returns snapshots in order, then repeats the last
```

## Behavior / contract

- Uses `Authorization: Bearer <secret>`. The secret is never logged.
- JSON field mapping must be verified against the **pinned sing-box version**.

## Config

Constructor arguments.

## Errors

HTTP errors wrap status and endpoint.

## Testing

`httptest` with recorded JSON fixtures.

## Out of scope

Modifying sing-box state via the API.

## Bootstrap (M0)

`doc.go` only.
