# Module: sites

| Field | Value |
|---|---|
| Path | internal/pc/sites |
| Kind | child |
| Binary | – |
| Purpose | stores the user's per-site routing rules (domain + action) in a small private JSON file |
| Consumers | internal/pc |
| Children | – |
| Uses libs | internal/lib/route |
| Build tags | – |
| Status | implemented (user-approved 2026-11-01, Go side; the UI follows separately) |

## Responsibility

Per-domain overrides chosen by the user ("always use this path for example.com"). This module validates, normalizes and **persists** the rules. It does not touch the checker, the route table or any process: `internal/pc` loads the rules at start and hands them to `checker.SetOverrides` after every change, and `api/desktop` maps HTTP onto `pc`. The starter list files are never read or written here.

## Public API

```go
package sites

const (
    MaxRules    = 500
    FileName    = "sites.json"         // inside the directory given to Open (Paths.Data)
    CorruptName = "sites.json.corrupt" // where an unreadable file is kept
)

type Action string // Direct "direct" | Desync "desync" | Tunnel "tunnel" | Block "block"
func Actions() []Action
func (a Action) Route() route.Route // the route the action forces (same name)

type Rule struct {
    Domain  string    // normalized; covers the domain and its subdomains
    Action  Action
    AddedAt time.Time // first added; an update keeps it
}

// NormalizeDomain drops a leading "*." or ".", then route.Normalize (lower case, trailing dot, punycode;
// rejects schemes, paths, ports, IP addresses, inner wildcards). Error: ErrInvalidDomain, never with the input.
func NormalizeDomain(s string) (string, error)
// ParseAction: the four actions, case-insensitive. "none", "proxy", "" and anything else: ErrInvalidAction.
func ParseAction(s string) (Action, error)

type FS interface { /* MkdirAll, ReadFile, WriteFileAtomic, Rename */ } // tests may replace it
func OSFS() FS

type Store struct{ /* safe for concurrent use */ }
type Option func(*Store)
func WithClock(now func() time.Time) Option // stamps AddedAt; default time.Now
func Open(dir string, fs FS, opts ...Option) (*Store, error)

func (s *Store) List() []Rule                                  // sorted by domain, never nil
func (s *Store) Entries() []route.Entry                        // Source = route.SourceUser, sorted by domain
func (s *Store) Set(domain, action string) (r Rule, created bool, err error) // upsert
func (s *Store) Delete(domain string) error
func (s *Store) Recovered() bool // Open moved an unreadable file aside

var ErrInvalidDomain, ErrInvalidAction, ErrLimit, ErrNotFound error
```

## Behavior / contract

- File: `<dir>/sites.json`, mode 0600, written with a temporary file in the same directory, sync, chmod, rename (atomic). `{ "version": 1, "rules": [ { "domain", "action", "addedAt" } ] }`, rules sorted by domain. The directory is created with 0700 if missing (`internal/pc` passes `Paths.Data`, which `paths.Ensure` already made).
- `Open`: a missing file is an empty store. A file that is not valid (bad JSON, unknown field, version other than 1, a domain that is not in normalized form, an action outside the four, a duplicate, more than 500 rules) is **moved** to `sites.json.corrupt` (an older one is replaced) and the store starts empty; `Recovered()` is true so the caller can log it. Rules are never silently dropped one by one.
- `Set` is an upsert: a new domain adds a rule (`created` true), an existing one changes its action and keeps `AddedAt`. A new rule when 500 exist is `ErrLimit`; an update at the limit works. The write happens before the in-memory change: on any error (write failure included) nothing changes.
- `Delete` of a domain without a rule is `ErrNotFound`; a malformed domain is `ErrInvalidDomain`.
- A rule covers the domain and every subdomain; one rule per normalized domain.
- Errors hold no value of the input (no domain, no path).

## Config

None.

## Errors

See the `Err*` values above; write errors are wrapped (`sites: save: ...`) and are not user sentences.

## Testing

Round trip through a real temporary directory, upsert and `AddedAt`, delete, normalization table, action table, the 500 limit, private file mode and no leftover temporary file, a failing write leaves memory unchanged, corrupt-file recovery for seven kinds of bad content. No test touches a real data directory.

## Out of scope

The route table, precedence against the lists (checker and `route.NewTable`), live apply (reload path in `internal/pc`), HTTP.

## Bootstrap

Done.
