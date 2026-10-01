package main

import (
	"bytes"
	"encoding/hex"
	"fmt"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

// Manifest is third_party/manifest.yaml.
type Manifest struct {
	Binaries []Binary `yaml:"binaries"`
}

// Binary is one pinned third-party tool.
type Binary struct {
	Name      string              `yaml:"name"`
	Version   string              `yaml:"version"`
	License   string              `yaml:"license"`
	Source    string              `yaml:"source"`
	Platforms map[string]Platform `yaml:"platforms"`
}

// Platform is the archive for one <os>-<arch> and the files taken from it.
type Platform struct {
	URL    string `yaml:"url"`
	SHA256 string `yaml:"sha256"`
	Files  []File `yaml:"files"`
}

// File is one archive member installed into third_party/bin/<platform>/.
type File struct {
	From   string `yaml:"from"`
	To     string `yaml:"to"`
	SHA256 string `yaml:"sha256"`
}

func loadManifest(name string) (Manifest, error) {
	data, err := os.ReadFile(name)
	if err != nil {
		return Manifest{}, err
	}
	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true)
	var m Manifest
	if err := dec.Decode(&m); err != nil {
		return Manifest{}, fmt.Errorf("%s: %w", name, err)
	}
	if err := m.validate(); err != nil {
		return Manifest{}, fmt.Errorf("%s: %w", name, err)
	}
	return m, nil
}

func (m Manifest) validate() error {
	seen := map[string]bool{}
	for _, b := range m.Binaries {
		if b.Name == "" || b.Version == "" {
			return fmt.Errorf("binary %q: name and version are required", b.Name)
		}
		if seen[b.Name] {
			return fmt.Errorf("binary %q listed twice", b.Name)
		}
		seen[b.Name] = true
		if len(b.Platforms) == 0 {
			return fmt.Errorf("binary %q: no platforms", b.Name)
		}
		for _, pn := range sortedKeys(b.Platforms) {
			p := b.Platforms[pn]
			where := b.Name + " " + pn
			if !validPlatform(pn) {
				return fmt.Errorf("%s: platform must look like <os>-<arch>", where)
			}
			u, err := url.Parse(p.URL)
			if err != nil || u.Scheme != "https" || u.Host == "" {
				return fmt.Errorf("%s: url must be an https URL", where)
			}
			if !validSHA256(p.SHA256) {
				return fmt.Errorf("%s: archive sha256 must be 64 hex characters", where)
			}
			if archiveKind(p.URL) == "" {
				return fmt.Errorf("%s: unsupported archive type (want .zip or .tar.gz)", where)
			}
			if len(p.Files) == 0 {
				return fmt.Errorf("%s: no files", where)
			}
			targets := map[string]bool{}
			for _, f := range p.Files {
				if f.From == "" || path.IsAbs(f.From) {
					return fmt.Errorf("%s: file %q: from must be a relative archive path", where, f.From)
				}
				if !localPath(f.To) {
					return fmt.Errorf("%s: file %q: to must be a relative path inside the platform directory", where, f.To)
				}
				if targets[f.To] {
					return fmt.Errorf("%s: file %q listed twice", where, f.To)
				}
				targets[f.To] = true
				if !validSHA256(f.SHA256) {
					return fmt.Errorf("%s: file %q: sha256 must be 64 hex characters", where, f.To)
				}
			}
		}
	}
	return nil
}

func validPlatform(s string) bool {
	osName, arch, ok := strings.Cut(s, "-")
	return ok && osName != "" && arch != "" && !strings.ContainsAny(s, `/\. `)
}

func validSHA256(s string) bool {
	b, err := hex.DecodeString(s)
	return err == nil && len(b) == 32 && s == strings.ToLower(s)
}

// localPath reports whether p (slash-separated) stays inside its base directory.
func localPath(p string) bool {
	return p != "" && !strings.Contains(p, `\`) && filepath.IsLocal(filepath.FromSlash(p))
}

// archiveKind returns "zip", "tar.gz" or "" for the URL's file name.
func archiveKind(rawURL string) string {
	u, err := url.Parse(rawURL)
	if err != nil {
		return ""
	}
	switch name := strings.ToLower(path.Base(u.Path)); {
	case strings.HasSuffix(name, ".zip"):
		return "zip"
	case strings.HasSuffix(name, ".tar.gz"), strings.HasSuffix(name, ".tgz"):
		return "tar.gz"
	}
	return ""
}

func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
