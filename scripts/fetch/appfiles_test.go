package main

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// Packaging guard (first-run regression: the app folder lacked sing-box.exe / winws2.exe, so Start failed).
// scripts/app-files.txt is the one list of the files that sit next to splitter.exe in the Windows app
// folder; scripts/build-app.ps1 (make app) copies exactly that list. This test keeps the list equal to the
// windows-amd64 files of third_party/manifest.yaml and checks that the script and the Makefile use it.

const appPlatform = "windows-amd64"

func repoFile(parts ...string) string {
	return filepath.Join(append([]string{"..", ".."}, parts...)...)
}

func listedAppFiles(t *testing.T) []string {
	t.Helper()
	data, err := os.ReadFile(repoFile("scripts", "app-files.txt"))
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, line := range strings.Split(string(data), "\n") {
		if i := strings.IndexByte(line, '#'); i >= 0 {
			line = line[:i]
		}
		if line = strings.TrimSpace(line); line != "" {
			out = append(out, line)
		}
	}
	sort.Strings(out)
	return out
}

func manifestAppFiles(t *testing.T) []string {
	t.Helper()
	m, err := loadManifest(repoFile("third_party", "manifest.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, b := range m.Binaries {
		for _, f := range b.Platforms[appPlatform].Files {
			out = append(out, f.To)
		}
	}
	sort.Strings(out)
	return out
}

func TestAppFilesListMatchesManifest(t *testing.T) {
	listed, pinned := listedAppFiles(t), manifestAppFiles(t)
	if len(pinned) == 0 {
		t.Fatalf("the manifest has no %s files", appPlatform)
	}
	in := func(list []string, s string) bool {
		for _, x := range list {
			if x == s {
				return true
			}
		}
		return false
	}
	for _, f := range pinned {
		if !in(listed, f) {
			t.Errorf("%s is pinned in third_party/manifest.yaml for %s but missing from scripts/app-files.txt: the app folder would lack it", f, appPlatform)
		}
	}
	for _, f := range listed {
		if !in(pinned, f) {
			t.Errorf("%s is in scripts/app-files.txt but not pinned for %s in third_party/manifest.yaml", f, appPlatform)
		}
	}
	// The programs the engine looks for next to splitter.exe, and the Lua files winws2 loads.
	for _, must := range []string{"sing-box.exe", "libcronet.dll", "winws2.exe", "WinDivert.dll", "WinDivert64.sys", "cygwin1.dll",
		"lua/zapret-lib.lua", "lua/zapret-antidpi.lua", "lua/zapret-auto.lua"} {
		if !in(listed, must) {
			t.Errorf("%s must be in scripts/app-files.txt", must)
		}
	}
	for _, f := range listed {
		if strings.Contains(f, "\\") || strings.HasPrefix(f, "/") || strings.Contains(f, "..") {
			t.Errorf("%q: use a relative path with forward slashes", f)
		}
	}
}

func TestBuildScriptAndMakefileUseTheList(t *testing.T) {
	ps, err := os.ReadFile(repoFile("scripts", "build-app.ps1"))
	if err != nil {
		t.Fatal(err)
	}
	script := string(ps)
	for _, want := range []string{"app-files.txt", "third_party/bin/windows-amd64", "splitter.exe", "Obhod.exe", "Fail "} {
		if !strings.Contains(script, want) {
			t.Errorf("scripts/build-app.ps1 does not mention %q", want)
		}
	}
	// One list only: the script must not carry its own copy of the file names.
	for _, f := range manifestAppFiles(t) {
		if strings.HasSuffix(f, ".exe") || strings.HasSuffix(f, ".dll") || strings.HasSuffix(f, ".sys") {
			if strings.Contains(script, f) {
				t.Errorf("scripts/build-app.ps1 hard-codes %s; keep file names in scripts/app-files.txt only", f)
			}
		}
	}
	mk, err := os.ReadFile(repoFile("Makefile"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(mk), "\napp:\n") || !strings.Contains(string(mk), "scripts/build-app.ps1") {
		t.Error("the Makefile has no `app` target that runs scripts/build-app.ps1")
	}
}
