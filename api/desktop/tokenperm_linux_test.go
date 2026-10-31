package desktop

import (
	"os"
	"strings"
	"testing"
)

func TestTokenFileModeOnLinux(t *testing.T) {
	path := writeTokenFile(t, strings.Repeat("a", 40))
	if _, err := readTokenFile(path); err != nil {
		t.Fatalf("0600: %v", err)
	}
	for _, mode := range []os.FileMode{0o640, 0o604, 0o644, 0o666} {
		if err := os.Chmod(path, mode); err != nil {
			t.Fatal(err)
		}
		_, err := readTokenFile(path)
		if err == nil || !strings.Contains(err.Error(), path) || strings.Contains(err.Error(), strings.Repeat("a", 40)) {
			t.Errorf("%04o: %v", mode, err)
		}
	}
}
