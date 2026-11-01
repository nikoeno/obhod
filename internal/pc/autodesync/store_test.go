package autodesync

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestStoreRoundTripIsPrivateAndAtomic(t *testing.T) {
	dir := t.TempDir()
	s, err := OpenStore(dir, nil)
	if err != nil {
		t.Fatal(err)
	}
	at := time.Unix(1_700_000_000, 0).UTC()
	rec := Record{Preset: "alt3-hostfakesplit", At: at, Fixed: []string{"b.example", "a.example"}, Failed: map[string]time.Time{"c.example": at}}
	if err := s.Put("asn:64500", rec); err != nil {
		t.Fatal(err)
	}
	if err := s.SetSelected("auto"); err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" {
		if fi, err := os.Stat(filepath.Join(dir, FileName)); err != nil || fi.Mode().Perm() != 0o600 {
			t.Fatalf("mode %v, %v", fi, err)
		}
	}
	if m, _ := filepath.Glob(filepath.Join(dir, "*.tmp*")); len(m) != 0 {
		t.Fatalf("temp files left: %v", m)
	}
	s2, err := OpenStore(dir, nil)
	if err != nil {
		t.Fatal(err)
	}
	got, ok := s2.Get("asn:64500")
	if !ok || got.Preset != rec.Preset || !got.At.Equal(at) || len(got.Fixed) != 2 || !got.Failed["c.example"].Equal(at) {
		t.Fatalf("got %+v, %v", got, ok)
	}
	if s2.Selected() != "auto" {
		t.Fatalf("selected %q", s2.Selected())
	}
	if _, ok := s2.Get("asn:1"); ok {
		t.Fatal("unknown network found")
	}
	// A copy: changing it does not change the store.
	got.Fixed[0] = "x"
	if again, _ := s2.Get("asn:64500"); again.Fixed[0] == "x" {
		t.Fatal("Get shares memory")
	}
}

func TestStoreMovesAnUnreadableFileAside(t *testing.T) {
	for name, content := range map[string]string{
		"garbage":       "not json",
		"unknown field": `{"version":1,"selected":"","networks":{},"extra":1}`,
		"bad version":   `{"version":2,"selected":"","networks":{}}`,
		"empty net id":  `{"version":1,"selected":"","networks":{"":{"preset":"x","at":"2026-01-01T00:00:00Z","fixed":[],"failed":{}}}}`,
	} {
		dir := t.TempDir()
		if err := os.WriteFile(filepath.Join(dir, FileName), []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
		s, err := OpenStore(dir, nil)
		if err != nil || !s.Recovered() {
			t.Fatalf("%s: %v recovered=%v", name, err, s != nil && s.Recovered())
		}
		if _, err := os.Stat(filepath.Join(dir, CorruptName)); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if s.Selected() != "" {
			t.Fatalf("%s: not empty", name)
		}
	}
}

func TestStoreKeepsAtMost32NetworksOldestFirstOut(t *testing.T) {
	s, err := OpenStore(t.TempDir(), nil)
	if err != nil {
		t.Fatal(err)
	}
	base := time.Unix(1_700_000_000, 0)
	for i := 0; i < 40; i++ {
		id := "asn:" + strings.Repeat("1", i+1)
		if err := s.Put(id, Record{Preset: "p", At: base.Add(time.Duration(i) * time.Hour)}); err != nil {
			t.Fatal(err)
		}
	}
	n := 0
	for i := 0; i < 40; i++ {
		if _, ok := s.Get("asn:" + strings.Repeat("1", i+1)); ok {
			n++
			if i < 8 {
				t.Errorf("network %d should have been dropped", i)
			}
		}
	}
	if n != maxNetworks {
		t.Fatalf("%d networks kept, want %d", n, maxNetworks)
	}
}

func TestConfigDefaultsAndLimits(t *testing.T) {
	if err := DefaultConfig().Validate(); err != nil {
		t.Fatal(err)
	}
	if err := (Config{}).Validate(); err != nil {
		t.Fatalf("zero value must take the defaults: %v", err)
	}
	d := DefaultConfig()
	if d.Concurrency > MaxConcurrency || d.MaxPresets != 6 || d.Budget != 3*time.Minute {
		t.Fatalf("%+v", d)
	}
	for name, c := range map[string]Config{
		"concurrency 3":  {Concurrency: 3},
		"concurrency -1": {Concurrency: -1},
		"presets 99":     {MaxPresets: 99},
		"budget 1s":      {Budget: time.Second},
		"pause reversed": {Pause: [2]time.Duration{3 * time.Second, time.Second}},
		"domains 100":    {MaxDomains: 100},
		"timeout 1h":     {ProbeTimeout: time.Hour},
		"ttl 1s":         {FailedTTL: time.Second},
	} {
		if err := c.Validate(); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}
