# Module: analyzer

| Field | Value |
|---|---|
| Path | internal/lib/analyzer |
| Kind | lib |
| Binary | – |
| Purpose | probes domains from the local network and classifies whether and how they are blocked |
| Consumers | internal/blockanalyzer, internal/lib/checker, internal/lib/checker/decide, internal/lib/checker/lib/model, internal/lib/checker/netwatch, internal/pc, internal/pc/monitor, internal/router, lab/fakeasn |
| Children | internal/lib/analyzer/classify, internal/lib/analyzer/dnscheck, internal/lib/analyzer/dialer, internal/lib/analyzer/lib/verdict |
| Uses libs | internal/lib/route, internal/lib/clock, internal/lib/proc, internal/lib/faultserver (tests), internal/lib/testca (tests) |
| Build tags | – |
| Status | design ready (see DESIGN.md, AGENTS.md) |

## Responsibility

Produce **facts**: for a target domain and a route dialer, run the staged probe (resolve → TCP → TLS → verify → HTTP → bulk) and return a typed verdict with evidence. Batch runs add calibration and produce a JSON report. It never decides routes and never stores anything. The only route-related output is a labeled *suggestion*.

Current scope: **direct route only** (see AGENTS.md). The dialer is still a parameter everywhere.

## Public API

```go
package analyzer

// Re-exported from lib/verdict (children can't import this root package).
type (
    Verdict  = verdict.Verdict
    Stage    = verdict.Stage
    Blocking = verdict.Blocking
    Evidence = verdict.Evidence
)

// Re-exported from children.
type (
    Dialer   = dialer.Dialer
    Resolver = dnscheck.Resolver
    ASN      = dnscheck.ASN
)

func NewDirectDialer(opts dialer.DirectOptions) (Dialer, error)
func NewFakeDialer(script ...dialer.Step) Dialer // tests

type Target struct {
    Domain   string     // normalized via route.Normalize
    Port     uint16     // default 443
    Path     string     // default "/"
    BulkPath string     // optional large object for download-mode bulk test
    IP       netip.Addr // optional: skip resolve
}

type StageSet uint8 // bit set of TCP|TLS|HTTP|Bulk

type Options struct {
    Stages      StageSet
    Fingerprint string // "chrome" (default) | "firefox" | "safari"; "go" only in tests
    Timeouts    Timeouts
    Bulk        BulkOptions
    SNISwap     bool   // default true
    Repeats     int    // default 2; majority vote
}

type ProbeResult struct {
    Target    Target
    Route     route.Route
    IP        netip.Addr
    Verdict   Verdict
    Blocking  Blocking
    Stage     Stage
    Evidence  Evidence
    StartedAt time.Time
    Duration  time.Duration
}

type Prober interface {
    Probe(ctx context.Context, t Target, d Dialer, o Options) ProbeResult
}

type Batch struct {
    Targets []Target
    Dialers []Dialer // currently exactly one: direct
    Options Options
}

type Runner interface {
    Run(ctx context.Context, b Batch) (Report, error)
}

type Report struct {
    Schema      int // 1
    Version     string
    StartedAt   time.Time
    Network     NetworkInfo
    Calibration Calibration
    Results     []ProbeResult
    Warnings    []string
}

type NetworkInfo struct {
    PublicIP netip.Addr
    ASN      ASN
}

type Calibration struct {
    Internet   CheckState // ok | failed
    DPIPresent CheckState // yes | no | unknown
    Warnings   []string   // e.g. foreign bypass tool running
}

type Deps struct {
    Clock    clock.Clock
    Procs    proc.Lister
    Resolver Resolver // DoH; nil → built from Config
}

func NewProber(cfg Config, deps Deps) (Prober, error)
func NewRunner(cfg Config, deps Deps) (Runner, error)

// SuggestRoute maps a direct-route result to a starting route. It is a hint, not a decision.
func SuggestRoute(r ProbeResult) (route.Route, bool)

// Classify maps a connection failure seen elsewhere (e.g. the live monitor) to a verdict.
func Classify(stage Stage, err error, rxBytes, txBytes int64) Verdict

// LookupASN resolves the ASN of ip via DNS (Team Cymru) using r.
func LookupASN(ctx context.Context, r Resolver, ip netip.Addr) (ASN, error)

func EncodeReport(w io.Writer, r Report) error // JSON, schema 1
func DecodeReport(r io.Reader) (Report, error)
```

## Behavior / contract

- The staged pipeline, verdict taxonomy, bulk 16–20 KB test, SNI-swap and calibration are specified in `DESIGN.md` §4–§6. The JSON report shape is in §8.
- `Probe` never returns an error. All failures are verdicts, and our own failures are `LOCAL_ERROR`.
- Probes always resolve via DoH (never the system resolver, which may return FakeIP).
- Real runs use uTLS browser fingerprints only.
- Concurrency limits and jitter are enforced by `Runner`, not by callers.

## Config

```go
type Config struct {
    Concurrency      int           // default 8
    PerIPConcurrency int           // default 1
    Jitter           [2]time.Duration
    Options          Options       // defaults for batches
    DoH              []string      // DoH endpoints
    ControlDomains   []string      // never-blocked, for calibration
    CanaryDomains    []string      // known-blocked, for calibration
    BypassProcesses  []string      // foreign tools to warn about
    NeutralSNI       []string      // for SNI-swap
}
```

## Errors

`NewProber` / `NewRunner` fail only on invalid config. `Run` fails only on cancelled context or invalid batch. A failed calibration is reported *inside* the `Report`.

## Testing

Fault-server table tests (under `-tags lab`) for every verdict, golden JSON reports, and fake dialer/resolver/clock unit tests. See AGENTS.md milestones.

## Out of scope

Route decisions, storage, networkId, starting other programs, strategy search, and passive monitoring of live traffic.

## Bootstrap (M0)

`doc.go` only.
