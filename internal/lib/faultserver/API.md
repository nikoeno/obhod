# Module: faultserver

| Field | Value |
|---|---|
| Path | internal/lib/faultserver |
| Kind | test-only |
| Binary | – |
| Purpose | runs localhost servers that misbehave in specific, DPI-like ways |
| Consumers | internal/lib/analyzer, lab |
| Children | – |
| Uses libs | internal/lib/testca (tests) |
| Build tags | lab |
| Status | implemented (M3) |

## Responsibility

Deterministic reproductions of every blocking behavior the analyzer must classify, so classification is tested without a real censor.

## Public API

```go
//go:build lab

package faultserver

type Behavior string

const (
    OK              Behavior = "ok"                // valid TLS + HTTP/1.1, serves N bytes
    RSTOnSYN        Behavior = "rst-on-syn"        // closed port (refused)
    BlackholeSYN    Behavior = "blackhole-syn"     // accept queue never served → client connect timeout (best effort on localhost)
    RSTAfterHello   Behavior = "rst-after-hello"   // read ClientHello, then RST
    DropHello       Behavior = "drop-hello"        // read ClientHello, never answer
    TLSAlert        Behavior = "tls-alert"         // send a TLS alert record
    Garbage         Behavior = "garbage"           // send non-TLS bytes after hello
    WrongCert       Behavior = "wrong-cert"        // cert for another host
    SelfSignedCert  Behavior = "self-signed"
    StallAfter      Behavior = "stall-after"       // stop moving data after N bytes (either direction)
    RSTAfterRequest Behavior = "rst-after-request" // RST after the first HTTP request
    HTTPStub        Behavior = "http-stub"         // plain HTTP 302 to a stub URL
    ConnLimit       Behavior = "conn-limit"        // reset handshakes after N concurrent conns
    SNIBlock        Behavior = "sni-block"         // valid TLS server that RSTs ClientHellos whose SNI is BlockSNI
)

type Spec struct {
    Behavior   Behavior
    Bytes      int    // OK: response size; StallAfter: byte count
    Direction  string // StallAfter: "rx" (server stops reading) | "tx" (server stops writing)
    AlertCode  uint8  // TLSAlert
    StubURL    string // HTTPStub
    Limit      int    // ConnLimit
    BlockSNI   string // SNIBlock: the server name that gets a RST
    RejectUnknownSNI bool // SNIBlock: names outside Hosts get a TLS alert (like a CDN edge)
    Hosts      []string
}

type Server struct {
    Addr netip.AddrPort
    Pool *x509.CertPool // trust this in the client under test
}

// Certs is what a server needs to present certificates. testca.CA implements it; it is an interface
// here because a test-only module may not import another test-only module from non-test code.
type Certs interface {
    RootPool() *x509.CertPool
    Issue(hosts ...string) (tls.Certificate, error)
    IssueWrongHost(hosts ...string) (tls.Certificate, error)
    IssueSelfSigned(hosts ...string) (tls.Certificate, error)
}

// Start runs a server bound to 127.0.0.1:0; closed via t.Cleanup.
func Start(t testing.TB, s Spec, ca Certs) *Server

// Run is the non-testing form for the lab. It stops when ctx is done or Close is called.
func Run(ctx context.Context, listen string, s Spec, ca Certs) (*Server, error)

// Close stops the server and drops its connections (idempotent).
func (s *Server) Close()
```

## Behavior / contract

- RST is produced via `SetLinger(0)` + close.
- Every behavior is deterministic and has a self-test.
- It binds to loopback only unless `Run` is given another address (lab namespaces).

## Config

None.

## Errors

Listen failures only.

## Testing

For each behavior, a raw client asserts the wire-level effect (RST seen, bytes-before-stall, alert code…).

## Out of scope

Classifying anything (that's the analyzer). Real DPI emulation on packets (that's `lab/fakedpi`).

## Bootstrap (M0)

`doc.go` only (with `//go:build lab`).
