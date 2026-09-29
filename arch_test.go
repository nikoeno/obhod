package arch_test

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// Module is one parsed API.md header.
type Module struct {
	Path      string
	Kind      string
	Binary    string
	Tags      []string
	Consumers []string
	Children  []string
	UsesLibs  []string
}

// Registry maps a module path to its header.
type Registry map[string]Module

func splitList(s string) []string {
	for {
		i := strings.Index(s, "(")
		j := strings.Index(s, ")")
		if i < 0 || j < i {
			break
		}
		s = s[:i] + s[j+1:]
	}
	var out []string
	for _, p := range strings.Split(s, ",") {
		p = strings.Trim(strings.TrimSpace(p), "`")
		if p == "" || p == "–" || p == "-" {
			continue
		}
		out = append(out, p)
	}
	return out
}

func parseHeader(file string) (Module, error) {
	f, err := os.Open(file)
	if err != nil {
		return Module{}, err
	}
	defer f.Close()
	fields := map[string]string{}
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := sc.Text()
		if strings.HasPrefix(line, "## ") {
			break
		}
		if !strings.HasPrefix(line, "|") {
			continue
		}
		cells := strings.Split(strings.Trim(line, "| "), "|")
		if len(cells) < 2 {
			continue
		}
		fields[strings.TrimSpace(cells[0])] = strings.TrimSpace(strings.Join(cells[1:], "|"))
	}
	m := Module{
		Path:      fields["Path"],
		Kind:      fields["Kind"],
		Binary:    strings.Join(splitList(fields["Binary"]), ""),
		Tags:      splitList(fields["Build tags"]),
		Consumers: splitList(fields["Consumers"]),
		Children:  splitList(fields["Children"]),
		UsesLibs:  splitList(fields["Uses libs"]),
	}
	if m.Path == "" || m.Kind == "" {
		return m, fmt.Errorf("%s: header lacks Path or Kind", file)
	}
	return m, nil
}

func loadRegistry(t testing.TB, root string) Registry {
	t.Helper()
	reg := Registry{}
	for _, top := range []string{"internal", "lab"} {
		err := filepath.WalkDir(filepath.Join(root, top), func(p string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() && d.Name() == "testdata" {
				return filepath.SkipDir
			}
			if d.IsDir() || d.Name() != "API.md" {
				return nil
			}
			m, err := parseHeader(p)
			if err != nil {
				return err
			}
			rel, _ := filepath.Rel(root, filepath.Dir(p))
			if want := filepath.ToSlash(rel); m.Path != want {
				return fmt.Errorf("%s: header Path %q does not match folder %q", p, m.Path, want)
			}
			reg[m.Path] = m
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	return reg
}

func isLab(p string) bool { return p == "lab" || strings.HasPrefix(p, "lab/") }

// isAncestorOrSelf reports whether a is p or a proper ancestor folder of p.
func isAncestorOrSelf(a, p string) bool { return a == p || strings.HasPrefix(p, a+"/") }

// libRoot reports whether to is <A>/lib/<name> and returns A.
func libRoot(to string) (string, bool) {
	dir, name := path.Split(to)
	dir = strings.TrimSuffix(dir, "/")
	if name == "" || path.Base(dir) != "lib" {
		return "", false
	}
	return path.Dir(dir), true
}

// allowed applies import rules (a)-(e). Paths are repo-relative.
func allowed(from, to string, isTest bool, reg Registry) (bool, string) {
	// (c) entry points.
	if strings.HasPrefix(from, "cmd/") {
		m, ok := reg[to]
		if ok && m.Kind == "app" && m.Binary == path.Base(from) {
			return true, "(c) entry point"
		}
		return false, "(c) cmd/<binary> may import only the app module declaring that Binary"
	}
	if strings.HasPrefix(to, "cmd/") {
		return false, "nothing may import cmd/..."
	}
	// (e) test-only modules.
	if reg[to].Kind == "test-only" && !isTest && !isLab(from) {
		return false, "(e) test-only module imported from non-test code"
	}
	// (d) lab.
	if isLab(from) {
		if path.Dir(to) == from && isLab(to) {
			return true, "(d)(a) direct child"
		}
		if a, ok := libRoot(to); ok {
			if isLab(a) && isAncestorOrSelf(a, from) {
				return true, "(d)(b) ancestor lib"
			}
			if a == "internal" {
				return true, "(d) internal/lib root"
			}
		}
		return false, "(d) lab may import only its direct children, ancestor libs inside lab and internal/lib roots"
	}
	// (a) direct child.
	if path.Dir(to) == from {
		return true, "(a) direct child"
	}
	// (b) ancestor lib, root package only.
	if a, ok := libRoot(to); ok && isAncestorOrSelf(a, from) {
		return true, "(b) ancestor lib"
	}
	switch {
	case isAncestorOrSelf(to, from):
		return false, "importing a parent is forbidden"
	case isAncestorOrSelf(from, to):
		if strings.Contains(strings.TrimPrefix(to, from+"/"), "lib/") {
			return false, "reaching into a lib child is forbidden"
		}
		return false, "grandchild: reach it through the child's API"
	case path.Dir(from) == path.Dir(to):
		return false, "sibling import: move the shared code to a lib/ in their parent"
	}
	return false, "not a direct child and not <ancestor>/lib/<name>"
}

func TestAllowed(t *testing.T) {
	reg := Registry{
		"internal/b":     {Kind: "app", Binary: "b"},
		"internal/lib/t": {Kind: "test-only"},
	}
	cases := []struct {
		from, to string
		test     bool
		want     bool
	}{
		{"internal/x", "internal/x/y", false, true},
		{"internal/x", "internal/x/y/z", false, false},
		{"internal/x/y", "internal/x/lib/s", false, true},
		{"internal/x/y", "internal/lib/m", false, true},
		{"internal/x/y", "internal/lib/m/n", false, false},
		{"internal/x/y", "internal/x/w", false, false},
		{"internal/x/y", "internal/x", false, false},
		{"internal/lib/m/n", "internal/lib/m/lib/t", false, true},
		{"internal/v", "internal/x/lib/s", false, false},
		{"cmd/b", "internal/b", false, true},
		{"cmd/b", "internal/lib/m", false, false},
		{"internal/x", "internal/lib/t", true, true},
		{"internal/x", "internal/lib/t", false, false},
		{"lab", "lab/k", false, true},
		{"lab/k", "internal/lib/m", false, true},
		{"lab/k", "internal/x", false, false},
	}
	for _, c := range cases {
		got, why := allowed(c.from, c.to, c.test, reg)
		if got != c.want {
			t.Errorf("allowed(%s, %s, test=%v) = %v (%s), want %v", c.from, c.to, c.test, got, why, c.want)
		}
	}
}

func hasGoFiles(dir string) bool {
	ents, _ := os.ReadDir(dir)
	for _, e := range ents {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".go") {
			return true
		}
	}
	return false
}

func report(t *testing.T, problems []string) {
	t.Helper()
	if len(problems) == 0 {
		return
	}
	sort.Strings(problems)
	t.Errorf("%d architecture violation(s):\n  %s", len(problems), strings.Join(problems, "\n  "))
}

func contains(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

// TestRegistry checks that API.md files and Go packages match one to one.
func TestRegistry(t *testing.T) {
	reg := loadRegistry(t, ".")
	var problems []string
	for p, m := range reg {
		if !hasGoFiles(filepath.FromSlash(p)) {
			problems = append(problems, p+": API.md without a Go package")
		}
		for _, c := range m.Children {
			if _, ok := reg[c]; !ok {
				problems = append(problems, fmt.Sprintf("%s: child %s has no API.md", p, c))
			}
		}
	}
	for _, top := range []string{"internal", "lab"} {
		_ = filepath.WalkDir(top, func(p string, d fs.DirEntry, err error) error {
			if err != nil || !d.IsDir() {
				return nil
			}
			if d.Name() == "testdata" {
				return filepath.SkipDir
			}
			if hasGoFiles(p) {
				if _, ok := reg[filepath.ToSlash(p)]; !ok {
					problems = append(problems, filepath.ToSlash(p)+": Go package without API.md")
				}
			}
			return nil
		})
	}
	report(t, problems)
}

type listedPkg struct {
	ImportPath   string
	Imports      []string
	TestImports  []string
	XTestImports []string
}

func goList(t testing.TB, env []string, args ...string) []listedPkg {
	t.Helper()
	cmd := exec.Command("go", append([]string{"list", "-e", "-json"}, args...)...)
	cmd.Env = append(os.Environ(), env...)
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("go list %v: %v", args, err)
	}
	var pkgs []listedPkg
	dec := json.NewDecoder(strings.NewReader(string(out)))
	for dec.More() {
		var p listedPkg
		if err := dec.Decode(&p); err != nil {
			t.Fatal(err)
		}
		pkgs = append(pkgs, p)
	}
	return pkgs
}

// TestArchitecture checks every in-repo import against rules (a)-(e) and the
// Consumers / Children / Uses libs declared in the API.md headers.
func TestArchitecture(t *testing.T) {
	reg := loadRegistry(t, ".")
	modOut, err := exec.Command("go", "list", "-m").Output()
	if err != nil {
		t.Fatal(err)
	}
	mod := strings.TrimSpace(string(modOut))
	rel := func(p string) (string, bool) {
		if p == mod {
			return "", true
		}
		if strings.HasPrefix(p, mod+"/") {
			return strings.TrimPrefix(p, mod+"/"), true
		}
		return "", false
	}

	configs := []struct {
		env  []string
		args []string
	}{
		{nil, nil},
		{nil, []string{"-tags", "lab"}},
		{[]string{"GOOS=linux"}, nil},
		{[]string{"GOOS=windows"}, nil},
	}
	seen := map[string]bool{}
	var problems []string
	for _, cfg := range configs {
		for _, p := range goList(t, cfg.env, append(append([]string{}, cfg.args...), "./...")...) {
			from, ok := rel(p.ImportPath)
			if !ok {
				continue
			}
			imports := map[string]bool{}
			for _, i := range p.Imports {
				imports[i] = false
			}
			for _, i := range append(append([]string{}, p.TestImports...), p.XTestImports...) {
				if _, dup := imports[i]; !dup {
					imports[i] = true
				}
			}
			for imp, isTest := range imports {
				to, ok := rel(imp)
				if !ok {
					continue
				}
				key := from + " → " + to
				if seen[key] {
					continue
				}
				seen[key] = true
				if ok, why := allowed(from, to, isTest, reg); !ok {
					problems = append(problems, fmt.Sprintf("%s: %s", key, why))
					continue
				}
				if strings.HasPrefix(from, "cmd/") {
					continue
				}
				if q, ok := reg[to]; ok && !contains(q.Consumers, from) {
					problems = append(problems, fmt.Sprintf("%s: %s is not listed in Consumers of %s", key, from, to))
				}
				if pm, ok := reg[from]; ok && !contains(pm.Children, to) && !contains(pm.UsesLibs, to) {
					problems = append(problems, fmt.Sprintf("%s: %s is in neither Children nor Uses libs of %s", key, to, from))
				}
			}
		}
	}
	report(t, problems)
}
