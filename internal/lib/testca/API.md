# Module: testca

| Field | Value |
|---|---|
| Path | internal/lib/testca |
| Kind | test-only |
| Binary | – |
| Purpose | creates an in-memory test certificate authority for lab and fault-server tests |
| Consumers | internal/lib/analyzer, internal/lib/faultserver, lab |
| Children | – |
| Uses libs | – |
| Build tags | lab |
| Status | implemented (M3) |

## Responsibility

Certificates for localhost TLS test servers, **in memory only**. Never written to a system trust store and never used by production builds (enforced by the `lab` build tag).

## Public API

```go
//go:build lab

package testca

type CA struct {
    Pool *x509.CertPool // pass to clients under test
    Cert *x509.Certificate
}

func New() (*CA, error)

func (ca *CA) Issue(hosts ...string) (tls.Certificate, error)        // valid leaf
func (ca *CA) IssueExpired(hosts ...string) (tls.Certificate, error)
func (ca *CA) IssueWrongHost(hosts ...string) (tls.Certificate, error) // SAN doesn't match
func SelfSigned(hosts ...string) (tls.Certificate, error)              // not signed by any CA

// Satisfy faultserver.Certs.
func (ca *CA) RootPool() *x509.CertPool
func (ca *CA) IssueSelfSigned(hosts ...string) (tls.Certificate, error)
```

## Behavior / contract

ECDSA P-256 keys, short validity (1 day), deterministic serials per CA instance.

## Config

None.

## Errors

Key generation failures only.

## Testing

Verify each variant with `x509.Verify` against `Pool`.

## Out of scope

Persisting keys, anything touching OS trust stores.

## Bootstrap (M0)

`doc.go` only (with `//go:build lab`).
