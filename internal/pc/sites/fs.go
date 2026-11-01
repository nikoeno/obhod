package sites

import (
	"io/fs"
	"os"
	"path/filepath"
)

type osFS struct{}

// OSFS returns the real file system.
func OSFS() FS { return osFS{} }

func (osFS) MkdirAll(dir string, perm fs.FileMode) error { return os.MkdirAll(dir, perm) }
func (osFS) ReadFile(path string) ([]byte, error)        { return os.ReadFile(path) }
func (osFS) Rename(oldPath, newPath string) error        { return os.Rename(oldPath, newPath) }

// WriteFileAtomic writes to a temporary file in the same directory, syncs it,
// sets perm and renames it over path.
func (osFS) WriteFileAtomic(path string, data []byte, perm fs.FileMode) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), filepath.Base(path)+".tmp*")
	if err != nil {
		return err
	}
	name := tmp.Name()
	fail := func(err error) error {
		tmp.Close()
		os.Remove(name)
		return err
	}
	if err := os.Chmod(name, perm); err != nil { // before the data is written
		return fail(err)
	}
	if _, err := tmp.Write(data); err != nil {
		return fail(err)
	}
	if err := tmp.Sync(); err != nil {
		return fail(err)
	}
	if err := tmp.Close(); err != nil {
		os.Remove(name)
		return err
	}
	if err := os.Rename(name, path); err != nil {
		os.Remove(name)
		return err
	}
	return nil
}
