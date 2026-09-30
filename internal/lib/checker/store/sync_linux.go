package store

import "os"

// chmodPrivate makes the temp file readable by the owner only.
func chmodPrivate(f *os.File) error { return f.Chmod(0o600) }

// syncDir flushes the directory entry of the renamed file.
func syncDir(dir string) error {
	d, err := os.Open(dir)
	if err != nil {
		return err
	}
	defer d.Close()
	return d.Sync()
}
