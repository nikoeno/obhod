package model

import (
	"encoding/json"
	"flag"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/nikoejento/obhod/internal/lib/analyzer"
	"github.com/nikoejento/obhod/internal/lib/route"
)

var update = flag.Bool("update", false, "update golden files")

func TestNetworkIDValid(t *testing.T) {
	cases := map[NetworkID]bool{
		"asn:12389":           true,
		"gw:3f9a0000deadbeef": true,
		"asn:12389/gw:3f9a":   true,
		"":                    false,
		"12389":               false,
		"asn:12 389":          false,
		"asn:1\n":             false,
		"tunnel:1":            false,
		NetworkID("asn:" + strings.Repeat("1", MaxIDLen)): false,
	}
	for id, want := range cases {
		if got := id.Valid(); got != want {
			t.Errorf("Valid(%q) = %v, want %v", id, got, want)
		}
	}
}

func sample() Decision {
	at := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	return Decision{
		Domain: "example.com", Route: route.Desync, Source: route.SourceChecked,
		DecidedAt: at, CheckedAt: at.Add(time.Hour), ExpiresAt: at.Add(25 * time.Hour),
		Pending: &Pending{Route: route.Tunnel, Count: 1, Since: at.Add(time.Hour)},
		Last:    map[route.Route]analyzer.Verdict{route.Direct: "TLS_RST"},
	}
}

func TestJSONRoundTrip(t *testing.T) {
	for name, d := range map[string]Decision{
		"full":       sample(),
		"nilPending": {Domain: "a.com", Route: route.Direct, Source: route.SourceDefault},
		"emptyLast":  {Domain: "a.com", Route: route.Direct, Last: map[route.Route]analyzer.Verdict{}},
	} {
		b, err := json.Marshal(d)
		if err != nil {
			t.Fatal(name, err)
		}
		var got Decision
		if err := json.Unmarshal(b, &got); err != nil {
			t.Fatal(name, err)
		}
		// omitempty turns an empty map into nil.
		if len(d.Last) == 0 {
			d.Last = nil
		}
		if !reflect.DeepEqual(got, d) {
			t.Errorf("%s: round trip mismatch:\n got %+v\nwant %+v", name, got, d)
		}
		if name == "nilPending" && strings.Contains(string(b), "pending") {
			t.Errorf("nil Pending must be omitted: %s", b)
		}
	}
}

func TestGoldenJSON(t *testing.T) {
	b, err := json.MarshalIndent(sample(), "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	b = append(b, '\n')
	const file = "testdata/decision.golden.json"
	if *update {
		if err := os.MkdirAll("testdata", 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(file, b, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	if string(want) != string(b) {
		t.Errorf("golden mismatch:\n got %s\nwant %s", b, want)
	}
}

func TestExpired(t *testing.T) {
	at := time.Unix(1000, 0)
	d := Decision{ExpiresAt: at}
	if d.Expired(at.Add(-time.Nanosecond)) {
		t.Error("expired before ExpiresAt")
	}
	if !d.Expired(at) {
		t.Error("not expired at boundary")
	}
	if (Decision{}).Expired(at) {
		t.Error("zero ExpiresAt must never expire")
	}
}

func TestCloneIsDeep(t *testing.T) {
	d := sample()
	c := d.Clone()
	c.Pending.Count = 9
	c.Last[route.Direct] = "OK"
	if d.Pending.Count != 1 || d.Last[route.Direct] != "TLS_RST" {
		t.Error("Clone shares state")
	}
}
