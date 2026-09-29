# Module: verdict

| Field | Value |
|---|---|
| Path | internal/lib/analyzer/lib/verdict |
| Kind | lib |
| Binary | – |
| Purpose | defines the verdict, stage, blocking and evidence types shared inside the analyzer |
| Consumers | internal/lib/analyzer, internal/lib/analyzer/classify, internal/lib/analyzer/dialer, internal/lib/analyzer/dnscheck |
| Children | – |
| Uses libs | – |
| Build tags | – |
| Status | implemented |

## Responsibility

The frozen vocabulary of probe outcomes. Outside the analyzer, these types are only reached through the analyzer root's re-exports.

## Public API

```go
package verdict

type Stage string

const (
    StageResolve Stage = "resolve"
    StageTCP     Stage = "tcp"
    StageTLS     Stage = "tls"
    StageVerify  Stage = "verify"
    StageHTTP    Stage = "http"
    StageBulk    Stage = "bulk"
    StageBurst   Stage = "burst" // deferred
)

type Verdict string

const (
    OK          Verdict = "OK"
    OKRedirect  Verdict = "OK_REDIRECT"
    DNSPoisoned Verdict = "DNS_POISONED"
    DNSFail     Verdict = "DNS_FAIL"
    TCPTimeout  Verdict = "TCP_TIMEOUT"
    TCPRefused  Verdict = "TCP_REFUSED"
    TLSRST      Verdict = "TLS_RST"
    TLSTimeout  Verdict = "TLS_TIMEOUT"
    TLSAlert    Verdict = "TLS_ALERT"
    TLSSpoof    Verdict = "TLS_SPOOF"
    TLSMITM     Verdict = "TLS_MITM"
    TLSEOF      Verdict = "TLS_EOF"
    HTTPStub    Verdict = "HTTP_STUB"
    HTTPRST     Verdict = "HTTP_RST"
    Stall1620   Verdict = "STALL_16_20"
    ReadTimeout Verdict = "READ_TIMEOUT"
    ConnLimited Verdict = "CONN_LIMITED"
    LocalError  Verdict = "LOCAL_ERROR"
    Unknown     Verdict = "UNKNOWN"
)

func (v Verdict) Valid() bool
func (v Verdict) DefaultBlocking() Blocking // mapping from DESIGN.md §5

type Blocking string

const (
    BlockingNone      Blocking = "none"
    BlockingLikely    Blocking = "likely"
    BlockingConfirmed Blocking = "confirmed"
    BlockingLocal     Blocking = "local_error"
    BlockingUnknown   Blocking = "unknown"
)

type Evidence struct {
    Err         string        // raw error chain, never empty on failure
    TLSVersion  string
    CertSubject string
    CertError   string
    HTTPStatus  int
    Location    string        // redirect target
    RxBytes     int64
    TxBytes     int64
    RTT         time.Duration
    StageTimes  map[Stage]time.Duration
    SNISwap     *SNISwapResult
}

type SNISwapResult struct {
    SNI     string
    Verdict Verdict
}

// LocalErr marks failures caused by our own side (dialer misconfig, no uplink…).
type LocalErr struct{ Err error }

func (e *LocalErr) Error() string
func (e *LocalErr) Unwrap() error
func Local(err error) error // wraps err as *LocalErr
```

## Behavior / contract

The `Verdict` and `Blocking` string values appear in JSON reports and are **frozen**. Adding a value means asking first and bumping the report schema.

## Config

None.

## Errors

None.

## Testing

`Valid`, `DefaultBlocking` for every constant, JSON round-trip.

## Out of scope

Classification logic (that's classify).

## Bootstrap (M0)

Done (M0-M8). `Evidence` and `SNISwapResult` carry the JSON tags of the report schema; durations are nanoseconds.
