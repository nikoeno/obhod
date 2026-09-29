package proc

import (
	"context"
	"errors"
	"testing"
	"testing/fstest"
)

func TestFind(t *testing.T) {
	l := NewFake(
		Process{PID: 1, Name: "winws"}, Process{PID: 2, Name: "chrome"}, Process{PID: 3, Name: "Sing-Box.exe"},
		Process{PID: 4, Name: "winws", PPID: 1})
	got, err := Find(context.Background(), l, "WINWS.EXE", "sing-box")
	if err != nil || len(got) != 3 || got[0].PID != 1 || got[1].PID != 3 || got[2].PID != 4 {
		t.Fatalf("%+v %v", got, err)
	}
	if got, _ := Find(context.Background(), l); len(got) != 0 {
		t.Fatalf("no names must match nothing: %+v", got)
	}
}

func TestFakeCancelled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := NewFake().List(ctx); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
}

func TestParseStat(t *testing.T) {
	cases := []struct {
		in   string
		comm string
		ppid int
		ok   bool
	}{
		{"42 (nfqws) S 1 42 42 0 -1 4194560 100 0 0 0", "nfqws", 1, true},
		{"7 (Web Content (x)) R 3 7 7 0", "Web Content (x)", 3, true},
		{"garbage", "", 0, false},
		{"1 (x) S", "", 0, false},
		{"1 (x) S notanumber 1", "", 0, false},
	}
	for _, c := range cases {
		comm, ppid, ok := parseStat(c.in)
		if comm != c.comm || ppid != c.ppid || ok != c.ok {
			t.Errorf("%q: %q %d %v", c.in, comm, ppid, ok)
		}
	}
}

func TestProcFS(t *testing.T) {
	fsys := fstest.MapFS{
		"1/stat":    {Data: []byte("1 (systemd) S 0 1 1 0")},
		"42/stat":   {Data: []byte("42 (nfqws) S 1 42 42 0")},
		"43/stat":   {Data: []byte("43 (A B) S 42 43 43 0")},
		"44/stat":   {Data: []byte("broken")},
		"45/other":  {Data: []byte("no stat file")},
		"self/stat": {Data: []byte("9 (self) S 1 9 9 0")}, // not a pid
		"cpuinfo":   {Data: []byte("x")},
		"46/stat":   {Data: []byte("46 (Sing-Box) S 1 46 46 0")},
	}
	got, err := procFS{root: fsys}.List(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	names := map[int]string{}
	for _, p := range got {
		names[p.PID] = p.Name
	}
	want := map[int]string{1: "systemd", 42: "nfqws", 43: "a b", 46: "sing-box"}
	if len(names) != len(want) {
		t.Fatalf("%v", names)
	}
	for pid, n := range want {
		if names[pid] != n {
			t.Errorf("pid %d: %q, want %q", pid, names[pid], n)
		}
	}
	found, _ := Find(context.Background(), procFS{root: fsys}, "nfqws")
	if len(found) != 1 || found[0].PID != 42 || found[0].PPID != 1 {
		t.Fatalf("%+v", found)
	}
}

func TestSystemLists(t *testing.T) {
	ps, err := System().List(context.Background())
	if errors.Is(err, ErrUnsupported) {
		t.Skip("unsupported OS")
	}
	if err != nil || len(ps) == 0 {
		t.Fatalf("%d processes, %v", len(ps), err)
	}
	for _, p := range ps {
		if p.Name != normalize(p.Name) {
			t.Fatalf("name %q is not normalized", p.Name)
		}
	}
}
