//go:build !linux

package store

import "os"

// chmodPrivate is a no-op: on Windows the file inherits the ACL of the data dir (see paths.Ensure).
func chmodPrivate(*os.File) error { return nil }

// syncDir is a no-op where directories cannot be fsynced.
func syncDir(string) error { return nil }
