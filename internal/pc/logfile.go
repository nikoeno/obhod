package pc

import (
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"sync"

	"github.com/nikoejento/obhod/internal/lib/paths"
)

// rotator is an io.WriteCloser that writes to a file and rotates it by size:
// file → file.1 → file.2 … up to keep rotated files.
type rotator struct {
	path string
	max  int64
	keep int

	mu     sync.Mutex
	f      *os.File
	size   int64
	closed bool
}

func newRotator(path string, maxBytes int64, keep int) (*rotator, error) {
	r := &rotator{path: path, max: maxBytes, keep: keep}
	if err := r.open(); err != nil {
		return nil, err
	}
	return r, nil
}

func (r *rotator) open() error {
	f, err := os.OpenFile(r.path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return err
	}
	st, err := f.Stat()
	if err != nil {
		_ = f.Close()
		return err
	}
	r.f, r.size = f, st.Size()
	return nil
}

func (r *rotator) Write(p []byte) (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed {
		return 0, os.ErrClosed
	}
	if r.f == nil {
		// A reopen after a rotation failed (file locked by a virus scanner,
		// for example); try again instead of losing the log for good.
		if err := r.open(); err != nil {
			return 0, err
		}
	}
	if r.size > 0 && r.size+int64(len(p)) > r.max {
		if err := r.rotate(); err != nil {
			return 0, err
		}
	}
	n, err := r.f.Write(p)
	r.size += int64(n)
	return n, err
}

func (r *rotator) rotate() error {
	if err := r.f.Close(); err != nil {
		return err
	}
	r.f = nil
	_ = os.Remove(fmt.Sprintf("%s.%d", r.path, r.keep))
	for i := r.keep - 1; i >= 1; i-- {
		_ = os.Rename(fmt.Sprintf("%s.%d", r.path, i), fmt.Sprintf("%s.%d", r.path, i+1))
	}
	if err := os.Rename(r.path, r.path+".1"); err != nil {
		_ = os.Remove(r.path) // cannot keep it; start over rather than grow without bound
	}
	return r.open()
}

func (r *rotator) Close() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.closed = true
	if r.f == nil {
		return nil
	}
	err := r.f.Close()
	r.f = nil
	return err
}

// NewLogger logs to stderr and, rotated by size, to a file in Paths.Logs. The
// returned function closes the file.
func NewLogger(cfg LogConfig, p paths.Paths, stderr io.Writer) (*slog.Logger, func(), error) {
	level, _ := parseLevel(cfg.Level)
	var w io.Writer = stderr
	closeFn := func() {}
	if p.Logs != "" {
		if err := os.MkdirAll(p.Logs, 0o700); err != nil {
			return nil, nil, err
		}
		rot, err := newRotator(filepath.Join(p.Logs, cfg.File), int64(cfg.MaxSizeMB)<<20, cfg.MaxFiles)
		if err != nil {
			return nil, nil, err
		}
		w = io.MultiWriter(stderr, rot)
		closeFn = func() { _ = rot.Close() }
	}
	return slog.New(slog.NewTextHandler(w, &slog.HandlerOptions{Level: level})), closeFn, nil
}
