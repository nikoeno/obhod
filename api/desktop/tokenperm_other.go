//go:build !linux

package desktop

// checkTokenPerm does nothing here: Windows access rights are not Unix modes,
// and the UI keeps the file in its per-user directory.
func checkTokenPerm(string) error { return nil }
