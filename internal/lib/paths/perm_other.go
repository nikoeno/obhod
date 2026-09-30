//go:build !windows

package paths

// restrictACL is a no-op: POSIX permissions set by Ensure are enough.
func restrictACL(string) error { return nil }
