package servers

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// Server is the non-secret summary of a stored server. It never holds a key,
// a UUID, a password, the link or the outbound.
type Server struct {
	ID       string    `json:"id"`
	Name     string    `json:"name"`
	Protocol string    `json:"protocol"` // sing-box outbound "type": vless | vmess | trojan | shadowsocks | hysteria2 | tuic
	Host     string    `json:"host"`
	Port     int       `json:"port"`
	Engine   string    `json:"engine"` // "singbox"; "xray" is reserved
	AddedAt  time.Time `json:"addedAt"`
	Active   bool      `json:"active"`
}

// Checker is the dry run of a candidate outbound: the caller renders a full
// sing-box config with it and runs `sing-box check`. A nil Checker skips it.
// Its error text is never shown or logged by this package.
type Checker func(ctx context.Context, outbound json.RawMessage) error

// FS is the file system the store writes to; tests may replace it.
type FS interface {
	MkdirAll(dir string, perm fs.FileMode) error
	ReadFile(path string) ([]byte, error)
	// WriteFileAtomic writes data to a temporary file next to path, sets perm and renames it over path.
	WriteFileAtomic(path string, data []byte, perm fs.FileMode) error
	Remove(path string) error
	// ReadDir returns the names of the entries of dir (not sorted).
	ReadDir(dir string) ([]string, error)
}

// Option customizes New.
type Option func(*Store)

// WithClock sets the clock that stamps AddedAt (default time.Now).
func WithClock(now func() time.Time) Option { return func(s *Store) { s.now = now } }

const (
	indexName = "index.json"
	fileMode  = 0o600
	dirMode   = 0o700
)

type indexFile struct {
	Active  string       `json:"active"`
	Servers []indexEntry `json:"servers"`
}

// indexEntry has no secret: no key, UUID, password or option of the outbound.
type indexEntry struct {
	ID       string    `json:"id"`
	Name     string    `json:"name"`
	Protocol string    `json:"protocol"`
	Host     string    `json:"host"`
	Port     int       `json:"port"`
	Engine   string    `json:"engine"`
	AddedAt  time.Time `json:"addedAt"`
}

// Store keeps the servers in dir: <id>.json holds the raw outbound (0600) and
// index.json the summaries. It is safe for concurrent use.
type Store struct {
	dir   string
	fs    FS
	check Checker
	newID func() string
	now   func() time.Time

	mu  sync.Mutex
	idx indexFile
}

// New opens the store in dir (Paths.Secrets/servers), creating it with mode
// 0700 and removing leftover temporary files. newID returns 16 random hex
// characters; nil uses crypto/rand.
func New(dir string, fsys FS, check Checker, newID func() string, opts ...Option) (*Store, error) {
	if fsys == nil {
		fsys = OSFS()
	}
	if newID == nil {
		newID = RandomID
	}
	s := &Store{dir: dir, fs: fsys, check: check, newID: newID, now: time.Now}
	for _, o := range opts {
		o(s)
	}
	if err := fsys.MkdirAll(dir, dirMode); err != nil {
		return nil, fmt.Errorf("servers: create directory: %w", err)
	}
	if names, err := fsys.ReadDir(dir); err == nil {
		for _, n := range names {
			if strings.Contains(n, ".tmp") {
				_ = fsys.Remove(filepath.Join(dir, n)) // left by an interrupted write
			}
		}
	}
	b, err := fsys.ReadFile(filepath.Join(dir, indexName))
	switch {
	case errors.Is(err, fs.ErrNotExist):
	case err != nil:
		return nil, fmt.Errorf("servers: read index: %w", err)
	default:
		if err := json.Unmarshal(b, &s.idx); err != nil {
			return nil, errors.New("servers: the index file is not valid")
		}
		for _, e := range s.idx.Servers {
			if !validID(e.ID) {
				return nil, errors.New("servers: the index file is not valid")
			}
		}
	}
	return s, nil
}

// RandomID returns 16 random hexadecimal characters from crypto/rand.
func RandomID() string {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic("servers: no randomness: " + err.Error())
	}
	return hex.EncodeToString(b[:])
}

func validID(id string) bool {
	if len(id) != 16 {
		return false
	}
	for _, r := range id {
		if !(r >= '0' && r <= '9' || r >= 'a' && r <= 'f') {
			return false
		}
	}
	return true
}

func (s *Store) file(id string) string { return filepath.Join(s.dir, id+".json") }
func (s *Store) indexPath() string     { return filepath.Join(s.dir, indexName) }
func (s *Store) server(e indexEntry) Server {
	return Server{ID: e.ID, Name: e.Name, Protocol: e.Protocol, Host: e.Host, Port: e.Port, Engine: e.Engine, AddedAt: e.AddedAt, Active: e.ID == s.idx.Active}
}

func (s *Store) find(id string) int {
	for i, e := range s.idx.Servers {
		if e.ID == id {
			return i
		}
	}
	return -1
}

// writeIndex stores idx atomically; on failure the caller restores its copy.
func (s *Store) writeIndex(idx indexFile) error {
	if idx.Servers == nil {
		idx.Servers = []indexEntry{}
	}
	b, err := json.MarshalIndent(idx, "", "  ")
	if err != nil {
		return err
	}
	return s.fs.WriteFileAtomic(s.indexPath(), append(b, '\n'), fileMode)
}

// List returns the servers, oldest first; never nil.
func (s *Store) List() ([]Server, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]Server, 0, len(s.idx.Servers))
	for _, e := range s.idx.Servers {
		out = append(out, s.server(e))
	}
	return out, nil
}

// Get returns one server.
func (s *Store) Get(id string) (Server, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	i := s.find(id)
	if !validID(id) || i < 0 {
		return Server{}, newError(ErrNotFound, "There is no such server.")
	}
	return s.server(s.idx.Servers[i]), nil
}

// Active returns the id of the active server, "" when none.
func (s *Store) Active() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.idx.Active
}

// Add parses input, runs the dry run, and stores the server (not activated).
// name is optional (1 to 60 printable characters); the default comes from the
// link's fragment, else "Server N". Order of the checks: size and syntax,
// name, duplicate, limit, dry run.
func (s *Store) Add(ctx context.Context, input, name string) (Server, error) {
	p, err := Parse(input)
	if err != nil {
		return Server{}, err
	}
	if name != "" && !ValidName(name) {
		return Server{}, invalid("The name must be 1 to 60 printable characters.")
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	id, ok := identity(p.Outbound)
	if !ok {
		return Server{}, invalid("The server settings are not valid.")
	}
	for _, e := range s.idx.Servers {
		raw, err := s.fs.ReadFile(s.file(e.ID))
		if err != nil {
			continue
		}
		if have, ok := identity(raw); ok && have == id {
			return Server{}, newError(ErrDuplicate, "This server is already in the list.")
		}
	}
	if len(s.idx.Servers) >= MaxServers {
		return Server{}, newError(ErrLimit, "The list is full (20 servers). Remove one first.")
	}
	if err := s.dryRun(ctx, p.Outbound); err != nil {
		return Server{}, err
	}

	if name == "" {
		name = p.Name
	}
	if name == "" {
		name = s.defaultName()
	}
	var sid string
	for {
		sid = s.newID()
		if validID(sid) && s.find(sid) < 0 {
			if _, err := s.fs.ReadFile(s.file(sid)); errors.Is(err, fs.ErrNotExist) {
				break
			}
		}
	}
	if err := s.fs.WriteFileAtomic(s.file(sid), append(append([]byte(nil), p.Outbound...), '\n'), fileMode); err != nil {
		return Server{}, fmt.Errorf("servers: store: %w", err)
	}
	entry := indexEntry{ID: sid, Name: name, Protocol: p.Protocol, Host: p.Host, Port: p.Port, Engine: p.Engine, AddedAt: s.now().UTC().Truncate(time.Second)}
	next := indexFile{Active: s.idx.Active, Servers: append(append([]indexEntry(nil), s.idx.Servers...), entry)}
	if err := s.writeIndex(next); err != nil {
		_ = s.fs.Remove(s.file(sid))
		return Server{}, fmt.Errorf("servers: store index: %w", err)
	}
	s.idx = next
	return s.server(entry), nil
}

func (s *Store) defaultName() string {
	used := map[string]bool{}
	for _, e := range s.idx.Servers {
		used[e.Name] = true
	}
	for n := len(s.idx.Servers) + 1; ; n++ {
		if name := fmt.Sprintf("Server %d", n); !used[name] {
			return name
		}
	}
}

// dryRun runs the injected checker. A context error passes through; any other
// failure becomes a fixed sentence (the checker's text is dropped).
func (s *Store) dryRun(ctx context.Context, outbound json.RawMessage) error {
	if s.check == nil {
		return nil
	}
	if err := s.check(ctx, outbound); err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return invalid("The sing-box engine did not accept this server's settings.")
	}
	return nil
}

// Outbound returns the stored outbound JSON of id. Only the composition module
// uses it; the gateway never gets it.
func (s *Store) Outbound(id string) (json.RawMessage, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.outbound(id)
}

func (s *Store) outbound(id string) (json.RawMessage, error) {
	if !validID(id) || s.find(id) < 0 {
		return nil, newError(ErrNotFound, "There is no such server.")
	}
	b, err := s.fs.ReadFile(s.file(id))
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, newError(ErrNotFound, "There is no such server.")
		}
		return nil, fmt.Errorf("servers: read server: %w", err)
	}
	var obj map[string]json.RawMessage
	if json.Unmarshal(b, &obj) != nil || obj == nil {
		return nil, errors.New("servers: the stored server file is not valid")
	}
	return json.RawMessage(bytes.TrimSpace(b)), nil
}

// Verify runs the dry run again on a stored server.
func (s *Store) Verify(ctx context.Context, id string) error {
	ob, err := s.Outbound(id)
	if err != nil {
		return err
	}
	return s.dryRun(ctx, ob)
}

// Test parses input (or loads the stored server id) and runs the dry run. It
// touches nothing and connects to nothing. The protocol is returned whenever
// it is known, also with an error.
func (s *Store) Test(ctx context.Context, id, input string) (string, error) {
	var ob json.RawMessage
	protocol := ""
	if id != "" {
		var err error
		if ob, err = s.Outbound(id); err != nil {
			return "", err
		}
		var t struct {
			Type string `json:"type"`
		}
		_ = json.Unmarshal(ob, &t)
		protocol = t.Type
	} else {
		p, err := Parse(input)
		if err != nil {
			return "", err
		}
		ob, protocol = p.Outbound, p.Protocol
	}
	return protocol, s.dryRun(ctx, ob)
}

// SetActive marks id as the active server ("" clears it). The caller copies
// the file to the tunnel name (ExportTo).
func (s *Store) SetActive(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if id != "" && (!validID(id) || s.find(id) < 0) {
		return newError(ErrNotFound, "There is no such server.")
	}
	next := indexFile{Active: id, Servers: s.idx.Servers}
	if err := s.writeIndex(next); err != nil {
		return fmt.Errorf("servers: store index: %w", err)
	}
	s.idx = next
	return nil
}

// ExportTo writes the stored outbound of id to file (the tunnel secret) with
// write-temp-and-rename, mode 0600. It is a copy, not a link.
func (s *Store) ExportTo(id, file string) error {
	ob, err := s.Outbound(id)
	if err != nil {
		return err
	}
	if err := s.fs.WriteFileAtomic(file, append(append([]byte(nil), ob...), '\n'), fileMode); err != nil {
		return fmt.Errorf("servers: write tunnel file: %w", err)
	}
	return nil
}

// Delete removes a server. Deleting the active one clears the active mark; the
// caller removes the tunnel file. Whether deleting is allowed right now
// (connected) is the caller's rule.
func (s *Store) Delete(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	i := s.find(id)
	if !validID(id) || i < 0 {
		return newError(ErrNotFound, "There is no such server.")
	}
	next := indexFile{Active: s.idx.Active, Servers: append(append([]indexEntry(nil), s.idx.Servers[:i]...), s.idx.Servers[i+1:]...)}
	if next.Active == id {
		next.Active = ""
	}
	if err := s.writeIndex(next); err != nil {
		return fmt.Errorf("servers: store index: %w", err)
	}
	s.idx = next
	if err := s.fs.Remove(s.file(id)); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("servers: remove server file: %w", err)
	}
	return nil
}
