package paths

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Errors returned by this package.
var (
	// ErrUnsupported means the OS has no known runtime layout.
	ErrUnsupported = errors.New("paths: unsupported on this OS")
	// ErrACL means the directories were created but restricting their Windows ACL failed.
	// The application may treat it as a warning: check it with errors.Is.
	ErrACL = errors.New("paths: could not restrict directory permissions")
)

// Paths lists the runtime locations of one application.
type Paths struct {
	Config  string // app.yaml
	Data    string // decisions, caches
	Logs    string
	Secrets string // tunnel credentials; never logged
	Run     string // generated configs, pid files (may be tmpfs)
}

// FromRoot puts everything under one directory (portable mode, tests).
func FromRoot(root string) Paths {
	return Paths{
		Config:  root,
		Data:    filepath.Join(root, "data"),
		Logs:    filepath.Join(root, "logs"),
		Secrets: filepath.Join(root, "secrets"),
		Run:     filepath.Join(root, "run"),
	}
}

// Default returns the standard locations for app (e.g. "splitter").
// It fails if app is not a plain name, the OS is unsupported, or the base location is unknown.
func Default(app string) (Paths, error) {
	return resolve(app, osProbe{})
}

// Ensure creates missing directories with owner-only permissions (0700 for Secrets, 0750 for the rest).
// On Windows it then restricts them to SYSTEM and Administrators; if that step fails, the directories
// still exist and the returned error wraps ErrACL.
func (p Paths) Ensure() error {
	return p.ensure(restrictACL)
}

func (p Paths) ensure(acl func(dir string) error) error {
	dirs := []struct {
		path string
		mode os.FileMode
	}{
		{p.Config, 0o750}, {p.Data, 0o750}, {p.Logs, 0o750}, {p.Run, 0o750}, {p.Secrets, 0o700},
	}
	var created []string
	for _, d := range dirs {
		if d.path == "" {
			continue
		}
		if err := os.MkdirAll(d.path, d.mode); err != nil {
			return fmt.Errorf("paths: create %s: %w", d.path, err)
		}
		if err := os.Chmod(d.path, d.mode); err != nil {
			return fmt.Errorf("paths: chmod %s: %w", d.path, err)
		}
		created = append(created, d.path)
	}
	var failed []string
	for _, d := range created {
		if err := acl(d); err != nil {
			failed = append(failed, fmt.Sprintf("%s: %v", d, err))
		}
	}
	if len(failed) > 0 {
		return fmt.Errorf("%w: %s", ErrACL, strings.Join(failed, "; "))
	}
	return nil
}
