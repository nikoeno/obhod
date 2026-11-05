package desktop

import (
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"runtime"
	"strings"
	"testing"

	"github.com/nikoejento/obhod/internal/lib/checker/lists"
	"github.com/nikoejento/obhod/internal/lib/paths"
	"github.com/nikoejento/obhod/internal/lib/route"
	"github.com/nikoejento/obhod/internal/pc"
)

// firstStart is a UI harness with no app.yaml, as on a fresh machine.
func firstStart(t *testing.T) (*uiHarness, string) {
	t.Helper()
	h := newUIHarness(t)
	cfg := filepath.Join(h.p.Config, "app.yaml")
	if err := os.RemoveAll(h.p.Config); err != nil {
		t.Fatal(err)
	}
	h.eng.runNow = true // Run returns at once: exit 0
	return h, cfg
}

func TestUIFirstStartCreatesDefaultConfig(t *testing.T) {
	h, cfg := firstStart(t)
	var got pc.Config
	h.cli.newEngine = func(c pc.Config, _ paths.Paths, _ ...pc.Option) (engine, error) {
		got = c
		return h.eng, nil
	}
	if code := h.run("run", "--ui", "--token-file", h.tokenArg); code != 0 {
		t.Fatalf("exit %d: %s", code, h.errOut.String())
	}
	data, err := os.ReadFile(cfg)
	if err != nil {
		t.Fatalf("default config not created: %v", err)
	}
	if !reflect.DeepEqual(got, mustLoad(t, cfg)) {
		t.Error("the engine did not get the config loaded from the new file")
	}
	// Nothing but defaults and the three starter lists next to the file.
	want := pc.DefaultConfig()
	// The file writes out the built-in default preset so that the user sees the setting.
	want.Desync.Preset = want.Desync.PresetID()
	dir := filepath.Dir(cfg)
	want.Checker.Lists.Candidates = []string{filepath.Join(dir, "lists", "candidates.txt")}
	want.Checker.Lists.Proxy = []string{filepath.Join(dir, "lists", "proxy.txt")}
	want.Checker.Lists.Block = []string{filepath.Join(dir, "lists", "block.txt")}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("default file changes the defaults:\n got %+v\nwant %+v", got, want)
	}
	if s := string(data); strings.Contains(s, "tunnel.json") && !strings.Contains(s, "#") {
		t.Error("unexpected content")
	}
	for _, d := range []string{h.p.Data, h.p.Logs, h.p.Secrets, h.p.Run} {
		if fi, err := os.Stat(d); err != nil || !fi.IsDir() {
			t.Errorf("directory %s not created: %v", d, err)
		}
	}
	if runtime.GOOS != "windows" {
		if fi, _ := os.Stat(cfg); fi.Mode().Perm() != 0o600 {
			t.Errorf("mode = %v, want 0600", fi.Mode().Perm())
		}
	}
	if left, _ := filepath.Glob(cfg + ".tmp*"); len(left) != 0 {
		t.Errorf("temporary files left: %v", left)
	}
}

func mustLoad(t *testing.T, path string) pc.Config {
	t.Helper()
	c, err := pc.LoadConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func TestUISecondStartDoesNotOverwrite(t *testing.T) {
	h, cfg := firstStart(t)
	if code := h.run("run", "--ui", "--token-file", h.tokenArg); code != 0 {
		t.Fatalf("first: exit %d: %s", code, h.errOut.String())
	}
	const mine = "log:\n  level: warn\n"
	if err := os.WriteFile(cfg, []byte(mine), 0o600); err != nil {
		t.Fatal(err)
	}
	if code := h.run("run", "--ui", "--token-file", h.tokenArg); code != 0 {
		t.Fatalf("second: exit %d: %s", code, h.errOut.String())
	}
	if b, _ := os.ReadFile(cfg); string(b) != mine {
		t.Errorf("existing config was overwritten: %q", b)
	}
}

func TestUIFirstStartCreatesStarterLists(t *testing.T) {
	h, cfg := firstStart(t)
	if code := h.run("run", "--ui", "--token-file", h.tokenArg); code != 0 {
		t.Fatalf("exit %d: %s", code, h.errOut.String())
	}
	dir := filepath.Join(filepath.Dir(cfg), "lists")
	for _, n := range defaultListNames {
		if fi, err := os.Stat(filepath.Join(dir, n)); err != nil || fi.Size() == 0 {
			t.Errorf("%s not created: %v", n, err)
		}
	}
	if left, _ := filepath.Glob(filepath.Join(dir, "*.tmp*")); len(left) != 0 {
		t.Errorf("temporary files left: %v", left)
	}
	// The generated app.yaml loads and its lists load through the real package.
	c := mustLoad(t, cfg)
	l, err := lists.Load(c.Checker.Lists)
	if err != nil {
		t.Fatal(err)
	}
	if len(l.Warnings) != 0 {
		t.Errorf("warnings: %v", l.Warnings)
	}
	if n := len(l.Candidates); n < 80 || n > 200 {
		t.Errorf("%d candidates, want about 80-150", n)
	}
	if len(l.Static) != 0 {
		t.Errorf("proxy/block must ship empty, got %v", l.Static)
	}
}

func TestStarterListsAreClean(t *testing.T) {
	ip := regexp.MustCompile(`\b\d{1,3}(\.\d{1,3}){3}\b|::|://|@|[Tt]oken|[Ss]ecret|[Pp]assword|[Kk]ey=`)
	for _, n := range defaultListNames {
		data, err := defaultLists.ReadFile("defaultlists/" + n)
		if err != nil {
			t.Fatal(err)
		}
		if m := ip.Find(data); m != nil {
			t.Errorf("%s contains %q", n, m)
		}
		domains, warns, err := lists.Parse(strings.NewReader(string(data)), n)
		if err != nil || len(warns) != 0 {
			t.Errorf("%s: err %v warnings %v", n, err, warns)
		}
		// No duplicates: Parse dedupes, so count the raw domain lines.
		var raw int
		for _, line := range strings.Split(string(data), "\n") {
			if i := strings.IndexByte(line, '#'); i >= 0 {
				line = line[:i]
			}
			if strings.TrimSpace(line) != "" {
				raw++
			}
		}
		if raw != len(domains) {
			t.Errorf("%s: %d lines but %d distinct domains", n, raw, len(domains))
		}
		for _, d := range domains {
			if norm, err := route.Normalize(d); err != nil || norm != d || !strings.Contains(d, ".") {
				t.Errorf("%s: %q is not a normalized domain (%v)", n, d, err)
			}
		}
	}
	// A domain must not sit in two lists: precedence would silently hide it.
	cand, _, _ := lists.Parse(strings.NewReader(readEmbedded(t, "candidates.txt")), "c")
	for _, n := range []string{"proxy.txt", "block.txt"} {
		other, _, _ := lists.Parse(strings.NewReader(readEmbedded(t, n)), n)
		if len(other) != 0 {
			t.Errorf("%s should ship empty", n)
		}
	}
	if len(cand) == 0 {
		t.Error("no candidates")
	}
}

func readEmbedded(t *testing.T, name string) string {
	t.Helper()
	b, err := defaultLists.ReadFile("defaultlists/" + name)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestUISecondStartKeepsEditedLists(t *testing.T) {
	h, cfg := firstStart(t)
	if code := h.run("run", "--ui", "--token-file", h.tokenArg); code != 0 {
		t.Fatalf("first: exit %d: %s", code, h.errOut.String())
	}
	mine := filepath.Join(filepath.Dir(cfg), "lists", "candidates.txt")
	if err := os.WriteFile(mine, []byte("example.org\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	// The user removed app.yaml but kept the lists: they must survive.
	if err := os.Remove(cfg); err != nil {
		t.Fatal(err)
	}
	if code := h.run("run", "--ui", "--token-file", h.tokenArg); code != 0 {
		t.Fatalf("second: exit %d: %s", code, h.errOut.String())
	}
	if b, _ := os.ReadFile(mine); string(b) != "example.org\n" {
		t.Errorf("edited list was overwritten: %q", b)
	}
}

func TestWriteDefaultConfigNeverOverwrites(t *testing.T) {
	path := filepath.Join(t.TempDir(), "app.yaml")
	if err := os.WriteFile(path, []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := writeDefaultConfig(path); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(path); string(b) != "keep" {
		t.Errorf("overwritten: %q", b)
	}
	if left, _ := filepath.Glob(path + ".tmp*"); len(left) != 0 {
		t.Errorf("temporary files left: %v", left)
	}
}

func TestExplicitMissingConfigStillFails(t *testing.T) {
	h, _ := firstStart(t)
	missing := filepath.Join(t.TempDir(), "nope.yaml")
	if code := h.run("run", "--ui", "--token-file", h.tokenArg, "--config", missing); code != 1 {
		t.Fatalf("exit %d, want 1", code)
	}
	if _, err := os.Stat(missing); err == nil {
		t.Error("an explicit missing --config was created")
	}
	if h.engines != 0 {
		t.Error("engine was built")
	}
	if _, err := os.Stat(filepath.Join(h.p.Config, "app.yaml")); err == nil {
		t.Error("default config was created next to an explicit --config")
	}
}

func TestNonUIModesDoNotCreateConfig(t *testing.T) {
	h, cfg := firstStart(t)
	for _, args := range [][]string{{"run"}, {"check-config"}} {
		if code := h.run(args...); code != 1 {
			t.Errorf("%v: exit %d, want 1", args, code)
		}
		if _, err := os.Stat(cfg); err == nil {
			t.Errorf("%v created the config", args)
		}
		if _, err := os.Stat(h.p.Config); err == nil {
			t.Errorf("%v created the directories", args)
		}
	}
}

// mkdirs stands in for paths.Paths.Ensure in tests: the real one restricts the
// ACL on Windows, which would lock a non-admin test user out of its temp dir.
func mkdirs(p paths.Paths) error {
	for _, d := range []string{p.Config, p.Data, p.Logs, p.Secrets, p.Run} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			return err
		}
	}
	return nil
}
