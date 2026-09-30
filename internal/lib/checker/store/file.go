package store

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/nikoejento/obhod/internal/lib/checker/lib/model"
	"github.com/nikoejento/obhod/internal/lib/clock"
)

const schemaVersion = 1

// fileFormat is the on-disk shape: {"schema":1,"networks":{"<id>":{...}}}.
type fileFormat struct {
	Schema   int                          `json:"schema"`
	Networks map[model.NetworkID]*network `json:"networks"`
}

// Open opens (or creates) the store file named by cfg.File. A missing file is an empty store. A corrupt or
// foreign-schema file is renamed to "<file>.corrupt-<unix>" and reported through Warnings. Leftover temp
// files from a crashed write are removed.
func Open(cfg Config, c clock.Clock) (Store, error) {
	if cfg.File == "" {
		return nil, errors.New("store: Config.File is empty")
	}
	if c == nil {
		c = clock.Real()
	}
	dir := filepath.Dir(cfg.File)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("store: %w", err)
	}
	s := &store{
		clk: c, path: cfg.File, interval: cfg.FlushInterval,
		nets: map[model.NetworkID]*network{}, done: make(chan struct{}),
	}
	if leftovers, _ := filepath.Glob(filepath.Join(dir, "decisions-*.tmp")); leftovers != nil {
		for _, f := range leftovers {
			_ = os.Remove(f)
		}
	}
	data, err := os.ReadFile(cfg.File)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return s, nil
	case err != nil:
		return nil, fmt.Errorf("store: %w", err)
	}
	var ff fileFormat
	if jerr := json.Unmarshal(data, &ff); jerr != nil || ff.Schema != schemaVersion {
		reason := fmt.Sprintf("schema %d", ff.Schema)
		if jerr != nil {
			reason = jerr.Error()
		}
		aside := fmt.Sprintf("%s.corrupt-%d", cfg.File, c.Now().Unix())
		if rerr := os.Rename(cfg.File, aside); rerr != nil {
			return nil, fmt.Errorf("store: cannot set aside corrupt file: %w", rerr)
		}
		s.warnings = append(s.warnings, fmt.Sprintf("store: %s unusable (%s); moved to %s and started empty", cfg.File, reason, aside))
		return s, nil
	}
	for id, n := range ff.Networks {
		if n == nil || !id.Valid() {
			continue
		}
		if n.Decisions == nil {
			n.Decisions = map[string]model.Decision{}
		}
		s.nets[id] = n
	}
	return s, nil
}

// writeLocked writes the whole file atomically: temp file in the same dir → fsync → rename → fsync dir.
// On success dirty is cleared. s.mu must be held.
func (s *store) writeLocked() error {
	data, err := json.MarshalIndent(fileFormat{Schema: schemaVersion, Networks: s.nets}, "", " ")
	if err != nil {
		return fmt.Errorf("store: %w", err)
	}
	dir := filepath.Dir(s.path)
	tmp, err := os.CreateTemp(dir, "decisions-*.tmp")
	if err != nil {
		return fmt.Errorf("store: %w", err)
	}
	name := tmp.Name()
	fail := func(err error) error {
		tmp.Close()
		os.Remove(name)
		return fmt.Errorf("store: %w", err)
	}
	if err := chmodPrivate(tmp); err != nil {
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
		return fmt.Errorf("store: %w", err)
	}
	if err := os.Rename(name, s.path); err != nil {
		os.Remove(name)
		return fmt.Errorf("store: %w", err)
	}
	if err := syncDir(dir); err != nil {
		return fmt.Errorf("store: %w", err)
	}
	s.dirty = false
	if s.onWrite != nil {
		s.onWrite()
	}
	return nil
}
