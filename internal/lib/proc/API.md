# Module: proc

| Field | Value |
|---|---|
| Path | internal/lib/proc |
| Kind | lib |
| Binary | – |
| Purpose | lists and finds running processes per OS |
| Consumers | internal/lib/analyzer, internal/pc/procman |
| Children | – |
| Uses libs | – |
| Build tags | – |
| Status | implemented |

## Responsibility

Read-only process discovery: detecting foreign bypass tools that would distort probes, and finding leftover children after a crash.

## Public API

```go
package proc

type Process struct {
    PID  int
    PPID int
    Name string // executable base name, lowercased, without .exe
    Path string // may be "" if not readable
}

type Lister interface {
    List(ctx context.Context) ([]Process, error)
}

func System() Lister

// Find returns processes whose Name matches any of names (case-insensitive).
func Find(ctx context.Context, l Lister, names ...string) ([]Process, error)

// NewFake returns a Lister with a fixed process list (tests).
func NewFake(ps ...Process) Lister
```

## Behavior / contract

Windows: Toolhelp32 snapshot. Linux: `/proc/*/stat` + `/proc/*/exe`. Unreadable entries are skipped, not errors.

## Config

None.

## Errors

Only when the listing itself fails.

## Testing

Linux `/proc` parsing from fixtures; `Find` matching; fake.

## Out of scope

Killing or controlling processes (that's procman).

## Bootstrap (M0)

`doc.go` only.
