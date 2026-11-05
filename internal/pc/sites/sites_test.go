package sites

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/nikoejento/obhod/internal/lib/route"
)

var t0 = time.Date(2026, 11, 1, 10, 0, 0, 0, time.UTC)

func open(t *testing.T) (*Store, string) {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "data") // Open creates it: no paths.Ensure on a real system path
	s, err := Open(dir, nil, WithClock(func() time.Time { return t0 }))
	if err != nil {
		t.Fatal(err)
	}
	return s, dir
}

func TestRoundTrip(t *testing.T) {
	s, dir := open(t)
	if got := s.List(); got == nil || len(got) != 0 {
		t.Fatalf("empty store List = %#v, want empty non-nil", got)
	}
	r, created, err := s.Set("Example.COM.", "Tunnel")
	if err != nil || !created || r.Domain != "example.com" || r.Action != Tunnel || !r.AddedAt.Equal(t0) {
		t.Fatalf("Set = %+v %v %v", r, created, err)
	}
	if _, _, err := s.Set("a.example", "block"); err != nil {
		t.Fatal(err)
	}
	s2, err := Open(dir, nil)
	if err != nil {
		t.Fatal(err)
	}
	want := []Rule{{"a.example", Block, t0}, {"example.com", Tunnel, t0}}
	got := s2.List()
	if len(got) != 2 || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("reloaded = %+v, want %+v", got, want)
	}
	es := s2.Entries()
	if len(es) != 2 || es[1] != (route.Entry{Domain: "example.com", Route: route.Tunnel, Source: route.SourceUser}) {
		t.Fatalf("Entries = %+v", es)
	}
}

func TestUpsertKeepsAddedAtAndDeleteRemoves(t *testing.T) {
	s, dir := open(t)
	if _, _, err := s.Set("example.com", "direct"); err != nil {
		t.Fatal(err)
	}
	s.now = func() time.Time { return t0.Add(time.Hour) }
	r, created, err := s.Set("example.com", "desync")
	if err != nil || created || r.Action != Desync || !r.AddedAt.Equal(t0) {
		t.Fatalf("update = %+v %v %v", r, created, err)
	}
	if err := s.Delete("EXAMPLE.com"); err != nil {
		t.Fatal(err)
	}
	if err := s.Delete("example.com"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("second delete = %v", err)
	}
	if err := s.Delete("not a domain"); !errors.Is(err, ErrInvalidDomain) {
		t.Fatalf("delete of garbage = %v", err)
	}
	s2, _ := Open(dir, nil)
	if len(s2.List()) != 0 {
		t.Fatalf("deleted rule came back: %+v", s2.List())
	}
}

func TestNormalization(t *testing.T) {
	good := map[string]string{
		"Example.com":        "example.com",
		" example.com. ":     "example.com",
		"*.example.com":      "example.com",
		".example.com":       "example.com",
		"пример.рф":          "xn--e1afmkfd.xn--p1ai",
		"sub.Example.co.uk.": "sub.example.co.uk",
	}
	for in, want := range good {
		if got, err := NormalizeDomain(in); err != nil || got != want {
			t.Errorf("NormalizeDomain(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
	for _, in := range []string{"", " ", "https://example.com", "example.com/path", "example.com:443", "1.2.3.4", "a.*.com", "exa mple.com", "-a.com", "a..com", "*.", "."} {
		if _, err := NormalizeDomain(in); !errors.Is(err, ErrInvalidDomain) {
			t.Errorf("NormalizeDomain(%q) = %v, want ErrInvalidDomain", in, err)
		} else if in != "" && strings.TrimSpace(in) != "" && strings.Contains(err.Error(), in) {
			t.Errorf("error repeats the input: %v", err)
		}
	}
}

func TestActions(t *testing.T) {
	s, _ := open(t)
	for _, a := range []string{"direct", "DESYNC", " tunnel ", "block"} {
		if _, _, err := s.Set("a.example", a); err != nil {
			t.Errorf("action %q: %v", a, err)
		}
	}
	for _, a := range []string{"", "none", "proxy", "allow", "tunnel,direct"} {
		if _, _, err := s.Set("b.example", a); !errors.Is(err, ErrInvalidAction) {
			t.Errorf("action %q = %v, want ErrInvalidAction", a, err)
		}
	}
	if len(s.List()) != 1 {
		t.Fatalf("a rejected action stored something: %+v", s.List())
	}
	for _, a := range Actions() {
		if !a.Route().Valid() {
			t.Errorf("%s is not a route", a)
		}
	}
}

func TestLimit(t *testing.T) {
	s, _ := open(t)
	for i := 0; i < MaxRules; i++ {
		if _, _, err := s.Set(fmt.Sprintf("d%d.example", i), "direct"); err != nil {
			t.Fatalf("rule %d: %v", i, err)
		}
	}
	if _, _, err := s.Set("one-too-many.example", "direct"); !errors.Is(err, ErrLimit) {
		t.Fatalf("501st = %v, want ErrLimit", err)
	}
	if _, created, err := s.Set("d7.example", "block"); err != nil || created {
		t.Fatalf("update at the limit = %v %v", created, err)
	}
	if err := s.Delete("d0.example"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.Set("one-too-many.example", "direct"); err != nil {
		t.Fatalf("after a delete: %v", err)
	}
}

func TestFileIsPrivateAndWrittenAtomically(t *testing.T) {
	s, dir := open(t)
	if _, _, err := s.Set("example.com", "tunnel"); err != nil {
		t.Fatal(err)
	}
	ents, _ := os.ReadDir(dir)
	for _, e := range ents {
		if strings.Contains(e.Name(), ".tmp") {
			t.Errorf("leftover temporary file %s", e.Name())
		}
	}
	if runtime.GOOS != "windows" {
		fi, err := os.Stat(filepath.Join(dir, FileName))
		if err != nil || fi.Mode().Perm() != 0o600 {
			t.Errorf("mode = %v %v, want 0600", fi.Mode(), err)
		}
	}
}

// recFS records how the file is written and can fail the write.
type recFS struct {
	FS
	failWrite bool
	perm      fs.FileMode
	writes    int
}

func (r *recFS) WriteFileAtomic(path string, data []byte, perm fs.FileMode) error {
	if r.failWrite {
		return errors.New("disk full")
	}
	r.writes++
	r.perm = perm
	return r.FS.WriteFileAtomic(path, data, perm)
}

func TestWriteFailureChangesNothing(t *testing.T) {
	dir := t.TempDir()
	rf := &recFS{FS: OSFS()}
	s, err := Open(dir, rf)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.Set("a.example", "direct"); err != nil {
		t.Fatal(err)
	}
	if rf.perm != 0o600 {
		t.Errorf("write perm = %v, want 0600", rf.perm)
	}
	rf.failWrite = true
	if _, _, err := s.Set("b.example", "block"); err == nil {
		t.Fatal("write failure not reported")
	}
	if _, _, err := s.Set("a.example", "block"); err == nil {
		t.Fatal("write failure not reported")
	}
	if err := s.Delete("a.example"); err == nil {
		t.Fatal("write failure not reported")
	}
	got := s.List()
	if len(got) != 1 || got[0].Domain != "a.example" || got[0].Action != Direct {
		t.Fatalf("memory changed after failed writes: %+v", got)
	}
}

func TestCorruptFileIsMovedAsideAndStartsEmpty(t *testing.T) {
	good := `{"version":1,"rules":[{"domain":"a.example","action":"block","addedAt":"2026-11-01T10:00:00Z"}]}`
	cases := map[string]string{
		"not json":        "{{{",
		"unknown version": `{"version":2,"rules":[]}`,
		"unknown field":   `{"version":1,"rules":[],"extra":1}`,
		"bad domain":      `{"version":1,"rules":[{"domain":"Not Valid","action":"block","addedAt":"2026-11-01T10:00:00Z"}]}`,
		"not normalized":  `{"version":1,"rules":[{"domain":"A.example","action":"block","addedAt":"2026-11-01T10:00:00Z"}]}`,
		"bad action":      `{"version":1,"rules":[{"domain":"a.example","action":"proxy","addedAt":"2026-11-01T10:00:00Z"}]}`,
		"duplicate":       `{"version":1,"rules":[{"domain":"a.example","action":"block","addedAt":"2026-11-01T10:00:00Z"},{"domain":"a.example","action":"direct","addedAt":"2026-11-01T10:00:00Z"}]}`,
	}
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			if err := os.WriteFile(filepath.Join(dir, FileName), []byte(body), 0o600); err != nil {
				t.Fatal(err)
			}
			s, err := Open(dir, nil)
			if err != nil {
				t.Fatal(err)
			}
			if !s.Recovered() || len(s.List()) != 0 {
				t.Fatalf("recovered=%v rules=%+v", s.Recovered(), s.List())
			}
			if b, err := os.ReadFile(filepath.Join(dir, CorruptName)); err != nil || string(b) != body {
				t.Errorf("the unreadable file was not kept: %v", err)
			}
			if _, err := os.Stat(filepath.Join(dir, FileName)); !errors.Is(err, fs.ErrNotExist) {
				t.Errorf("the unreadable file is still in place")
			}
			if _, _, err := s.Set("c.example", "direct"); err != nil {
				t.Errorf("store unusable after recovery: %v", err)
			}
		})
	}
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, FileName), []byte(good), 0o600)
	if s, err := Open(dir, nil); err != nil || s.Recovered() || len(s.List()) != 1 {
		t.Fatalf("valid file: %v %v", s, err)
	}
}
