# Module: version

| Field | Value |
|---|---|
| Path | internal/lib/version |
| Kind | lib |
| Binary | – |
| Purpose | holds build version information injected at link time |
| Consumers | internal/blockanalyzer, internal/lib/analyzer, internal/pc, internal/router |
| Children | – |
| Uses libs | – |
| Build tags | – |
| Status | implemented |

## Responsibility

One place for the version string that every binary prints and records in reports.

## Public API

```go
package version

// Set via: -ldflags "-X <module>/internal/lib/version.Version=v0.1.0 -X ...Commit=abc123"
var (
    Version = "dev"
    Commit  = ""
)

// String returns "<binary> <Version>" plus " (<Commit>)" if Commit is set.
func String(binary string) string
```

## Behavior / contract

These are the only exported variables in the repo that are allowed to be mutable, and only the linker sets them.

## Config

None.

## Errors

None.

## Testing

`String` with and without `Commit`.

## Out of scope

Update checks and release channels.

## Bootstrap (M0)

Implement fully: the variables, `String`, and a test asserting `Version` is non-empty and `String("x") == "x dev"` by default.
