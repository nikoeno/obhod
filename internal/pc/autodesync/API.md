# Module: autodesync

| Field | Value |
|---|---|
| Path | internal/pc/autodesync |
| Kind | child |
| Binary | – |
| Purpose | searches the desync presets for the one that fixes the blocked sites of a network, and remembers the winner per network |
| Consumers | internal/pc |
| Children | – |
| Uses libs | internal/lib/clock |
| Build tags | – |
| Status | implemented (2026-11-01, stage 2 of 3 of the preset catalogue) |

## Responsibility

The loop of "try a preset, wait, probe, score, next", with its budget, and the small private file that keeps the result. It knows nothing about engines, tables or the catalogue: `internal/pc` implements the two interfaces it needs (`Applier`: run the desync engine with this preset for exactly these sites; `Prober`: probe these sites through the real direct path) and passes the preset ids in the order to try. It never starts a process and never touches the network itself.

## Public API

```go
package autodesync

const MaxConcurrency = 2 // the most probes at once; Config.Validate refuses more

type Config struct { // YAML keys are the snake_case names; zero fields take the defaults
    MaxPresets   int              // presets one search applies at most; skipped ones do not count. Default 6
    Settle       time.Duration    // wait after the engine restarted, before the first probe. Default 2s
    Pause        [2]time.Duration // [min, max] random pause between two presets. Default [1s, 3s]
    Budget       time.Duration    // time cap of a search, checked between presets. Default 3m
    MaxDomains   int              // sites probed per search. Default 8
    ProbeTimeout time.Duration    // cap of every stage of a probe. Default 8s
    Concurrency  int              // probes at once, 1 or 2. Default 2
    Backoff      time.Duration    // wait before an automatic search after an inconclusive one or a live-failure one. Default 10m
    FailedTTL    time.Duration    // a site no preset fixed stays failed this long. Default 24h
}
func DefaultConfig() Config
func (c Config) WithDefaults() Config
func (c Config) Validate() error

var ErrSkip, ErrNoInternet error // returned by Applier / Prober, see below

type Applier interface { Apply(ctx context.Context, preset string, domains []string) error }
type Prober interface  { Probe(ctx context.Context, domains []string) (reached map[string]bool, err error) }

type Request struct {
    Domains  []string        // cut to Config.MaxDomains, duplicates removed
    Order    []string        // preset ids, best candidate first
    Progress func(Progress)  // optional; carries no domain
}
type Progress struct { Phase Phase /* start | trying */; Index, Total int; Preset string }

type Result struct {
    Outcome Outcome   // chosen | none | inconclusive | canceled | error
    Chosen  string    // the preset that fixed the most sites ("" unless chosen)
    Fixed, Failed []string
    Tried   []Attempt // every preset in order: Fixed/Total counts, Skipped, Unusable
    Domains []string  // what was probed
    Stopped string    // all fixed | budget | presets | exhausted | canceled | error
    Err     error
}

type Searcher struct{ /* ... */ }
func New(cfg Config, clk clock.Clock, a Applier, p Prober, opts ...Option) *Searcher
func WithJitter(f func(lo, hi time.Duration) time.Duration) Option // tests
func (s *Searcher) Run(ctx context.Context, req Request) Result

// The store: Paths.Data/desync.json, 0600, atomic, at most 32 networks.
type Record struct { Preset string; At time.Time; Fixed []string; Failed map[string]time.Time }
type FS interface { /* MkdirAll, ReadFile, WriteFileAtomic, Rename */ }
func OSFS() FS
type Store struct{ /* safe for concurrent use */ }
func OpenStore(dir string, fs FS) (*Store, error)
func (s *Store) Get(netID string) (Record, bool)
func (s *Store) Put(netID string, r Record) error
func (s *Store) Selected() string            // the user's choice through the API ("auto" or a preset id); "" = follow app.yaml
func (s *Store) SetSelected(v string) error
func (s *Store) Recovered() bool
const FileName = "desync.json"; const CorruptName = "desync.json.corrupt"
```

## Behavior / contract

- `Run` takes the presets of `Request.Order` one at a time. For each: wait out the pause (not before the first) and check the budget; `Applier.Apply(preset, domains)`; sleep `Settle`; `Prober.Probe(domains)`; score = number of sites reached.
- It stops at the first preset that reaches **every** site (`Stopped` "all fixed"), after `MaxPresets` applied presets ("presets"), when `Budget` has passed between two presets ("budget"), or when the order ends ("exhausted"). The preset in progress is never cut short by the budget; its probes have their own timeouts. All waits use the injected clock and honour `ctx`.
- The winner is the preset that reached the most sites; on a tie the earlier one. `Failed` is the probed sites it does not reach. If no preset reached any site and at least one measurement was usable, the outcome is `none` and every site is in `Failed`. If no measurement was usable (the probes' internet check failed, or the probe returned an error: `ErrNoInternet` is the usual one), the outcome is `inconclusive` and nothing is marked failed.
- `Applier.Apply` returning `ErrSkip` (the preset's payload file is missing) skips that preset without counting it. Any other error ends the search with outcome `error`; a cancelled context ends it with `canceled`. `Run` never applies the winner and never restores anything: the caller does, because it owns the engine.
- The store validates a file on open (version 1, no unknown field, bounded sizes); an unreadable file is moved to `desync.json.corrupt` and the store starts empty. A record holds the opaque network id (a salted hash such as `asn:64500`, never the public IP), a catalogue id and the names of listed sites.
- No telemetry; everything stays in the data directory.

## Config

See above; `pc.Config.DesyncAuto` (`desync_auto` in `app.yaml`) embeds it. Hard caps in `Validate`: concurrency <= 2, 1 to 30 presets, 1 to 20 sites, a budget of 10 s to 30 min, a probe timeout of 1 s to 1 min.

## Errors

`Validate` errors; `OpenStore` errors for a directory it cannot create or a file it can neither read nor move aside; `Put` and `SetSelected` write errors (the in-memory state is rolled back).

## Testing

`search_test.go`: a scripted `Applier`/`Prober` and a step clock (its `Sleep` advances the fake time and records the pause): first preset fixes nothing and the second everything (stops, order, hostlist = the failing sites, progress), best partial fix and ties, nothing fixed, preset cap, time budget, domain cut and de-duplication, pause and settle bounds (jitter asked for the configured range), `ErrSkip`, an `Applier` error, unusable measurements, cancellation. `store_test.go`: round trip with mode 0600 and no temp files, corrupt files moved aside, the 32-network cap, config defaults and caps.

Not covered: a real engine or a real probe (the app's tests use fakes).

## Out of scope

Choosing which sites are searched, reading the route table, starting the engine, the HTTP API.
