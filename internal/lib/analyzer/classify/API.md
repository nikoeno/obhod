# Module: classify

| Field | Value |
|---|---|
| Path | internal/lib/analyzer/classify |
| Kind | child |
| Binary | – |
| Purpose | maps a failed probe stage and error to a verdict (pure functions) |
| Consumers | internal/lib/analyzer |
| Children | – |
| Uses libs | internal/lib/analyzer/lib/verdict |
| Build tags | – |
| Status | implemented |

## Responsibility

All knowledge of "what this error at this stage means". No I/O, no network.

## Public API

```go
package classify

type Input struct {
    Stage      verdict.Stage
    Err        error         // nil if the stage succeeded
    RxBytes    int64
    TxBytes    int64
    Elapsed    time.Duration
    Timeout    time.Duration // the deadline that applied
    HTTPStatus int
    Location   string
    CertErr    error
    StubHosts  []string      // known ISP stub hosts/IPs (from calibration)
    StallWin   [2]int64      // bulk window in bytes, default 12–36 KB
}

// Classify returns the verdict for a single probe attempt.
func Classify(in Input) verdict.Verdict

// Majority aggregates repeated attempts: most frequent non-LOCAL verdict wins;
// ties prefer the more "blocked" verdict; all LOCAL → LOCAL_ERROR.
func Majority(vs []verdict.Verdict) verdict.Verdict

// Confirm upgrades Blocking using differential evidence (SNI-swap OK → confirmed SNI block).
func Confirm(v verdict.Verdict, swap *verdict.SNISwapResult) verdict.Blocking
```

## Behavior / contract

The rules from DESIGN.md §5, including:

- RST/abort at `tls` → `TLS_RST`, even if the OS reports EOF;
- `wrong version number` / `record overflow` / `decode error` → `TLS_SPOOF`;
- a timeout at `tcp` vs at `tls` is kept distinct;
- `*verdict.LocalErr` anywhere in the chain → `LOCAL_ERROR`;
- a stall inside `StallWin` at `bulk` → `STALL_16_20`, outside it → `READ_TIMEOUT`.

Errors are matched with `errors.Is/As` and `syscall` errnos (`ECONNRESET`, `WSAECONNRESET`…) first, and message substrings only as a last resort.

## Config

None (inputs carry everything).

## Errors

None.

## Testing

Large table test of error fixtures per OS (Linux errno and Windows WSA codes), `Majority` ties, and `Confirm`.

## Out of scope

Producing the errors (that's the analyzer root pipeline).

## Bootstrap (M0)

Done (M0-M8).
