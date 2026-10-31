package desktop

import (
	"context"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nikoejento/obhod/internal/lib/paths"
	"github.com/nikoejento/obhod/internal/pc"
)

// uiHarness runs `splitter run --ui` against a fake engine.
type uiHarness struct {
	*cliHarness
	eng      *fakeEngine
	engines  int
	opts     int
	tokenArg string
	cancel   context.CancelFunc
}

func newUIHarness(t *testing.T) *uiHarness {
	t.Helper()
	h := &uiHarness{cliHarness: newCLIHarness(t), eng: newFakeEngine()}
	h.writeConfig("log:\n  level: debug\n")
	h.cli.newEngine = func(_ pc.Config, _ paths.Paths, opts ...pc.Option) (engine, error) {
		h.engines++
		h.opts = len(opts)
		return h.eng, nil
	}
	h.cli.signalCtx = func() (context.Context, context.CancelFunc) {
		ctx, cancel := context.WithCancel(context.Background())
		h.cancel = cancel
		return ctx, cancel
	}
	h.tokenArg = writeTokenFile(t, testToken+"\r\n")
	return h
}

// syncWriter makes a buffer safe to read while the CLI writes.
type syncWriter struct {
	mu sync.Mutex
	b  strings.Builder
}

func (w *syncWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.b.Write(p)
}
func (w *syncWriter) String() string { w.mu.Lock(); defer w.mu.Unlock(); return w.b.String() }

var readyLine = regexp.MustCompile(`^OBHOD-API v1 (127\.0\.0\.1:\d+)\n$`)

func TestUIFlagErrors(t *testing.T) {
	h := newUIHarness(t)
	tok := h.tokenArg
	cases := map[string][]string{
		"ui without token file":       {"run", "--ui"},
		"token file without ui":       {"run", "--token-file", tok},
		"listen without ui":           {"run", "--listen", "127.0.0.1:0"},
		"addr file without ui":        {"run", "--addr-file", filepath.Join(t.TempDir(), "a")},
		"wildcard v4":                 {"run", "--ui", "--token-file", tok, "--listen", "0.0.0.0:0"},
		"wildcard v6":                 {"run", "--ui", "--token-file", tok, "--listen", "[::]:0"},
		"localhost":                   {"run", "--ui", "--token-file", tok, "--listen", "localhost:0"},
		"host name":                   {"run", "--ui", "--token-file", tok, "--listen", "example.org:8080"},
		"empty host":                  {"run", "--ui", "--token-file", tok, "--listen", ":0"},
		"missing port":                {"run", "--ui", "--token-file", tok, "--listen", "127.0.0.1"},
		"non-loopback address":        {"run", "--ui", "--token-file", tok, "--listen", "192.0.2.1:0"},
		"token file missing":          {"run", "--ui", "--token-file", filepath.Join(t.TempDir(), "absent")},
		"token file empty":            {"run", "--ui", "--token-file", writeTokenFile(t, "")},
		"token file short":            {"run", "--ui", "--token-file", writeTokenFile(t, "short")},
		"token file with a space":     {"run", "--ui", "--token-file", writeTokenFile(t, strings.Repeat("a", 20)+" "+strings.Repeat("b", 20))},
		"token file is a directory":   {"run", "--ui", "--token-file", t.TempDir()},
		"token file malformed binary": {"run", "--ui", "--token-file", writeTokenFile(t, strings.Repeat("a", 40)+"\x00")},
	}
	for name, args := range cases {
		h.errOut.Reset()
		h.out.Reset()
		if code := h.run(args...); code != 1 {
			t.Errorf("%s: exit %d, want 1 (%s)", name, code, h.errOut.String())
		}
		if h.out.Len() != 0 {
			t.Errorf("%s: stdout is not empty: %q", name, h.out.String())
		}
		if h.engines != 0 {
			t.Fatalf("%s: the engine was created for an invalid command line", name)
		}
	}
}

func TestUITokenErrorNeverEchoesTheContent(t *testing.T) {
	h := newUIHarness(t)
	const marker = "SECRETCONTENT"
	path := writeTokenFile(t, marker)
	if code := h.run("run", "--ui", "--token-file", path); code != 1 {
		t.Fatalf("exit %d", code)
	}
	if strings.Contains(h.errOut.String()+h.out.String(), marker) {
		t.Fatalf("the token content was printed: %s", h.errOut.String())
	}
	if !strings.Contains(h.errOut.String(), path) {
		t.Fatalf("the path is not named: %s", h.errOut.String())
	}
}

func TestUIBindFailureIsAConfigError(t *testing.T) {
	h := newUIHarness(t)
	busy, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer busy.Close()
	if code := h.run("run", "--ui", "--token-file", h.tokenArg, "--listen", busy.Addr().String()); code != 1 {
		t.Fatalf("exit %d: %s", code, h.errOut.String())
	}
	if h.out.Len() != 0 || h.eng.callCount("Run") != 0 {
		t.Fatalf("something started although the port is busy: %q %v", h.out.String(), h.eng.called())
	}
}

func TestUIReadyLineAddrFileAndServing(t *testing.T) {
	h := newUIHarness(t)
	out := &syncWriter{}
	h.cli.stdout = out
	addrFile := filepath.Join(t.TempDir(), "api.addr")

	codeCh := make(chan int, 1)
	go func() {
		codeCh <- h.run("run", "--ui", "--token-file", h.tokenArg, "--addr-file", addrFile, "--parent-pid", "5")
	}()
	waitFor(t, "Run", func() bool { return h.eng.callCount("Run") == 1 })

	// Nothing is printed, written or served before the app is ready.
	time.Sleep(50 * time.Millisecond)
	if out.String() != "" {
		t.Fatalf("output before Ready: %q", out.String())
	}
	if _, err := os.Stat(addrFile); err == nil {
		t.Fatal("addr file written before Ready")
	}

	h.eng.makeReady()
	waitFor(t, "the ready line", func() bool { return strings.HasSuffix(out.String(), "\n") })
	m := readyLine.FindStringSubmatch(out.String())
	if m == nil {
		t.Fatalf("ready line = %q", out.String())
	}
	addr := m[1]
	if b, err := os.ReadFile(addrFile); err != nil || string(b) != addr {
		t.Fatalf("addr file = %q, %v", b, err)
	}
	if runtime.GOOS != "windows" {
		if st, err := os.Stat(addrFile); err != nil || st.Mode().Perm() != 0o600 {
			t.Fatalf("addr file mode = %v, %v", st.Mode(), err)
		}
	}

	// The API answers on that address, with the token from the file (CRLF trimmed).
	get := func(path, token string) (int, string) {
		req, _ := http.NewRequest("GET", "http://"+addr+path, nil)
		if token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, string(b)
	}
	if code, body := get("/api/v1/version", testToken); code != 200 || !strings.Contains(body, `"api":"v1"`) || !strings.Contains(body, `"splitter":"splitter `) {
		t.Fatalf("version: %d %s", code, body)
	}
	if code, body := get("/api/v1/version", ""); code != 401 || body != "" {
		t.Fatalf("no token: %d %q", code, body)
	}

	// shutdown ends the run and the process with exit 0.
	req, _ := http.NewRequest("POST", "http://"+addr+"/api/v1/shutdown", nil)
	req.Header.Set("Authorization", "Bearer "+testToken)
	resp, err := http.DefaultClient.Do(req)
	if err != nil || resp.StatusCode != 200 {
		t.Fatalf("shutdown: %v %v", resp, err)
	}
	resp.Body.Close()
	select {
	case code := <-codeCh:
		if code != 0 {
			t.Fatalf("exit %d: %s", code, h.errOut.String())
		}
	case <-time.After(10 * time.Second):
		t.Fatal("run --ui did not exit after shutdown")
	}
	if got := out.String(); !readyLine.MatchString(got) {
		t.Fatalf("stdout has more than the ready line: %q", got)
	}
	if _, err := http.Get("http://" + addr + "/api/v1/version"); err == nil {
		t.Fatal("the listener is still open")
	}
	if h.opts < 4 {
		t.Fatalf("the engine got %d options", h.opts)
	}
}

func TestUIExitsWhenTheSignalArrives(t *testing.T) {
	h := newUIHarness(t)
	h.cli.stdout = &syncWriter{}
	codeCh := make(chan int, 1)
	go func() { codeCh <- h.run("run", "--ui", "--token-file", h.tokenArg) }()
	h.eng.makeReady()
	waitFor(t, "ready", func() bool { return strings.Contains(h.cli.stdout.(*syncWriter).String(), "OBHOD-API") })
	h.cancel()
	select {
	case code := <-codeCh:
		if code != 0 {
			t.Fatalf("exit %d", code)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("no exit after the signal")
	}
}

func TestUIStartFailureBeforeReadyPrintsNoLine(t *testing.T) {
	for name, c := range map[string]struct {
		err  error
		want int
	}{
		"needs admin": {pc.Fail(pc.ExitManual, "administrator rights are required"), 2},
		"instance":    {pc.Fail(pc.ExitRuntime, "another instance"), 3},
		"config":      {pc.Fail(pc.ExitConfig, "status: port busy"), 1},
	} {
		h := newUIHarness(t)
		h.eng.runErr = c.err
		h.eng.runNow = true // Run returns at once without ever becoming ready
		if code := h.run("run", "--ui", "--token-file", h.tokenArg); code != c.want {
			t.Errorf("%s: exit %d, want %d", name, code, c.want)
		}
		if h.out.Len() != 0 {
			t.Errorf("%s: stdout = %q", name, h.out.String())
		}
		if !strings.Contains(h.errOut.String(), c.err.Error()) {
			t.Errorf("%s: stderr = %q", name, h.errOut.String())
		}
	}
}

func TestUIAddrFileFailureIsReportedWithoutALine(t *testing.T) {
	h := newUIHarness(t)
	h.eng.makeReady()
	bad := filepath.Join(t.TempDir(), "missing-dir", "api.addr")
	if code := h.run("run", "--ui", "--token-file", h.tokenArg, "--addr-file", bad); code != 1 {
		t.Fatalf("exit %d", code)
	}
	if h.out.Len() != 0 {
		t.Fatalf("stdout = %q", h.out.String())
	}
}

func TestUIRunErrorAfterReadyMapsToExitCode(t *testing.T) {
	h := newUIHarness(t)
	out := &syncWriter{}
	h.cli.stdout = out
	h.eng.makeReady()
	h.eng.runErr = pc.Fail(pc.ExitRuntime, "sing-box failed")
	codeCh := make(chan int, 1)
	go func() { codeCh <- h.run("run", "--ui", "--token-file", h.tokenArg) }()
	waitFor(t, "ready line", func() bool { return strings.Contains(out.String(), "OBHOD-API") })
	h.cancel()
	if code := <-codeCh; code != 3 {
		t.Fatalf("exit %d", code)
	}
}
