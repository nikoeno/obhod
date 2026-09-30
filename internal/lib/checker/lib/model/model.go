// Package model defines the decision and network identity types shared inside the checker.
package model

import (
	"strings"
	"time"
	"unicode"

	"github.com/nikoejento/obhod/internal/lib/analyzer"
	"github.com/nikoejento/obhod/internal/lib/route"
)

// MaxIDLen bounds a NetworkID: it becomes a JSON key and appears in logs.
const MaxIDLen = 128

// NetworkID is an opaque label for the current network, e.g. "asn:12389" or "gw:3f9a…".
type NetworkID string

// Valid reports whether id is non-empty, short, has no whitespace/control characters and starts with
// "asn:" or "gw:" (a combined "asn:…/gw:…" also satisfies this).
func (id NetworkID) Valid() bool {
	s := string(id)
	if s == "" || len(s) > MaxIDLen {
		return false
	}
	if !strings.HasPrefix(s, "asn:") && !strings.HasPrefix(s, "gw:") {
		return false
	}
	for _, r := range s {
		if unicode.IsSpace(r) || unicode.IsControl(r) {
			return false
		}
	}
	return true
}

// Decision is the stored route for one domain on one network.
type Decision struct {
	Domain    string                           `json:"domain"`
	Route     route.Route                      `json:"route"`
	Source    route.Source                     `json:"source"`
	DecidedAt time.Time                        `json:"decided_at"`
	CheckedAt time.Time                        `json:"checked_at"`
	ExpiresAt time.Time                        `json:"expires_at"`
	Pending   *Pending                         `json:"pending,omitempty"`
	Last      map[route.Route]analyzer.Verdict `json:"last,omitempty"`
}

// Pending is a candidate route awaiting confirmation (hysteresis).
type Pending struct {
	Route route.Route `json:"route"`
	Count int         `json:"count"` // consecutive agreeing results
	Since time.Time   `json:"since"`
}

// Expired reports whether the decision's TTL has run out at now. A zero ExpiresAt never expires.
func (d Decision) Expired(now time.Time) bool {
	return !d.ExpiresAt.IsZero() && !now.Before(d.ExpiresAt)
}

// Clone returns a deep copy of d.
func (d Decision) Clone() Decision {
	if d.Pending != nil {
		p := *d.Pending
		d.Pending = &p
	}
	if d.Last != nil {
		m := make(map[route.Route]analyzer.Verdict, len(d.Last))
		for k, v := range d.Last {
			m[k] = v
		}
		d.Last = m
	}
	return d
}
