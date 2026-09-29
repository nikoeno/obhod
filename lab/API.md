# Module: lab

| Field | Value |
|---|---|
| Path | lab |
| Kind | lab |
| Binary | – |
| Purpose | runs end-to-end scenarios in Linux network namespaces with a fake TSPU |
| Consumers | – (go test only) |
| Children | lab/netns, lab/fakedpi, lab/fakeasn |
| Uses libs | internal/lib/faultserver, internal/lib/testca |
| Build tags | linux |
| Status | planned |

## Responsibility

Level 3 testing: a fake internet with two "ISPs", a fake TSPU in the path, and our binaries as the client, driven by Go tests. It needs root, so it runs in CI (`sudo`) or a disposable VM, never on a dev machine unattended.

## Public API

```go
//go:build linux

package lab

type Profile struct {        // loaded from lab/profiles/*.yaml
    Name  string
    ASN   uint32
    Rules []fakedpi.Rule
}

type Lab struct{ /* ... */ }

// Up builds namespaces: client, isp-a, isp-b, internet (fault servers), asn service.
// Teardown is registered with t.Cleanup and always runs.
func Up(t *testing.T, profiles ...Profile) *Lab

func (l *Lab) UseISP(name string)                 // switch client uplink (network change)
func (l *Lab) SetRules(isp string, r ...fakedpi.Rule) // simulate a TSPU update
func (l *Lab) Server(name string) *faultserver.Server
func (l *Lab) Exec(ctx context.Context, argv ...string) (stdout, stderr []byte, err error) // in the client ns
func (l *Lab) Binary(name string) string          // path to a freshly built binary
```

## Behavior / contract

- Scenarios live as `*_test.go` in this package, e.g.:
  - blockanalyzer classifies each rule correctly;
  - checker network switch;
  - a rule change triggers a re-decision;
  - nfqws desync defeats the naive-SNI rule but not the reassembling rule.
- Tests skip (not fail) when not root or not Linux.
- No changes outside the namespaces it creates. Namespaces use a unique prefix per run and are removed in teardown.

## Config

Profiles in `lab/profiles/*.yaml`.

## Errors

Setup failures fail the test with the failing command.

## Testing

It *is* the tests.

## Out of scope

Windows (windows-smoke workflow), real TSPU behavior.

## Bootstrap (M0)

`doc.go` only (with `//go:build linux`). Also create `lab/profiles/.gitkeep`.
