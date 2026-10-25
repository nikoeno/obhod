//go:build linux

package lab

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"net/netip"
	"os"
	"path/filepath"
	"slices"

	"gopkg.in/yaml.v3"

	"github.com/nikoejento/obhod/lab/fakedpi"
)

// Profile is one fake ISP: its name (also its namespace name), ASN, fake TSPU rules and the names
// its plain DNS resolver poisons. Profiles are loaded from lab/profiles/*.yaml (LoadProfiles).
type Profile struct {
	Name     string
	ASN      uint32
	Rules    []fakedpi.Rule
	Poisoned map[string]netip.Addr // plain-DNS answers the ISP resolver fakes (name → stub IP)
}

// profileFile is the YAML form of a Profile.
type profileFile struct {
	Name  string `yaml:"name"`
	ASN   uint32 `yaml:"asn"`
	Rules []struct {
		Kind    string   `yaml:"kind"`
		Domains []string `yaml:"domains"`
		Prefix  string   `yaml:"prefix"`
		Bytes   int      `yaml:"bytes"`
	} `yaml:"rules"`
	Poisoned map[string]string `yaml:"poisoned"`
}

var ruleKinds = []fakedpi.Kind{
	fakedpi.NaiveSNI, fakedpi.FirstHello, fakedpi.Reassemble, fakedpi.Stall, fakedpi.IPBlock, fakedpi.QUICBlock,
}

// LoadProfiles reads every *.yaml file in dir, in file name order (the first one is the client's
// ISP when passed to Up as is). Unknown keys, unknown rule kinds and invalid addresses are errors.
//
//	name: ispa              # [a-z][a-z0-9]*, at most 15 characters; not cli, inet or vps
//	asn: 64500
//	rules:                  # fakedpi rules: kind, domains, prefix, bytes
//	  - {kind: naive-sni, domains: [blocked.test]}
//	poisoned:               # name (suffix match) → stub address
//	  poisoned.test: 192.0.2.10
func LoadProfiles(dir string) ([]Profile, error) {
	files, err := filepath.Glob(filepath.Join(dir, "*.yaml"))
	if err != nil {
		return nil, fmt.Errorf("lab: profiles: %w", err)
	}
	slices.Sort(files)
	var out []Profile
	for _, f := range files {
		p, err := loadProfile(f)
		if err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	if err := validateProfiles(out); err != nil {
		return nil, err
	}
	return out, nil
}

func loadProfile(file string) (Profile, error) {
	b, err := os.ReadFile(file)
	if err != nil {
		return Profile{}, fmt.Errorf("lab: profile: %w", err)
	}
	var pf profileFile
	dec := yaml.NewDecoder(bytes.NewReader(b))
	dec.KnownFields(true)
	if err := dec.Decode(&pf); err != nil && !errors.Is(err, io.EOF) {
		return Profile{}, fmt.Errorf("lab: profile %s: %w", file, err)
	}
	p := Profile{Name: pf.Name, ASN: pf.ASN}
	for i, r := range pf.Rules {
		rule := fakedpi.Rule{Kind: fakedpi.Kind(r.Kind), Domains: r.Domains, Bytes: r.Bytes}
		if !slices.Contains(ruleKinds, rule.Kind) {
			return Profile{}, fmt.Errorf("lab: profile %s: rule %d: unknown kind %q", file, i, r.Kind)
		}
		if r.Prefix != "" {
			if rule.Prefix, err = netip.ParsePrefix(r.Prefix); err != nil {
				return Profile{}, fmt.Errorf("lab: profile %s: rule %d: %w", file, i, err)
			}
		}
		p.Rules = append(p.Rules, rule)
	}
	if len(pf.Poisoned) > 0 {
		p.Poisoned = map[string]netip.Addr{}
		for name, s := range pf.Poisoned {
			a, err := netip.ParseAddr(s)
			if err != nil {
				return Profile{}, fmt.Errorf("lab: profile %s: poisoned %s: %w", file, name, err)
			}
			p.Poisoned[name] = a
		}
	}
	if err := p.validate(); err != nil {
		return Profile{}, fmt.Errorf("%w (%s)", err, file)
	}
	return p, nil
}

// validate checks the fields Up relies on: the name becomes a namespace name.
func (p Profile) validate() error {
	if !validISPName(p.Name) {
		return fmt.Errorf("lab: profile name %q: want [a-z][a-z0-9]*, at most 15 characters, not cli, inet or vps", p.Name)
	}
	if p.ASN == 0 {
		return fmt.Errorf("lab: profile %s: asn missing", p.Name)
	}
	return nil
}

func validateProfiles(ps []Profile) error {
	if len(ps) == 0 {
		return errors.New("lab: no profiles")
	}
	if len(ps) > maxISPs {
		return fmt.Errorf("lab: %d profiles, at most %d", len(ps), maxISPs)
	}
	seen := map[string]bool{}
	for _, p := range ps {
		if err := p.validate(); err != nil {
			return err
		}
		if seen[p.Name] {
			return fmt.Errorf("lab: profile %s listed twice", p.Name)
		}
		seen[p.Name] = true
	}
	return nil
}

func validISPName(s string) bool {
	if len(s) < 1 || len(s) > 15 || s[0] < 'a' || s[0] > 'z' || s == "cli" || s == "inet" || s == "vps" {
		return false
	}
	for i := 1; i < len(s); i++ {
		if c := s[i]; (c < 'a' || c > 'z') && (c < '0' || c > '9') {
			return false
		}
	}
	return true
}
