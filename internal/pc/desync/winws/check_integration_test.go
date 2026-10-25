//go:build integration && windows

package winws

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestPinnedBinaryKnowsFlags checks that the pinned winws2.exe contains every
// flag name the renderer emits. winws2.exe asks for elevation even for --help,
// so the check reads the binary instead of running it.
func TestPinnedBinaryKnowsFlags(t *testing.T) {
	bin, err := filepath.Abs(filepath.Join("..", "..", "..", "..", "third_party", "bin", "windows-amd64", "winws2.exe"))
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(bin)
	if err != nil {
		t.Skipf("pinned winws2 not installed: %v", err)
	}
	out, err := Render(baseInput())
	if err != nil {
		t.Fatal(err)
	}
	for _, arg := range out.Args {
		name, _, _ := strings.Cut(strings.TrimPrefix(arg, "--"), "=")
		if !strings.Contains(string(data), name) {
			t.Errorf("winws2.exe does not mention %q", name)
		}
	}
}
