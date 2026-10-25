//go:build !windows && !linux

package pc

// isAdmin cannot tell on this OS; the first failing child reports ErrNeedsAdmin.
func isAdmin() (bool, error) { return true, nil }

const elevationHint = "run splitter with administrator rights"
