package route

import (
	"fmt"
	"sort"
	"strings"
	"time"
)

// Source says where an entry came from.
type Source string

// The known sources.
const (
	SourceStatic  Source = "static"  // from proxy/block lists; never changed by the checker
	SourceChecked Source = "checked" // decided by the checker from probes
	SourceDefault Source = "default" // safe default for an unknown network (no probe yet)
)

// Valid reports whether s is a known source.
func (s Source) Valid() bool {
	return s == SourceStatic || s == SourceChecked || s == SourceDefault
}

// Entry routes a domain and all its subdomains.
type Entry struct {
	Domain string // normalized, see Normalize
	Route  Route
	Source Source
}

// Table is the complete routing decision for one network at one moment.
// It is a value: consumers must not mutate Entries in place.
type Table struct {
	NetworkID string
	Generated time.Time
	Entries   []Entry // sorted by Domain; one entry per Domain
}

// Change describes how the route of one domain differs between two tables.
type Change struct {
	Domain string
	From   Route // "" if newly added
	To     Route // "" if removed
}

// NewTable validates entries, normalizes their domains, removes duplicates and sorts
// them by Domain. For duplicate domains the last entry wins, but a SourceStatic entry
// is never overridden by an entry of another source. The input slice is copied.
// Errors wrap ErrInvalidRoute (also used for an unknown source) or ErrInvalidDomain.
func NewTable(networkID string, generated time.Time, entries []Entry) (Table, error) {
	byDomain := make(map[string]Entry, len(entries))
	for _, e := range entries {
		if !e.Route.Valid() {
			return Table{}, fmt.Errorf("%w: %q", ErrInvalidRoute, string(e.Route))
		}
		if !e.Source.Valid() {
			return Table{}, fmt.Errorf("%w: unknown source %q", ErrInvalidRoute, string(e.Source))
		}
		d, err := Normalize(e.Domain)
		if err != nil {
			return Table{}, err
		}
		e.Domain = d
		if old, ok := byDomain[d]; ok && old.Source == SourceStatic && e.Source != SourceStatic {
			continue
		}
		byDomain[d] = e
	}
	out := make([]Entry, 0, len(byDomain))
	for _, e := range byDomain {
		out = append(out, e)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Domain < out[j].Domain })
	return Table{NetworkID: networkID, Generated: generated, Entries: out}, nil
}

// find returns the entry with exactly this normalized domain.
func (t Table) find(domain string) (Entry, bool) {
	i := sort.Search(len(t.Entries), func(i int) bool { return t.Entries[i].Domain >= domain })
	if i < len(t.Entries) && t.Entries[i].Domain == domain {
		return t.Entries[i], true
	}
	return Entry{}, false
}

// Lookup returns the entry with the longest domain that equals domain or is a parent
// of it on a label boundary. The input is normalized; an invalid domain never matches.
func (t Table) Lookup(domain string) (Entry, bool) {
	d, err := Normalize(domain)
	if err != nil {
		return Entry{}, false
	}
	for {
		if e, ok := t.find(d); ok {
			return e, true
		}
		i := strings.IndexByte(d, '.')
		if i < 0 {
			return Entry{}, false
		}
		d = d[i+1:]
	}
}

// Domains returns the domains routed to r, sorted.
func (t Table) Domains(r Route) []string {
	out := []string{}
	for _, e := range t.Entries {
		if e.Route == r {
			out = append(out, e.Domain)
		}
	}
	return out
}

// Counts returns the number of entries per route. Routes without entries are absent.
func (t Table) Counts() map[Route]int {
	out := make(map[Route]int)
	for _, e := range t.Entries {
		out[e.Route]++
	}
	return out
}

// Equal reports whether both tables describe the same network and entries.
// Generated is ignored.
func (t Table) Equal(o Table) bool {
	if t.NetworkID != o.NetworkID || len(t.Entries) != len(o.Entries) {
		return false
	}
	for i := range t.Entries {
		if t.Entries[i] != o.Entries[i] {
			return false
		}
	}
	return true
}

// Diff lists the domains whose route differs between old and new, sorted by Domain.
// A change of Source alone is not a change.
func Diff(old, new Table) []Change {
	var out []Change
	i, j := 0, 0
	for i < len(old.Entries) || j < len(new.Entries) {
		switch {
		case j == len(new.Entries) || i < len(old.Entries) && old.Entries[i].Domain < new.Entries[j].Domain:
			out = append(out, Change{Domain: old.Entries[i].Domain, From: old.Entries[i].Route})
			i++
		case i == len(old.Entries) || new.Entries[j].Domain < old.Entries[i].Domain:
			out = append(out, Change{Domain: new.Entries[j].Domain, To: new.Entries[j].Route})
			j++
		default:
			if o, n := old.Entries[i], new.Entries[j]; o.Route != n.Route {
				out = append(out, Change{Domain: o.Domain, From: o.Route, To: n.Route})
			}
			i++
			j++
		}
	}
	return out
}
