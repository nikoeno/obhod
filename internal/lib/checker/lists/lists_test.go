package lists

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/nikoejento/obhod/internal/lib/route"
)

func TestParseFixture(t *testing.T) {
	f, err := os.Open("testdata/mixed.txt")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	got, warns, err := Parse(f, "mixed.txt")
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"example.com", "wild.org", "dot.net", "xn--bcher-kva.de"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("domains = %v, want %v", got, want)
	}
	for i, line := range []string{"6", "7", "10"} {
		if i >= len(warns) || !strings.HasPrefix(warns[i], "mixed.txt:"+line+": ") {
			t.Errorf("warning %d = %v, want line %s", i, warns, line)
		}
	}
	if len(warns) != 3 {
		t.Errorf("warnings = %v", warns)
	}
}

func TestParseLongLine(t *testing.T) {
	long := strings.Repeat("a", 200*1024) + ".com\nok.com\n"
	got, warns, err := Parse(strings.NewReader(long), "x")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0] != "ok.com" || len(warns) != 1 {
		t.Errorf("got %v %v", got, warns)
	}
}

func write(t *testing.T, dir, name, body string) string {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestLoadPrecedence(t *testing.T) {
	dir := t.TempDir()
	cand := write(t, dir, "c.txt", "all.com\nonly-cand.com\nproxied.com\nZ.com\n")
	cand2 := write(t, dir, "c2.txt", "only-cand.com\nbad line here\n")
	proxy := write(t, dir, "p.txt", "all.com\nproxied.com\n")
	block := write(t, dir, "b.txt", "all.com\nblocked.com\n")

	l, err := Load(Config{Candidates: []string{cand, cand2}, Proxy: []string{proxy}, Block: []string{block}})
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"only-cand.com", "z.com"}; !reflect.DeepEqual(l.Candidates, want) {
		t.Errorf("candidates = %v, want %v", l.Candidates, want)
	}
	want := []route.Entry{
		{Domain: "all.com", Route: route.Block, Source: route.SourceStatic},
		{Domain: "blocked.com", Route: route.Block, Source: route.SourceStatic},
		{Domain: "proxied.com", Route: route.Proxy, Source: route.SourceStatic},
	}
	if !reflect.DeepEqual(l.Static, want) {
		t.Errorf("static = %v, want %v", l.Static, want)
	}
	if len(l.Warnings) != 1 || !strings.Contains(l.Warnings[0], "c2.txt:2:") {
		t.Errorf("warnings = %v", l.Warnings)
	}
}

func TestLoadMissingFile(t *testing.T) {
	if _, err := Load(Config{Candidates: []string{filepath.Join(t.TempDir(), "nope.txt")}}); err == nil {
		t.Error("want error for a missing file")
	}
}

func TestLoadEmpty(t *testing.T) {
	l, err := Load(Config{})
	if err != nil || len(l.Candidates) != 0 || len(l.Static) != 0 {
		t.Errorf("got %+v, %v", l, err)
	}
}
