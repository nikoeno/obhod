package desktop

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/nikoejento/obhod/internal/pc"
)

func readErrorFile(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("error file: %v", err)
	}
	return string(b)
}

func TestErrorFileNeedsUI(t *testing.T) {
	h := newUIHarness(t)
	path := filepath.Join(t.TempDir(), "err.txt")
	if code := h.run("run", "--error-file", path); code != 1 {
		t.Fatalf("exit %d", code)
	}
	if _, err := os.Stat(path); err == nil {
		t.Fatal("the file was written for a non-UI run")
	}
}

func TestErrorFileHoldsTheStartFailure(t *testing.T) {
	for name, c := range map[string]struct {
		err  error
		want int
	}{
		"needs admin": {pc.Fail(pc.ExitManual, "administrator rights are required"), 2},
		"runtime":     {pc.Fail(pc.ExitRuntime, "another instance"), 3},
		"config":      {pc.Fail(pc.ExitConfig, "status: port busy"), 1},
	} {
		h := newUIHarness(t)
		path := filepath.Join(t.TempDir(), "engine-error.txt")
		h.eng.runErr = c.err
		h.eng.runNow = true
		if code := h.run("run", "--ui", "--token-file", h.tokenArg, "--error-file", path); code != c.want {
			t.Fatalf("%s: exit %d", name, code)
		}
		want := "splitter: " + c.err.Error() + "\nexit code: " + string(rune('0'+c.want)) + "\n"
		if got := readErrorFile(t, path); got != want {
			t.Errorf("%s: file = %q, want %q", name, got, want)
		}
		if runtime.GOOS == "linux" {
			if fi, _ := os.Stat(path); fi.Mode().Perm() != 0o600 {
				t.Errorf("%s: mode %v", name, fi.Mode())
			}
		}
	}
}

func TestErrorFileForUsageAndTokenErrorsHasOneLineAndNoToken(t *testing.T) {
	h := newUIHarness(t)
	dir := t.TempDir()
	path := filepath.Join(dir, "e.txt")
	// A missing --token-file value is a usage error: the usage text must not go into the file.
	if code := h.run("run", "--ui", "--error-file", path); code != 1 {
		t.Fatalf("exit %d", code)
	}
	got := readErrorFile(t, path)
	if strings.Count(got, "\n") != 2 || !strings.HasSuffix(got, "exit code: 1\n") || strings.Contains(got, "usage:") {
		t.Fatalf("file = %q", got)
	}
	// An unknown flag after --error-file still reaches the file; the token file content never does.
	path2 := filepath.Join(dir, "e2.txt")
	const marker = "SECRETCONTENT"
	tok := writeTokenFile(t, marker)
	if code := h.run("run", "--ui", "--token-file", tok, "--error-file", path2, "--bogus"); code != 1 {
		t.Fatalf("exit %d", code)
	}
	if got := readErrorFile(t, path2); strings.Contains(got, marker) || !strings.Contains(got, "bogus") {
		t.Fatalf("file = %q", got)
	}
	path3 := filepath.Join(dir, "e3.txt")
	if code := h.run("run", "--ui", "--token-file", tok, "--error-file", path3, "--listen", "0.0.0.0:0"); code != 1 {
		t.Fatalf("exit %d", code)
	}
	if got := readErrorFile(t, path3); strings.Contains(got, marker) {
		t.Fatalf("file = %q", got)
	}
}

func TestErrorFileMasksTheToken(t *testing.T) {
	h := newUIHarness(t)
	path := filepath.Join(t.TempDir(), "e.txt")
	h.cli.errorFile = path
	h.cli.secrets = []string{testToken}
	h.cli.errorf(3, "boom %s boom", testToken)
	if got := readErrorFile(t, path); strings.Contains(got, testToken) || !strings.Contains(got, "[hidden]") {
		t.Fatalf("file = %q", got)
	}
}

func TestErrorFileStaleOneIsRemovedAndSuccessLeavesNone(t *testing.T) {
	h := newUIHarness(t)
	path := filepath.Join(t.TempDir(), "e.txt")
	if err := os.WriteFile(path, []byte("old\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	out := &syncWriter{}
	h.cli.stdout = out
	h.eng.makeReady()
	codeCh := make(chan int, 1)
	go func() { codeCh <- h.run("run", "--ui", "--token-file", h.tokenArg, "--error-file", path) }()
	waitFor(t, "ready line", func() bool { return strings.Contains(out.String(), "OBHOD-API") })
	if _, err := os.Stat(path); err == nil {
		t.Fatal("the stale file survived the start")
	}
	h.cancel()
	<-codeCh
	if _, err := os.Stat(path); err == nil {
		t.Fatal("a clean run left an error file")
	}
}

func TestErrorFileNotWrittenForAFailureAfterReady(t *testing.T) {
	h := newUIHarness(t)
	path := filepath.Join(t.TempDir(), "e.txt")
	out := &syncWriter{}
	h.cli.stdout = out
	h.eng.makeReady()
	h.eng.runErr = pc.Fail(pc.ExitRuntime, "sing-box failed")
	codeCh := make(chan int, 1)
	go func() { codeCh <- h.run("run", "--ui", "--token-file", h.tokenArg, "--error-file", path) }()
	waitFor(t, "ready line", func() bool { return strings.Contains(out.String(), "OBHOD-API") })
	h.cancel()
	if code := <-codeCh; code != 3 {
		t.Fatalf("exit %d", code)
	}
	if _, err := os.Stat(path); err == nil {
		t.Fatal("a runtime failure after the ready line was written as a start failure")
	}
}
