//go:build !linux && !windows

package pc

// lockInstance is a no-op where the app does not run.
func lockInstance(string) (release func(), err error) { return func() {}, nil }
