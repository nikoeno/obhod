package route

import (
	"errors"
	"reflect"
	"testing"
	"time"
)

var t0 = time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)

func mustTable(t *testing.T, es ...Entry) Table {
	t.Helper()
	tb, err := NewTable("net", t0, es)
	if err != nil {
		t.Fatal(err)
	}
	return tb
}

func TestSourceValid(t *testing.T) {
	for _, s := range []Source{SourceStatic, SourceChecked, SourceDefault} {
		if !s.Valid() {
			t.Errorf("%q should be valid", s)
		}
	}
	for _, s := range []Source{"", "other"} {
		if s.Valid() {
			t.Errorf("%q should be invalid", s)
		}
	}
}

func TestNewTable(t *testing.T) {
	tests := []struct {
		name string
		in   []Entry
		want []Entry
	}{
		{"empty", nil, []Entry{}},
		{"sorted and normalized",
			[]Entry{{"B.com.", Tunnel, SourceChecked}, {"a.com", Direct, SourceDefault}, {"пример.рф", Desync, SourceChecked}},
			[]Entry{{"a.com", Direct, SourceDefault}, {"b.com", Tunnel, SourceChecked}, {"xn--e1afmkfd.xn--p1ai", Desync, SourceChecked}}},
		{"last wins",
			[]Entry{{"a.com", Direct, SourceChecked}, {"A.com", Tunnel, SourceChecked}},
			[]Entry{{"a.com", Tunnel, SourceChecked}}},
		{"static not overridden",
			[]Entry{{"a.com", Block, SourceStatic}, {"a.com", Direct, SourceChecked}, {"a.com", Direct, SourceDefault}},
			[]Entry{{"a.com", Block, SourceStatic}}},
		{"static overrides earlier non-static",
			[]Entry{{"a.com", Direct, SourceChecked}, {"a.com", Proxy, SourceStatic}},
			[]Entry{{"a.com", Proxy, SourceStatic}}},
		{"last static wins over static",
			[]Entry{{"a.com", Proxy, SourceStatic}, {"a.com", Block, SourceStatic}},
			[]Entry{{"a.com", Block, SourceStatic}}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			tb, err := NewTable("net", t0, tc.in)
			if err != nil {
				t.Fatal(err)
			}
			if tb.NetworkID != "net" || !tb.Generated.Equal(t0) {
				t.Errorf("metadata lost: %+v", tb)
			}
			if !reflect.DeepEqual(tb.Entries, tc.want) {
				t.Errorf("got %v, want %v", tb.Entries, tc.want)
			}
		})
	}
}

func TestNewTableInvalid(t *testing.T) {
	tests := []struct {
		name string
		in   Entry
		want error
	}{
		{"route", Entry{"a.com", "nope", SourceChecked}, ErrInvalidRoute},
		{"empty route", Entry{"a.com", "", SourceChecked}, ErrInvalidRoute},
		{"source", Entry{"a.com", Direct, "nope"}, ErrInvalidRoute},
		{"empty source", Entry{"a.com", Direct, ""}, ErrInvalidRoute},
		{"domain", Entry{"*.a.com", Direct, SourceChecked}, ErrInvalidDomain},
		{"ip", Entry{"1.2.3.4", Direct, SourceChecked}, ErrInvalidDomain},
		{"empty domain", Entry{"", Direct, SourceChecked}, ErrInvalidDomain},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := NewTable("net", t0, []Entry{{"ok.com", Direct, SourceChecked}, tc.in})
			if !errors.Is(err, tc.want) {
				t.Fatalf("got %v, want %v", err, tc.want)
			}
		})
	}
}

func TestNewTableCopiesInput(t *testing.T) {
	in := []Entry{{"a.com", Direct, SourceChecked}, {"b.com", Tunnel, SourceChecked}}
	tb := mustTable(t, in...)
	in[0].Route = Block
	in[1].Domain = "evil.com"
	want := []Entry{{"a.com", Direct, SourceChecked}, {"b.com", Tunnel, SourceChecked}}
	if !reflect.DeepEqual(tb.Entries, want) {
		t.Fatalf("table changed with input: %v", tb.Entries)
	}
}

func TestLookup(t *testing.T) {
	tb := mustTable(t,
		Entry{"example.com", Direct, SourceChecked},
		Entry{"b.example.com", Tunnel, SourceChecked},
		Entry{"xn--e1afmkfd.xn--p1ai", Desync, SourceChecked},
	)
	tests := []struct {
		in   string
		want Route
		ok   bool
	}{
		{"example.com", Direct, true},
		{"EXAMPLE.com.", Direct, true},
		{"www.example.com", Direct, true},
		{"a.b.example.com", Tunnel, true},
		{"b.example.com", Tunnel, true},
		{"x.a.b.example.com", Tunnel, true},
		{"notexample.com", "", false},
		{"example.com.evil.org", "", false},
		{"com", "", false},
		{"other.org", "", false},
		{"www.пример.рф", Desync, true},
		{"", "", false},
		{"*.example.com", "", false},
		{"1.2.3.4", "", false},
	}
	for _, tc := range tests {
		e, ok := tb.Lookup(tc.in)
		if ok != tc.ok || e.Route != tc.want {
			t.Errorf("Lookup(%q) = %v, %v; want %q, %v", tc.in, e, ok, tc.want, tc.ok)
		}
	}
	if _, ok := (Table{}).Lookup("example.com"); ok {
		t.Error("empty table matched")
	}
}

func TestDomainsCounts(t *testing.T) {
	tb := mustTable(t,
		Entry{"c.com", Tunnel, SourceChecked},
		Entry{"a.com", Tunnel, SourceChecked},
		Entry{"b.com", Direct, SourceDefault},
		Entry{"z.com", Block, SourceStatic},
	)
	if got, want := tb.Domains(Tunnel), []string{"a.com", "c.com"}; !reflect.DeepEqual(got, want) {
		t.Errorf("Domains = %v, want %v", got, want)
	}
	if got := tb.Domains(Proxy); got == nil || len(got) != 0 {
		t.Errorf("Domains(Proxy) = %#v, want empty non-nil", got)
	}
	want := map[Route]int{Tunnel: 2, Direct: 1, Block: 1}
	if got := tb.Counts(); !reflect.DeepEqual(got, want) {
		t.Errorf("Counts = %v, want %v", got, want)
	}
}

func TestEqual(t *testing.T) {
	a := mustTable(t, Entry{"a.com", Direct, SourceChecked})
	b := mustTable(t, Entry{"a.com", Direct, SourceChecked})
	b.Generated = t0.Add(time.Hour)
	if !a.Equal(b) {
		t.Error("Generated must be ignored")
	}
	diffNet := b
	diffNet.NetworkID = "other"
	tests := map[string]Table{
		"network": diffNet,
		"route":   mustTable(t, Entry{"a.com", Tunnel, SourceChecked}),
		"source":  mustTable(t, Entry{"a.com", Direct, SourceDefault}),
		"domain":  mustTable(t, Entry{"b.com", Direct, SourceChecked}),
		"length":  mustTable(t, Entry{"a.com", Direct, SourceChecked}, Entry{"b.com", Direct, SourceChecked}),
	}
	for name, o := range tests {
		if a.Equal(o) {
			t.Errorf("%s: tables should differ", name)
		}
	}
}

func TestDiff(t *testing.T) {
	old := mustTable(t,
		Entry{"same.com", Direct, SourceChecked},
		Entry{"changed.com", Direct, SourceChecked},
		Entry{"removed.com", Tunnel, SourceChecked},
		Entry{"src.com", Direct, SourceDefault},
	)
	nw := mustTable(t,
		Entry{"same.com", Direct, SourceChecked},
		Entry{"changed.com", Tunnel, SourceChecked},
		Entry{"added.com", Desync, SourceChecked},
		Entry{"src.com", Direct, SourceChecked},
	)
	want := []Change{
		{"added.com", "", Desync},
		{"changed.com", Direct, Tunnel},
		{"removed.com", Tunnel, ""},
	}
	if got := Diff(old, nw); !reflect.DeepEqual(got, want) {
		t.Errorf("Diff = %v, want %v", got, want)
	}
	if got := Diff(old, old); len(got) != 0 {
		t.Errorf("Diff of equal tables = %v", got)
	}
	if got := Diff(Table{}, Table{}); len(got) != 0 {
		t.Errorf("Diff of empty tables = %v", got)
	}
	if got, want := Diff(Table{}, nw), 4; len(got) != want {
		t.Errorf("Diff(empty, nw) has %d changes, want %d", len(got), want)
	}
}

func TestMethodsDoNotMutate(t *testing.T) {
	tb := mustTable(t, Entry{"b.com", Direct, SourceChecked}, Entry{"a.com", Tunnel, SourceChecked})
	snap := append([]Entry(nil), tb.Entries...)
	tb.Lookup("x.a.com")
	tb.Domains(Direct)
	tb.Counts()
	tb.Equal(tb)
	Diff(tb, Table{})
	if !reflect.DeepEqual(tb.Entries, snap) {
		t.Fatal("entries mutated")
	}
}
