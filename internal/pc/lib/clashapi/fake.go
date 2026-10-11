package clashapi

import (
	"context"
	"sync"
)

// FakeVersion is the string a fake Client reports from Version.
const FakeVersion = "sing-box fake"

type fake struct {
	mu   sync.Mutex
	snap []Snapshot
	next int
}

// NewFake returns a Client that serves the snapshots in order and then repeats
// the last one. With no snapshots it serves an empty Snapshot. It is safe for
// concurrent use.
func NewFake(snapshots ...Snapshot) Client {
	return &fake{snap: snapshots}
}

func (f *fake) Connections(ctx context.Context) (Snapshot, error) {
	if err := ctx.Err(); err != nil {
		return Snapshot{}, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.snap) == 0 {
		return Snapshot{}, nil
	}
	i := f.next
	if i >= len(f.snap) {
		i = len(f.snap) - 1
	} else {
		f.next++
	}
	return f.snap[i], nil
}

func (f *fake) Version(ctx context.Context) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	return FakeVersion, nil
}
