//go:build !linux && !windows

package proc

import "context"

// System is not supported on this OS.
func System() Lister { return unsupported{} }

type unsupported struct{}

func (unsupported) List(context.Context) ([]Process, error) { return nil, ErrUnsupported }
