package sites

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/nikoejento/obhod/internal/lib/route"
)

// Limits and file names.
const (
	// MaxRules is the number of rules the store accepts.
	MaxRules = 500
	// FileName is the rules file inside the directory given to Open.
	FileName = "sites.json"
	// CorruptName is where an unreadable rules file is moved (the store then starts empty).
	CorruptName = "sites.json.corrupt"

	fileMode = 0o600
	dirMode  = 0o700
	version  = 1
)

// Action is what a rule does with a domain.
type Action string

// The actions. They are the routes a user may force; "none" is a deleted rule.
const (
	Direct Action = "direct"
	Desync Action = "desync"
	Tunnel Action = "tunnel"
	Block  Action = "block"
)

// Actions lists the valid actions in a stable order.
func Actions() []Action { return []Action{Direct, Desync, Tunnel, Block} }

// Route is the route the action forces.
func (a Action) Route() route.Route { return route.Route(a) }

// Rule is one per-site rule. It covers the domain and its subdomains.
type Rule struct {
	Domain  string    `json:"domain"` // normalized (NormalizeDomain)
	Action  Action    `json:"action"`
	AddedAt time.Time `json:"addedAt"` // when the rule was first added; an update keeps it
}

// Errors; test them with errors.Is. They hold no value of the input.
var (
	ErrInvalidDomain = errors.New("sites: invalid domain")
	ErrInvalidAction = errors.New("sites: invalid action")
	ErrLimit         = errors.New("sites: too many rules")
	ErrNotFound      = errors.New("sites: no such rule")
)

// FS is the file system the store writes to; tests may replace it.
type FS interface {
	MkdirAll(dir string, perm fs.FileMode) error
	ReadFile(path string) ([]byte, error)
	// WriteFileAtomic writes data to a temporary file next to path, sets perm and renames it over path.
	WriteFileAtomic(path string, data []byte, perm fs.FileMode) error
	Rename(oldPath, newPath string) error
}

// Option customizes Open.
type Option func(*Store)

// WithClock sets the clock that stamps AddedAt (default time.Now).
func WithClock(now func() time.Time) Option { return func(s *Store) { s.now = now } }

// NormalizeDomain returns the stored form of a domain: a leading "*." or "." is dropped (as list files do),
// then route.Normalize applies (lower case, no trailing dot, punycode; schemes, paths, ports, IP addresses
// and inner wildcards are rejected). The error is ErrInvalidDomain and never holds the input.
func NormalizeDomain(s string) (string, error) {
	d := strings.TrimSpace(s)
	d = strings.TrimPrefix(d, "*.")
	d = strings.TrimPrefix(d, ".")
	n, err := route.Normalize(d)
	if err != nil {
		return "", ErrInvalidDomain
	}
	return n, nil
}

// ParseAction accepts the four actions (case-insensitive); anything else, including "none" and "proxy",
// is ErrInvalidAction.
func ParseAction(s string) (Action, error) {
	a := Action(strings.ToLower(strings.TrimSpace(s)))
	for _, v := range Actions() {
		if a == v {
			return a, nil
		}
	}
	return "", ErrInvalidAction
}

type file struct {
	Version int    `json:"version"`
	Rules   []Rule `json:"rules"`
}

// Store keeps the rules in <dir>/sites.json (0600, atomic writes). It is safe for concurrent use.
type Store struct {
	dir string
	fs  FS
	now func() time.Time

	// recovered is true when Open found an unreadable file and moved it aside.
	recovered bool

	mu    sync.Mutex
	rules map[string]Rule
}

// Open opens the store in dir (Paths.Data), creating the directory (0700) when missing. A missing file is an
// empty store. A file that is not valid (bad JSON, unknown version or field, bad domain or action,
// duplicates, over MaxRules) is moved to CorruptName and the store starts empty; Recovered reports it. A
// read or rename error is returned.
func Open(dir string, fsys FS, opts ...Option) (*Store, error) {
	if fsys == nil {
		fsys = OSFS()
	}
	s := &Store{dir: dir, fs: fsys, now: time.Now, rules: map[string]Rule{}}
	for _, o := range opts {
		o(s)
	}
	if err := fsys.MkdirAll(dir, dirMode); err != nil {
		return nil, fmt.Errorf("sites: create directory: %w", err)
	}
	path := filepath.Join(dir, FileName)
	b, err := fsys.ReadFile(path)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return s, nil
	case err != nil:
		return nil, fmt.Errorf("sites: read rules: %w", err)
	}
	rules, perr := parse(b)
	if perr != nil {
		if err := fsys.Rename(path, filepath.Join(dir, CorruptName)); err != nil {
			return nil, fmt.Errorf("sites: move the unreadable rules file aside: %w", err)
		}
		s.recovered = true
		return s, nil
	}
	s.rules = rules
	return s, nil
}

func parse(b []byte) (map[string]Rule, error) {
	var f file
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&f); err != nil || f.Version != version || len(f.Rules) > MaxRules {
		return nil, errors.New("not valid")
	}
	out := make(map[string]Rule, len(f.Rules))
	for _, r := range f.Rules {
		d, err := NormalizeDomain(r.Domain)
		if err != nil || d != r.Domain {
			return nil, errors.New("not valid")
		}
		if a, err := ParseAction(string(r.Action)); err != nil || a != r.Action {
			return nil, errors.New("not valid")
		}
		if _, dup := out[d]; dup {
			return nil, errors.New("not valid")
		}
		out[d] = r
	}
	return out, nil
}

// Recovered reports whether Open moved an unreadable file aside.
func (s *Store) Recovered() bool { return s.recovered }

func sorted(m map[string]Rule) []Rule {
	out := make([]Rule, 0, len(m))
	for _, r := range m {
		out = append(out, r)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Domain < out[j].Domain })
	return out
}

// List returns the rules sorted by domain; never nil.
func (s *Store) List() []Rule {
	s.mu.Lock()
	defer s.mu.Unlock()
	return sorted(s.rules)
}

// Entries returns the rules as route entries (Source user), sorted by domain, for the checker.
func (s *Store) Entries() []route.Entry {
	s.mu.Lock()
	defer s.mu.Unlock()
	rs := sorted(s.rules)
	out := make([]route.Entry, 0, len(rs))
	for _, r := range rs {
		out = append(out, route.Entry{Domain: r.Domain, Route: r.Action.Route(), Source: route.SourceUser})
	}
	return out
}

// save writes the rules; the caller replaces the in-memory map only after it succeeded.
func (s *Store) save(m map[string]Rule) error {
	b, err := json.MarshalIndent(file{Version: version, Rules: sorted(m)}, "", "  ")
	if err != nil {
		return err
	}
	if err := s.fs.MkdirAll(s.dir, dirMode); err != nil {
		return err
	}
	return s.fs.WriteFileAtomic(filepath.Join(s.dir, FileName), append(b, '\n'), fileMode)
}

func clone(m map[string]Rule) map[string]Rule {
	c := make(map[string]Rule, len(m)+1)
	for k, v := range m {
		c[k] = v
	}
	return c
}

// Set adds or updates the rule of a domain (upsert). It returns the stored rule and whether it is new. An
// update keeps AddedAt. Errors: ErrInvalidDomain, ErrInvalidAction, ErrLimit (a new rule beyond MaxRules),
// or the write error; on any error nothing changes.
func (s *Store) Set(domain, action string) (Rule, bool, error) {
	d, err := NormalizeDomain(domain)
	if err != nil {
		return Rule{}, false, err
	}
	a, err := ParseAction(action)
	if err != nil {
		return Rule{}, false, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	old, exists := s.rules[d]
	if !exists && len(s.rules) >= MaxRules {
		return Rule{}, false, ErrLimit
	}
	r := Rule{Domain: d, Action: a, AddedAt: s.now().UTC()}
	if exists {
		r.AddedAt = old.AddedAt
	}
	next := clone(s.rules)
	next[d] = r
	if err := s.save(next); err != nil {
		return Rule{}, false, fmt.Errorf("sites: save: %w", err)
	}
	s.rules = next
	return r, !exists, nil
}

// Delete removes the rule of a domain. ErrInvalidDomain for a bad name, ErrNotFound when there is no rule.
func (s *Store) Delete(domain string) error {
	d, err := NormalizeDomain(domain)
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.rules[d]; !ok {
		return ErrNotFound
	}
	next := clone(s.rules)
	delete(next, d)
	if err := s.save(next); err != nil {
		return fmt.Errorf("sites: save: %w", err)
	}
	s.rules = next
	return nil
}
