package autodesync

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"
)

// File names inside the directory given to OpenStore.
const (
	FileName    = "desync.json"
	CorruptName = "desync.json.corrupt"

	fileMode    = 0o600
	dirMode     = 0o700
	version     = 1
	maxNetworks = 32
	maxList     = 64
	maxName     = 256
)

// Record is what one network remembers.
type Record struct {
	// Preset is the catalogue id that fixed the sites of the last search; "" when none did (the app then runs
	// the default preset).
	Preset string `json:"preset"`
	// At is when the last search on this network ended.
	At time.Time `json:"at"`
	// Fixed are the sites that were reached under Preset (the verified ones).
	Fixed []string `json:"fixed"`
	// Failed maps a site that no tried preset fixed to the time of that search.
	Failed map[string]time.Time `json:"failed"`
}

func (r Record) clone() Record {
	r.Fixed = append([]string(nil), r.Fixed...)
	f := make(map[string]time.Time, len(r.Failed))
	for k, v := range r.Failed {
		f[k] = v
	}
	r.Failed = f
	return r
}

// FS is the file system the store writes to; tests may replace it.
type FS interface {
	MkdirAll(dir string, perm fs.FileMode) error
	ReadFile(path string) ([]byte, error)
	// WriteFileAtomic writes data to a temporary file next to path, sets perm and renames it over path.
	WriteFileAtomic(path string, data []byte, perm fs.FileMode) error
	Rename(oldPath, newPath string) error
}

type osFS struct{}

// OSFS returns the real file system.
func OSFS() FS { return osFS{} }

func (osFS) MkdirAll(dir string, perm fs.FileMode) error { return os.MkdirAll(dir, perm) }
func (osFS) ReadFile(path string) ([]byte, error)        { return os.ReadFile(path) }
func (osFS) Rename(oldPath, newPath string) error        { return os.Rename(oldPath, newPath) }

func (osFS) WriteFileAtomic(path string, data []byte, perm fs.FileMode) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), filepath.Base(path)+".tmp*")
	if err != nil {
		return err
	}
	name := tmp.Name()
	fail := func(err error) error {
		tmp.Close()
		os.Remove(name)
		return err
	}
	if err := os.Chmod(name, perm); err != nil { // before the data is written
		return fail(err)
	}
	if _, err := tmp.Write(data); err != nil {
		return fail(err)
	}
	if err := tmp.Sync(); err != nil {
		return fail(err)
	}
	if err := tmp.Close(); err != nil {
		os.Remove(name)
		return err
	}
	if err := os.Rename(name, path); err != nil {
		os.Remove(name)
		return err
	}
	return nil
}

type file struct {
	Version  int               `json:"version"`
	Selected string            `json:"selected"`
	Networks map[string]Record `json:"networks"`
}

// Store keeps the choice per network, and the user's selection, in <dir>/desync.json (0600, atomic writes).
// It holds network IDs (opaque, salted hashes, never the public IP) and the names of listed sites. Safe for
// concurrent use.
type Store struct {
	dir string
	fs  FS

	recovered bool

	mu       sync.Mutex
	selected string
	nets     map[string]Record
}

// OpenStore opens the store in dir (Paths.Data), creating the directory (0700) when missing. A missing file is
// an empty store. A file that is not valid is moved to CorruptName and the store starts empty (Recovered).
func OpenStore(dir string, fsys FS) (*Store, error) {
	if fsys == nil {
		fsys = OSFS()
	}
	s := &Store{dir: dir, fs: fsys, nets: map[string]Record{}}
	if err := fsys.MkdirAll(dir, dirMode); err != nil {
		return nil, fmt.Errorf("autodesync: create directory: %w", err)
	}
	path := filepath.Join(dir, FileName)
	b, err := fsys.ReadFile(path)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return s, nil
	case err != nil:
		return nil, fmt.Errorf("autodesync: read: %w", err)
	}
	f, perr := parse(b)
	if perr != nil {
		if err := fsys.Rename(path, filepath.Join(dir, CorruptName)); err != nil {
			return nil, fmt.Errorf("autodesync: move the unreadable file aside: %w", err)
		}
		s.recovered = true
		return s, nil
	}
	s.selected, s.nets = f.Selected, f.Networks
	return s, nil
}

func parse(b []byte) (file, error) {
	var f file
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.DisallowUnknownFields()
	bad := errors.New("not valid")
	if err := dec.Decode(&f); err != nil || f.Version != version || len(f.Networks) > maxNetworks || len(f.Selected) > maxName {
		return f, bad
	}
	if f.Networks == nil {
		f.Networks = map[string]Record{}
	}
	for id, r := range f.Networks {
		if id == "" || len(id) > maxName || len(r.Preset) > maxName || len(r.Fixed) > maxList || len(r.Failed) > maxList {
			return f, bad
		}
		for _, d := range r.Fixed {
			if d == "" || len(d) > maxName {
				return f, bad
			}
		}
		for d := range r.Failed {
			if d == "" || len(d) > maxName {
				return f, bad
			}
		}
		if r.Failed == nil {
			r.Failed = map[string]time.Time{}
			f.Networks[id] = r
		}
	}
	return f, nil
}

// Recovered reports whether OpenStore moved an unreadable file aside.
func (s *Store) Recovered() bool { return s.recovered }

// Selected returns the preset the user chose through the API ("auto" or a catalogue id), "" when none: the
// app then follows its config.
func (s *Store) Selected() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.selected
}

// SetSelected stores the user's choice ("" clears it).
func (s *Store) SetSelected(v string) error {
	if len(v) > maxName {
		return errors.New("autodesync: selection too long")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	old := s.selected
	s.selected = v
	if err := s.saveLocked(); err != nil {
		s.selected = old
		return err
	}
	return nil
}

// Get returns a copy of the record of a network.
func (s *Store) Get(netID string) (Record, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	r, ok := s.nets[netID]
	if !ok {
		return Record{}, false
	}
	return r.clone(), true
}

// Put stores the record of a network (atomic). At most 32 networks are kept; the oldest go first.
func (s *Store) Put(netID string, r Record) error {
	if netID == "" || len(netID) > maxName {
		return errors.New("autodesync: bad network id")
	}
	if len(r.Fixed) > maxList || len(r.Failed) > maxList {
		return errors.New("autodesync: record too large")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	oldNets := s.nets
	next := make(map[string]Record, len(s.nets)+1)
	for k, v := range s.nets {
		next[k] = v
	}
	next[netID] = r.clone()
	for len(next) > maxNetworks {
		var oldest string
		for k, v := range next {
			if k != netID && (oldest == "" || v.At.Before(next[oldest].At)) {
				oldest = k
			}
		}
		delete(next, oldest)
	}
	s.nets = next
	if err := s.saveLocked(); err != nil {
		s.nets = oldNets
		return err
	}
	return nil
}

func (s *Store) saveLocked() error {
	nets := make(map[string]Record, len(s.nets))
	for k, v := range s.nets {
		v = v.clone()
		sort.Strings(v.Fixed)
		nets[k] = v
	}
	b, err := json.MarshalIndent(file{Version: version, Selected: s.selected, Networks: nets}, "", "  ")
	if err != nil {
		return err
	}
	if err := s.fs.WriteFileAtomic(filepath.Join(s.dir, FileName), append(b, '\n'), fileMode); err != nil {
		return fmt.Errorf("autodesync: write: %w", err)
	}
	return nil
}
