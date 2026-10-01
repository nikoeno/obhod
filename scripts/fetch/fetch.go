package main

import (
	"archive/tar"
	"archive/zip"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"strings"
)

const (
	maxArchiveBytes = 1 << 30   // 1 GiB per download
	maxFileBytes    = 512 << 20 // 512 MiB per extracted file
)

type fetcher struct {
	client   *http.Client
	cacheDir string // downloaded archives, named by their sha256
	outDir   string // third_party/bin
	log      io.Writer
}

// verify checks the installed files of j without touching the network.
func (f *fetcher) verify(j job) error {
	var bad []string
	for _, file := range j.Spec.Files {
		if err := checkFile(f.target(j, file), file.SHA256); err != nil {
			bad = append(bad, fmt.Sprintf("%s: %v", file.To, err))
		}
	}
	if len(bad) > 0 {
		return errors.New(strings.Join(bad, "; "))
	}
	fmt.Fprintf(f.log, "ok        %s %s %s\n", j.Name, j.Version, j.Platform)
	return nil
}

// install makes the installed files of j match the manifest, downloading the
// archive only when a file is missing or differs.
func (f *fetcher) install(ctx context.Context, j job) error {
	var todo []File
	for _, file := range j.Spec.Files {
		if checkFile(f.target(j, file), file.SHA256) != nil {
			todo = append(todo, file)
		}
	}
	if len(todo) == 0 {
		fmt.Fprintf(f.log, "current   %s %s %s\n", j.Name, j.Version, j.Platform)
		return nil
	}

	archive, err := f.archive(ctx, j)
	if err != nil {
		return err
	}
	if err := f.extract(archive, archiveKind(j.Spec.URL), j, todo); err != nil {
		return err
	}
	fmt.Fprintf(f.log, "installed %s %s %s (%d files)\n", j.Name, j.Version, j.Platform, len(todo))
	return nil
}

func (f *fetcher) target(j job, file File) string {
	return filepath.Join(f.outDir, j.Platform, filepath.FromSlash(file.To))
}

// archive returns the path of the verified archive for j, downloading it into
// the cache unless a copy with the right sha256 is already there.
func (f *fetcher) archive(ctx context.Context, j job) (string, error) {
	want := j.Spec.SHA256
	cached := filepath.Join(f.cacheDir, want+"."+strings.ReplaceAll(archiveKind(j.Spec.URL), ".", "-"))
	if checkFile(cached, want) == nil {
		return cached, nil
	}
	if err := os.MkdirAll(f.cacheDir, 0o755); err != nil {
		return "", err
	}

	fmt.Fprintf(f.log, "download  %s\n", j.Spec.URL)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, j.Spec.URL, nil)
	if err != nil {
		return "", err
	}
	resp, err := f.client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("download: HTTP %d", resp.StatusCode)
	}

	tmp, err := os.CreateTemp(f.cacheDir, "download-*.tmp")
	if err != nil {
		return "", err
	}
	defer os.Remove(tmp.Name()) // no-op after a successful rename

	h := sha256.New()
	n, err := io.Copy(io.MultiWriter(tmp, h), io.LimitReader(resp.Body, maxArchiveBytes+1))
	if cerr := tmp.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return "", fmt.Errorf("download: %w", err)
	}
	if n > maxArchiveBytes {
		return "", fmt.Errorf("download: larger than %d bytes", maxArchiveBytes)
	}
	if got := hex.EncodeToString(h.Sum(nil)); got != want {
		return "", fmt.Errorf("archive sha256 mismatch: got %s, manifest says %s", got, want)
	}
	if err := os.Rename(tmp.Name(), cached); err != nil {
		return "", err
	}
	return cached, nil
}

// extract installs the files in todo from the archive. Each file is written to
// a temporary name, checked, and only then renamed into place.
func (f *fetcher) extract(archive, kind string, j job, todo []File) error {
	want := map[string]File{}
	for _, file := range todo {
		want[file.From] = file
	}
	done := map[string]bool{}

	put := func(name string, mode os.FileMode, r io.Reader) error {
		file, ok := want[name]
		if !ok || done[name] {
			return nil
		}
		if err := f.write(f.target(j, file), file.SHA256, fileMode(j.Platform, file.To, mode), r); err != nil {
			return fmt.Errorf("%s: %w", file.To, err)
		}
		done[name] = true
		return nil
	}

	var err error
	switch kind {
	case "zip":
		err = walkZip(archive, put)
	case "tar.gz":
		err = walkTarGz(archive, put)
	default:
		err = fmt.Errorf("unsupported archive type %q", kind)
	}
	if err != nil {
		return err
	}

	var missing []string
	for _, file := range todo {
		if !done[file.From] {
			missing = append(missing, file.From)
		}
	}
	if len(missing) > 0 {
		return fmt.Errorf("not found in archive: %s", strings.Join(missing, ", "))
	}
	return nil
}

func (f *fetcher) write(dst, wantSHA string, mode os.FileMode, r io.Reader) error {
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(dst), ".fetch-*.tmp")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name()) // no-op after a successful rename

	h := sha256.New()
	n, err := io.Copy(io.MultiWriter(tmp, h), io.LimitReader(r, maxFileBytes+1))
	if cerr := tmp.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return err
	}
	if n > maxFileBytes {
		return fmt.Errorf("larger than %d bytes", maxFileBytes)
	}
	if got := hex.EncodeToString(h.Sum(nil)); got != wantSHA {
		return fmt.Errorf("sha256 mismatch: got %s, manifest says %s", got, wantSHA)
	}
	if err := os.Chmod(tmp.Name(), mode); err != nil {
		return err
	}
	// Windows cannot rename over an existing file that is in use; remove first.
	if err := os.Remove(dst); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return os.Rename(tmp.Name(), dst)
}

// fileMode keeps the archive's executable bit and also marks extension-less
// files for non-Windows platforms executable (zip archives often drop modes).
func fileMode(platform, to string, archived os.FileMode) os.FileMode {
	if archived&0o111 != 0 {
		return 0o755
	}
	if !strings.HasPrefix(platform, "windows-") && path.Ext(to) == "" && !strings.HasPrefix(path.Base(to), "LICENSE") {
		return 0o755
	}
	return 0o644
}

func walkZip(name string, put func(string, os.FileMode, io.Reader) error) error {
	zr, err := zip.OpenReader(name)
	if err != nil {
		return err
	}
	defer zr.Close()
	for _, zf := range zr.File {
		if zf.FileInfo().IsDir() {
			continue
		}
		rc, err := zf.Open()
		if err != nil {
			return err
		}
		err = put(zf.Name, zf.Mode(), rc)
		rc.Close()
		if err != nil {
			return err
		}
	}
	return nil
}

func walkTarGz(name string, put func(string, os.FileMode, io.Reader) error) error {
	fh, err := os.Open(name)
	if err != nil {
		return err
	}
	defer fh.Close()
	gz, err := gzip.NewReader(fh)
	if err != nil {
		return err
	}
	defer gz.Close()
	tr := tar.NewReader(gz)
	for {
		hdr, err := tr.Next()
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return err
		}
		if hdr.Typeflag != tar.TypeReg {
			continue
		}
		if err := put(strings.TrimPrefix(hdr.Name, "./"), hdr.FileInfo().Mode(), tr); err != nil {
			return err
		}
	}
}

// checkFile returns nil if name exists and has the given sha256.
func checkFile(name, wantSHA string) error {
	fh, err := os.Open(name)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return errors.New("missing")
		}
		return err
	}
	defer fh.Close()
	h := sha256.New()
	if _, err := io.Copy(h, fh); err != nil {
		return err
	}
	if got := hex.EncodeToString(h.Sum(nil)); got != wantSHA {
		return fmt.Errorf("sha256 mismatch: got %s", got)
	}
	return nil
}
