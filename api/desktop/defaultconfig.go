package desktop

import (
	"embed"
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

// defaultListsDir is the folder, next to app.yaml, that holds the starter lists.
const defaultListsDir = "lists"

// defaultListNames are the starter list files, in the order they are written.
var defaultListNames = []string{"candidates.txt", "proxy.txt", "block.txt"}

//go:embed defaultlists/candidates.txt defaultlists/proxy.txt defaultlists/block.txt
var defaultLists embed.FS

// defaultConfigYAML is the app.yaml written on the first `run --ui` start. Every
// key but checker.lists stays at its built-in default. It holds no secret and no
// server address (servers are added through the control API).
const defaultConfigYAML = `# splitter configuration, created on first start.
# Every key that is not set here has a built-in default. Unknown keys are an error.
# Servers are managed from the app; nothing secret belongs in this file.

# Domain lists. The checker probes the domains in "candidates" on your connection
# and decides for each: direct, desync or tunnel. The files are plain text, one
# domain per line, '#' for comments; a domain also covers its subdomains.
#   candidates.txt  starter set of services commonly blocked or throttled in Russia
#   proxy.txt       always through the tunnel, never probed (empty)
#   block.txt       always refused (empty)
# Relative paths are relative to the folder of this file. Edit the files, add
# more files to a list, or point to your own (absolute paths work too).
# The lists are read once, when splitter starts: restart the app after a change.
# These files are created only when missing and are never overwritten.
checker:
  lists:
    candidates: [lists/candidates.txt]
    proxy: [lists/proxy.txt]
    block: [lists/block.txt]

# paths:
#   root: ""        # put all directories under one folder (portable mode)
# log:
#   level: info     # debug | info | warn | error
`

// writeDefaultLists writes the embedded starter lists to <configDir>/lists. A
// file that already exists is left alone, so user edits survive.
func writeDefaultLists(configDir string) error {
	dir := filepath.Join(configDir, defaultListsDir)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	for _, name := range defaultListNames {
		data, err := defaultLists.ReadFile("defaultlists/" + name)
		if err != nil {
			return err
		}
		if err := writeIfMissing(filepath.Join(dir, name), data); err != nil {
			return err
		}
	}
	return nil
}

// writeDefaultConfig writes the default app.yaml to path unless a file is
// already there.
func writeDefaultConfig(path string) error {
	return writeIfMissing(path, []byte(defaultConfigYAML))
}

// writeIfMissing writes data to path unless a file is already there. It never
// overwrites: the data goes to a private temporary file that is linked into
// place, which fails if path exists.
func writeIfMissing(path string, data []byte) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), filepath.Base(path)+".tmp*")
	if err != nil {
		return err
	}
	name := tmp.Name()
	defer os.Remove(name)
	// CreateTemp makes the file 0600 (owner-only) already.
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	err = os.Link(name, path)
	switch {
	case err == nil, errors.Is(err, os.ErrExist):
		return nil
	}
	// No hard links on this file system: fall back to a rename after a check.
	if _, serr := os.Lstat(path); serr == nil {
		return nil
	}
	if rerr := os.Rename(name, path); rerr != nil {
		return fmt.Errorf("%w", rerr)
	}
	return nil
}
