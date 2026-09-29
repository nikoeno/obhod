# Module: lists

| Field | Value |
|---|---|
| Path | internal/lib/checker/lists |
| Kind | child |
| Binary | – |
| Purpose | loads and merges domain lists: candidates to check and static proxy/block entries |
| Consumers | internal/lib/checker |
| Children | – |
| Uses libs | internal/lib/route |
| Build tags | – |
| Status | planned |

## Responsibility

Reading list files and turning them into normalized domains and static route entries.

## Public API

```go
package lists

type Config struct {
    Candidates []string // files; domains the checker decides for
    Proxy      []string // files; static proxy route
    Block      []string // files; static block route
}

type Lists struct {
    Candidates []string      // normalized, deduped, sorted
    Static     []route.Entry // Source = static
    Warnings   []string      // skipped lines with file:line
}

func Load(cfg Config) (Lists, error)
func Parse(r io.Reader, name string) (domains []string, warnings []string, err error)
```

## Behavior / contract

- Format: one domain per line, `#` comments, blank lines ignored, leading `*.` / `.` stripped (suffix match is implicit).
- Invalid lines → a warning, not an error.
- Precedence: block > proxy > candidate. A domain in a static list is removed from candidates.

## Config

See above.

## Errors

Missing or unreadable files.

## Testing

Parse fixtures, precedence, dedup, warnings with line numbers.

## Out of scope

Downloading lists from URLs (maybe later), app-specific formats.

## Bootstrap (M0)

`doc.go` only.
