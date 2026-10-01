package main

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
)

func sum(b []byte) string {
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}

func makeZip(t *testing.T, files map[string]string) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for _, name := range sortedKeys(files) {
		w, err := zw.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		io.WriteString(w, files[name])
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func makeTarGz(t *testing.T, files map[string]string, mode int64) []byte {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	for _, name := range sortedKeys(files) {
		body := files[name]
		if err := tw.WriteHeader(&tar.Header{Name: name, Mode: mode, Size: int64(len(body)), Typeflag: tar.TypeReg}); err != nil {
			t.Fatal(err)
		}
		io.WriteString(tw, body)
	}
	tw.Close()
	gz.Close()
	return buf.Bytes()
}

// server serves archives by path and counts requests.
type server struct {
	*httptest.Server
	hits atomic.Int32
}

func newServer(t *testing.T, archives map[string][]byte) *server {
	t.Helper()
	s := &server{}
	s.Server = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.hits.Add(1)
		b, ok := archives[r.URL.Path]
		if !ok {
			http.NotFound(w, r)
			return
		}
		w.Write(b)
	}))
	t.Cleanup(s.Close)
	return s
}

func newFetcher(t *testing.T, s *server) (*fetcher, *bytes.Buffer) {
	t.Helper()
	var log bytes.Buffer
	dir := t.TempDir()
	return &fetcher{
		client:   s.Client(),
		cacheDir: filepath.Join(dir, "cache"),
		outDir:   filepath.Join(dir, "bin"),
		log:      &log,
	}, &log
}

func zipJob(url string, archive []byte, files ...File) job {
	return job{Name: "tool", Version: "1.0", Platform: "linux-amd64",
		Spec: Platform{URL: url, SHA256: sum(archive), Files: files}}
}

func readFile(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile(name)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestInstallZip(t *testing.T) {
	archive := makeZip(t, map[string]string{
		"tool-1.0/bin/tool":     "binary",
		"tool-1.0/lua/lib.lua":  "lua",
		"tool-1.0/unused/other": "ignored",
	})
	s := newServer(t, map[string][]byte{"/tool.zip": archive})
	f, _ := newFetcher(t, s)
	j := zipJob(s.URL+"/tool.zip", archive,
		File{From: "tool-1.0/bin/tool", To: "tool", SHA256: sum([]byte("binary"))},
		File{From: "tool-1.0/lua/lib.lua", To: "lua/lib.lua", SHA256: sum([]byte("lua"))},
	)

	if err := f.install(context.Background(), j); err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(f.outDir, "linux-amd64")
	if got := readFile(t, filepath.Join(dir, "tool")); got != "binary" {
		t.Errorf("tool = %q", got)
	}
	if got := readFile(t, filepath.Join(dir, "lua", "lib.lua")); got != "lua" {
		t.Errorf("lib.lua = %q", got)
	}
	if _, err := os.Stat(filepath.Join(dir, "other")); !os.IsNotExist(err) {
		t.Errorf("unlisted file was extracted")
	}
	if runtime.GOOS != "windows" {
		fi, _ := os.Stat(filepath.Join(dir, "tool"))
		if fi.Mode().Perm()&0o111 == 0 {
			t.Errorf("extension-less linux file is not executable: %v", fi.Mode())
		}
	}
	if err := f.verify(j); err != nil {
		t.Errorf("verify after install: %v", err)
	}
}

func TestInstallTarGz(t *testing.T) {
	archive := makeTarGz(t, map[string]string{"./pkg/run": "exe"}, 0o755)
	s := newServer(t, map[string][]byte{"/pkg.tar.gz": archive})
	f, _ := newFetcher(t, s)
	j := zipJob(s.URL+"/pkg.tar.gz", archive, File{From: "pkg/run", To: "run", SHA256: sum([]byte("exe"))})

	if err := f.install(context.Background(), j); err != nil {
		t.Fatal(err)
	}
	if got := readFile(t, filepath.Join(f.outDir, "linux-amd64", "run")); got != "exe" {
		t.Errorf("run = %q", got)
	}
}

func TestInstallSkipsWhenCurrent(t *testing.T) {
	archive := makeZip(t, map[string]string{"a": "A"})
	s := newServer(t, map[string][]byte{"/a.zip": archive})
	f, log := newFetcher(t, s)
	j := zipJob(s.URL+"/a.zip", archive, File{From: "a", To: "a.txt", SHA256: sum([]byte("A"))})

	for range 2 {
		if err := f.install(context.Background(), j); err != nil {
			t.Fatal(err)
		}
	}
	if n := s.hits.Load(); n != 1 {
		t.Errorf("server hit %d times, want 1", n)
	}
	if !strings.Contains(log.String(), "current") {
		t.Errorf("second run did not report current:\n%s", log)
	}
}

func TestInstallUsesCache(t *testing.T) {
	archive := makeZip(t, map[string]string{"a": "A"})
	s := newServer(t, map[string][]byte{"/a.zip": archive})
	f, _ := newFetcher(t, s)
	j := zipJob(s.URL+"/a.zip", archive, File{From: "a", To: "a.txt", SHA256: sum([]byte("A"))})

	if err := f.install(context.Background(), j); err != nil {
		t.Fatal(err)
	}
	os.RemoveAll(f.outDir)
	if err := f.install(context.Background(), j); err != nil {
		t.Fatal(err)
	}
	if n := s.hits.Load(); n != 1 {
		t.Errorf("server hit %d times, want 1 (cache)", n)
	}
}

func TestInstallRepairsChangedFile(t *testing.T) {
	archive := makeZip(t, map[string]string{"a": "A"})
	s := newServer(t, map[string][]byte{"/a.zip": archive})
	f, _ := newFetcher(t, s)
	j := zipJob(s.URL+"/a.zip", archive, File{From: "a", To: "a.txt", SHA256: sum([]byte("A"))})
	if err := f.install(context.Background(), j); err != nil {
		t.Fatal(err)
	}
	dst := filepath.Join(f.outDir, "linux-amd64", "a.txt")
	os.WriteFile(dst, []byte("tampered"), 0o644)

	if err := f.verify(j); err == nil {
		t.Fatal("verify accepted a tampered file")
	}
	if err := f.install(context.Background(), j); err != nil {
		t.Fatal(err)
	}
	if got := readFile(t, dst); got != "A" {
		t.Errorf("a.txt = %q after repair", got)
	}
}

func TestInstallRejectsBadArchiveHash(t *testing.T) {
	archive := makeZip(t, map[string]string{"a": "A"})
	s := newServer(t, map[string][]byte{"/a.zip": archive})
	f, _ := newFetcher(t, s)
	j := zipJob(s.URL+"/a.zip", archive, File{From: "a", To: "a.txt", SHA256: sum([]byte("A"))})
	j.Spec.SHA256 = sum([]byte("something else"))

	err := f.install(context.Background(), j)
	if err == nil || !strings.Contains(err.Error(), "archive sha256 mismatch") {
		t.Fatalf("err = %v", err)
	}
	if _, err := os.Stat(filepath.Join(f.outDir, "linux-amd64", "a.txt")); !os.IsNotExist(err) {
		t.Error("file installed from an unverified archive")
	}
	entries, _ := os.ReadDir(f.cacheDir)
	if len(entries) != 0 {
		t.Errorf("cache keeps a rejected download: %v", entries)
	}
}

func TestInstallRejectsBadFileHash(t *testing.T) {
	archive := makeZip(t, map[string]string{"a": "A"})
	s := newServer(t, map[string][]byte{"/a.zip": archive})
	f, _ := newFetcher(t, s)
	j := zipJob(s.URL+"/a.zip", archive, File{From: "a", To: "a.txt", SHA256: sum([]byte("B"))})

	err := f.install(context.Background(), j)
	if err == nil || !strings.Contains(err.Error(), "sha256 mismatch") {
		t.Fatalf("err = %v", err)
	}
	if _, err := os.Stat(filepath.Join(f.outDir, "linux-amd64", "a.txt")); !os.IsNotExist(err) {
		t.Error("file with a wrong hash was installed")
	}
}

func TestInstallMissingMember(t *testing.T) {
	archive := makeZip(t, map[string]string{"a": "A"})
	s := newServer(t, map[string][]byte{"/a.zip": archive})
	f, _ := newFetcher(t, s)
	j := zipJob(s.URL+"/a.zip", archive, File{From: "nope", To: "nope", SHA256: sum([]byte("A"))})

	err := f.install(context.Background(), j)
	if err == nil || !strings.Contains(err.Error(), "not found in archive: nope") {
		t.Fatalf("err = %v", err)
	}
}

func TestInstallHTTPError(t *testing.T) {
	archive := makeZip(t, map[string]string{"a": "A"})
	s := newServer(t, nil)
	f, _ := newFetcher(t, s)
	j := zipJob(s.URL+"/missing.zip", archive, File{From: "a", To: "a", SHA256: sum([]byte("A"))})

	if err := f.install(context.Background(), j); err == nil || !strings.Contains(err.Error(), "HTTP 404") {
		t.Fatalf("err = %v", err)
	}
}

func TestManifestValidate(t *testing.T) {
	good := func() Manifest {
		return Manifest{Binaries: []Binary{{
			Name: "tool", Version: "1",
			Platforms: map[string]Platform{"linux-amd64": {
				URL: "https://example.invalid/t.zip", SHA256: sum(nil),
				Files: []File{{From: "t/tool", To: "tool", SHA256: sum(nil)}},
			}},
		}}}
	}
	if err := good().validate(); err != nil {
		t.Fatalf("good manifest: %v", err)
	}

	cases := map[string]func(*Platform){
		"http url":     func(p *Platform) { p.URL = "http://example.invalid/t.zip" },
		"unknown type": func(p *Platform) { p.URL = "https://example.invalid/t.7z" },
		"short sha":    func(p *Platform) { p.SHA256 = "abc" },
		"upper sha":    func(p *Platform) { p.SHA256 = strings.ToUpper(sum(nil)) },
		"no files":     func(p *Platform) { p.Files = nil },
		"to escapes":   func(p *Platform) { p.Files[0].To = "../evil" },
		"to absolute":  func(p *Platform) { p.Files[0].To = "/etc/evil" },
		"to backslash": func(p *Platform) { p.Files[0].To = `lua\x` },
		"from abs":     func(p *Platform) { p.Files[0].From = "/t/tool" },
		"dup target": func(p *Platform) {
			p.Files = append(p.Files, File{From: "t/x", To: "tool", SHA256: sum(nil)})
		},
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			m := good()
			p := m.Binaries[0].Platforms["linux-amd64"]
			p.Files = append([]File(nil), p.Files...)
			mutate(&p)
			m.Binaries[0].Platforms["linux-amd64"] = p
			if err := m.validate(); err == nil {
				t.Error("accepted")
			}
		})
	}
}

func TestSelectJobs(t *testing.T) {
	m := Manifest{Binaries: []Binary{
		{Name: "a", Platforms: map[string]Platform{"linux-amd64": {}, "windows-amd64": {}}},
		{Name: "b", Platforms: map[string]Platform{"linux-amd64": {}}},
	}}
	count := func(p, o []string) int {
		j, err := selectJobs(m, p, o)
		if err != nil {
			return -1
		}
		return len(j)
	}
	if n := count([]string{"all"}, nil); n != 3 {
		t.Errorf("all = %d", n)
	}
	if n := count([]string{"linux-amd64"}, nil); n != 2 {
		t.Errorf("linux-amd64 = %d", n)
	}
	if n := count([]string{"all"}, []string{"b"}); n != 1 {
		t.Errorf("only b = %d", n)
	}
	if n := count([]string{"darwin-arm64"}, nil); n != -1 {
		t.Errorf("unknown platform not rejected")
	}
	if n := count([]string{"all"}, []string{"zzz"}); n != -1 {
		t.Errorf("unknown binary not rejected")
	}
}

// TestRepoManifest keeps the real manifest loadable.
func TestRepoManifest(t *testing.T) {
	if _, err := loadManifest(filepath.Join("..", "..", "third_party", "manifest.yaml")); err != nil {
		t.Fatal(err)
	}
}
