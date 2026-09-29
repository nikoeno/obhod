package proc

import (
	"context"
	"errors"
	"strings"
)

// ErrUnsupported is returned by System on operating systems without a process lister.
var ErrUnsupported = errors.New("proc: unsupported on this OS")

// Process is one running process.
type Process struct {
	PID  int
	PPID int
	Name string // executable base name, lowercased, without .exe
	Path string // may be "" if not readable
}

// Lister lists running processes.
type Lister interface {
	List(ctx context.Context) ([]Process, error)
}

// normalize lowercases a process name and strips a trailing ".exe".
func normalize(name string) string {
	return strings.TrimSuffix(strings.ToLower(strings.TrimSpace(name)), ".exe")
}

// Find returns the processes whose Name matches any of names (case-insensitive, ".exe" ignored).
func Find(ctx context.Context, l Lister, names ...string) ([]Process, error) {
	all, err := l.List(ctx)
	if err != nil {
		return nil, err
	}
	want := make(map[string]bool, len(names))
	for _, n := range names {
		want[normalize(n)] = true
	}
	var out []Process
	for _, p := range all {
		if want[normalize(p.Name)] {
			out = append(out, p)
		}
	}
	return out, nil
}

type fake struct{ ps []Process }

// NewFake returns a Lister with a fixed process list (tests).
func NewFake(ps ...Process) Lister { return fake{ps: ps} }

func (f fake) List(ctx context.Context) ([]Process, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return append([]Process(nil), f.ps...), nil
}
