package procman

import (
	"strings"
	"sync"
)

// maxPartial bounds an unterminated line so a child without newlines cannot grow memory.
const maxPartial = 8 << 10

// ring keeps the last lines written to it. It implements io.Writer.
type ring struct {
	mu      sync.Mutex
	max     int
	lines   []string
	partial []byte
}

func newRing(max int) *ring { return &ring{max: max} }

func (r *ring) Write(p []byte) (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.partial = append(r.partial, p...)
	for {
		i := indexByte(r.partial, '\n')
		if i < 0 {
			break
		}
		r.push(strings.TrimRight(string(r.partial[:i]), "\r"))
		r.partial = r.partial[i+1:]
	}
	if len(r.partial) > maxPartial {
		r.push(string(r.partial))
		r.partial = nil
	}
	return len(p), nil
}

func (r *ring) push(line string) {
	if len(r.lines) == r.max {
		copy(r.lines, r.lines[1:])
		r.lines = r.lines[:r.max-1]
	}
	r.lines = append(r.lines, line)
}

// Lines returns the lines, oldest first, including a last line that has no
// trailing newline yet (a panic message of a child that died).
func (r *ring) Lines() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := append([]string(nil), r.lines...)
	if len(r.partial) > 0 {
		out = append(out, strings.TrimSuffix(string(r.partial), "\r"))
		if len(out) > r.max {
			out = out[1:]
		}
	}
	return out
}

func indexByte(b []byte, c byte) int {
	for i, x := range b {
		if x == c {
			return i
		}
	}
	return -1
}
