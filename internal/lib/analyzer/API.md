# Module: analyzer

| Field | Value |
|---|---|
| Path | internal/lib/analyzer |
| Kind | lib |
| Binary | – |
| Purpose | probes domains from the local network and classifies whether and how they are blocked |
| Consumers | internal/blockanalyzer, internal/lib/checker, internal/lib/checker/decide, internal/lib/checker/lib/model, internal/lib/checker/netwatch, internal/pc, internal/pc/monitor, internal/router, lab/fakeasn |
| Children | internal/lib/analyzer/classify, internal/lib/analyzer/dnscheck, internal/lib/analyzer/dialer, internal/lib/analyzer/lib/verdict |
| Uses libs | internal/lib/route, internal/lib/clock, internal/lib/proc, internal/lib/version, internal/lib/faultserver (tests), internal/lib/testca (tests) |
| Build tags | – |
| Status | implemented (M1-M8; see DESIGN.md, AGENTS.md) |

## Responsibility

Produce **facts**: for a target domain and a route dialer, run the staged probe (resolve → TCP → TLS → verify → HTTP → bulk) and return a typed verdict with evidence. Batch runs add calibration and produce a JSON report. It never decides routes and never stores anything. The only route-related output is a labeled *suggestion*.

Current scope: **direct route only** (see AGENTS.md). The dialer is still a parameter everywhere.

## Public API

```go
package analyzer

// Re-exported from lib/verdict (children can't import this root package).
type (
    Verdict       = verdict.Verdict
    Stage         = verdict.Stage
    Blocking      = verdict.Blocking
    Evidence      = verdict.Evidence
    SNISwapResult = verdict.SNISwapResult
)

// Re-exported from children.
type (
    Dialer        = dialer.Dialer
    DirectOptions = dialer.DirectOptions
    DialerStep    = dialer.Step
    Resolver      = dnscheck.Resolver
    ASN           = dnscheck.ASN
)

func NewDirectDialer(opts DirectOptions) (Dialer, error)
func NewFakeDialer(script ...DialerStep) Dialer // tests; route direct
func NewSystemResolver() Resolver               // OS resolver, for Deps.System
func SystemProcs() proc.Lister                  // OS process list, for Deps.Procs

type Target struct {
    Domain   string     // normalized via route.Normalize
    Port     uint16     // default 443
    Path     string     // default "/"
    BulkPath string     // optional large object for download-mode bulk test
    IP       netip.Addr // optional: skip resolve
}

func (t Target) Normalized() (Target, error) // normalizes Domain, applies defaults

type StageSet uint8 // bit set of StageTCP|StageTLS|StageHTTP|StageBulk (StageAll)

func ParseStageSet(s string) (StageSet, error) // "tcp,tls"; a later stage implies the earlier ones
func (s StageSet) Has(o StageSet) bool
func (s StageSet) String() string

type Options struct {
    Stages      StageSet
    Fingerprint string      // "chrome" (default) | "firefox" | "safari"; "go" only in tests
    Timeouts    Timeouts    // Resolve, TCP, TLS, HTTP, Bulk
    Bulk        BulkOptions // Mode "download"|"upload", Size, StallMin, StallMax (DESIGN §4.1)
    SNISwap     bool        // default true
    Repeats     int         // default 2; majority vote
}

type ProbeResult struct {
    Target         Target
    Route          route.Route
    IP             netip.Addr
    Verdict        Verdict
    Blocking       Blocking
    Stage          Stage
    Evidence       Evidence
    Suggestion     route.Route // filled by the Runner; "" = none
    SuggestionNote string
    StartedAt      time.Time
    Duration       time.Duration
}

type Prober interface {
    Probe(ctx context.Context, t Target, d Dialer, o Options) ProbeResult
}

type Batch struct {
    Targets []Target
    Dialers []Dialer // currently exactly one: direct
    Options Options  // zero value → Config.Options
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
    PublicIP netip.Addr // only when Config.PublicIPEndpoint is set
    ASN      ASN
}

type CheckState string // ok | failed | yes | no | unknown

type Calibration struct {
    Internet   CheckState // ok | failed | unknown (no control domains)
    DPIPresent CheckState // yes | no | unknown
    Warnings   []string   // e.g. foreign bypass tool running
}

type Deps struct {
    Clock    clock.Clock // nil → real
    Resolver Resolver    // DoH; nil → built from Config.DoH
    System   Resolver    // OS resolver for the poisoning comparison; nil → no comparison
    Procs    proc.Lister // for the bypass-tool warning; nil → no scan
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
func DecodeReport(r io.Reader) (Report, error) // rejects other schemas and unknown verdict/blocking values

func DefaultConfig() Config
func ParseConfig(r io.Reader) (Config, error) // YAML over DefaultConfig; unknown keys rejected; validated
func (c *Config) Validate() error             // also normalizes the domain lists
```

## Behavior / contract

- The staged pipeline, verdict taxonomy, bulk 16–20 KB test, SNI-swap and calibration are specified in `DESIGN.md` §4–§7. The JSON report shape is in §8.
- `Probe` never returns an error. All failures are verdicts, and our own failures are `LOCAL_ERROR`.
- Probes always resolve via DoH (never the system resolver, which may return FakeIP). The system resolver is used only for the poisoning comparison (`Deps.System`); a poisoned answer stops the probe at `resolve` with `DNS_POISONED`.
- Real runs use uTLS browser fingerprints only. The handshake offers ALPN `http/1.1` only, because the probe speaks HTTP/1.1.
- Certificate verification is deferred until after the handshake so a spoofed certificate is observed (`TLS_MITM` at `verify`).
- Concurrency limits (`Concurrency`, `PerIPConcurrency`) and jitter are enforced by `Runner`, not by callers.
- `Run` calibrates first (control and canary domains, bypass-tool scan, optional public IP/ASN), then probes. `Results` keep the order of `Batch.Targets`. Suggestions are left empty when calibration failed.
- A probe whose attempts disagree (some succeed, some are reset or refused) is `CONN_LIMITED`.
- The report and `Options` use nanoseconds for durations.

## Config

```go
type Config struct {
    Concurrency      int              // default 8
    PerIPConcurrency int              // default 1
    Jitter           [2]time.Duration // [min, max] pause before each probe
    Options          Options          // defaults for batches
    DoH              []string         // DoH endpoints (https URLs; IP-based recommended)
    ControlDomains   []string         // never-blocked, for calibration
    CanaryDomains    []string         // known-blocked, for calibration
    BypassProcesses  []string         // foreign tools to warn about
    NeutralSNI       []string         // for SNI-swap
    PublicIPEndpoint string           // https URL answering with the caller's IP; empty (default) = no lookup
}
```

YAML keys are the snake_case field names (`per_ip_concurrency`, `control_domains`, `public_ip_endpoint`, …); `stages` is a comma list.

## Errors

`NewProber` / `NewRunner` fail only on invalid config. `Run` fails only on cancelled context or invalid batch (no dialer, more than one dialer, bad domain, bad options); on cancellation it returns the partial report with the error. A failed calibration is reported *inside* the `Report`.

## Testing

Unit tests with fake dialer/resolver/clock/process list (no network). Fault-server table tests (under `-tags lab`) for every verdict that can be produced on localhost: TCP refused, TLS RST/timeout/alert/spoof, MITM (wrong and self-signed cert), HTTP stub, HTTP RST, bulk stalls at 16 KB and 50 KB, SNI block with swap, connection limit. `TCP_TIMEOUT` and `DNS_*` need no server (fake dialer/resolver). Golden JSON report in `testdata/`, updated only with `-update`.

## Out of scope

Route decisions, storage, networkId, starting other programs, strategy search, and passive monitoring of live traffic.

## Bootstrap

Done: M0–M8 (see AGENTS.md).
