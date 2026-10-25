//go:build linux

package lab

import (
	"net/netip"
	"os"
	"path/filepath"
	"testing"

	"github.com/nikoejento/obhod/lab/fakedpi"
)

func TestLoadProfiles(t *testing.T) {
	ps, err := LoadProfiles("profiles")
	if err != nil {
		t.Fatal(err)
	}
	if len(ps) < 2 || ps[0].Name != "ispa" || ps[0].ASN != 64500 || ps[1].Name != "ispb" || ps[1].ASN != 64501 {
		t.Fatalf("profiles = %+v", ps)
	}
	if ps[0].Poisoned["poisoned.test"] != netip.MustParseAddr("192.0.2.10") {
		t.Errorf("ispa poisoned = %v", ps[0].Poisoned)
	}

	dir := t.TempDir()
	write := func(name, body string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write("a.yaml", "name: x1\nasn: 1\nrules:\n  - {kind: ip-block, prefix: 198.51.100.10/32}\n  - {kind: naive-sni, domains: [a.test]}\n")
	ps, err = LoadProfiles(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(ps[0].Rules) != 2 || ps[0].Rules[0].Kind != fakedpi.IPBlock || ps[0].Rules[0].Prefix != netip.MustParsePrefix("198.51.100.10/32") {
		t.Errorf("rules = %+v", ps[0].Rules)
	}

	for name, body := range map[string]string{
		"unknown key":  "name: x2\nasn: 1\nfoo: 1\n",
		"unknown kind": "name: x2\nasn: 1\nrules: [{kind: nope}]\n",
		"bad name":     "name: isp-a\nasn: 1\n",
		"reserved":     "name: inet\nasn: 1\n",
		"no asn":       "name: x2\n",
		"bad stub":     "name: x2\nasn: 1\npoisoned: {a.test: nope}\n",
		"duplicate":    "name: x1\nasn: 2\n",
	} {
		write("b.yaml", body)
		if _, err := LoadProfiles(dir); err == nil {
			t.Errorf("%s: want an error", name)
		}
	}
}
