package pc

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/nikoejento/obhod/internal/lib/paths"
	"github.com/nikoejento/obhod/internal/pc/desync"
)

// TestWritePlanFilesOwnerOnly: the hostlist is Run/hostlist.txt, mode 0600
// (chmod, whatever the umask); the Run directory is left alone.
func TestWritePlanFilesOwnerOnly(t *testing.T) {
	p := paths.FromRoot(t.TempDir())
	if err := os.Mkdir(p.Run, 0o700); err != nil {
		t.Fatal(err)
	}
	a := &App{p: p}
	plan := desync.Plan{Files: map[string][]byte{desync.HostlistFile: []byte("yt.example\n")}}
	if err := a.writePlanFiles(plan); err != nil {
		t.Fatal(err)
	}
	f := filepath.Join(p.Run, "hostlist.txt")
	if b, err := os.ReadFile(f); err != nil || string(b) != "yt.example\n" {
		t.Fatalf("hostlist = %q, %v", b, err)
	}
	if runtime.GOOS != "windows" {
		if fi, _ := os.Stat(f); fi.Mode().Perm() != 0o600 {
			t.Fatalf("hostlist mode = %v, want 0600", fi.Mode().Perm())
		}
		if fi, _ := os.Stat(p.Run); fi.Mode().Perm() != 0o700 {
			t.Fatalf("run dir mode = %v, want 0700", fi.Mode().Perm())
		}
	}
}

func TestWritePlanFilesRejectsUnsafeNames(t *testing.T) {
	a := &App{p: paths.FromRoot(t.TempDir())}
	for _, n := range []string{"../x", "a/b", "/abs", ".", "..", ""} {
		if err := a.writePlanFiles(desync.Plan{Files: map[string][]byte{n: nil}}); err == nil {
			t.Errorf("name %q accepted", n)
		}
	}
}
