package paths

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

type fakeProbe struct {
	goos   string
	env    map[string]string
	exists map[string]bool
}

func (f fakeProbe) GOOS() string           { return f.goos }
func (f fakeProbe) Getenv(k string) string { return f.env[k] }
func (f fakeProbe) Exists(p string) bool   { return f.exists[p] }

func TestFromRoot(t *testing.T) {
	p := FromRoot("r")
	want := Paths{Config: "r", Data: filepath.Join("r", "data"), Logs: filepath.Join("r", "logs"), Secrets: filepath.Join("r", "secrets"), Run: filepath.Join("r", "run")}
	if p != want {
		t.Fatalf("got %+v", p)
	}
}

func TestResolve(t *testing.T) {
	win, err := resolve("splitter", fakeProbe{goos: "windows", env: map[string]string{"ProgramData": `C:\PD`}})
	if err != nil || win != FromRoot(filepath.Join(`C:\PD`, "splitter")) {
		t.Errorf("windows: %+v %v", win, err)
	}
	if _, err := resolve("splitter", fakeProbe{goos: "windows"}); err == nil {
		t.Error("windows without ProgramData must fail")
	}
	lin, err := resolve("splitter", fakeProbe{goos: "linux"})
	want := Paths{Config: "/etc/splitter", Data: "/var/lib/splitter", Logs: "/var/log/splitter", Secrets: "/etc/splitter/secrets", Run: "/run/splitter"}
	if err != nil || lin != want {
		t.Errorf("linux: %+v %v", lin, err)
	}
	if _, err := resolve("splitter", fakeProbe{goos: "linux", exists: map[string]bool{"/etc/openwrt_release": true}}); !errors.Is(err, ErrUnsupported) {
		t.Errorf("openwrt must fail loudly until implemented: %v", err)
	}
	if _, err := resolve("splitter", fakeProbe{goos: "plan9"}); !errors.Is(err, ErrUnsupported) {
		t.Errorf("plan9: %v", err)
	}
}

func TestResolveRejectsBadAppNames(t *testing.T) {
	for _, app := range []string{"", ".", "..", "../x", "a/b", `a\b`, "a\x00b"} {
		if _, err := resolve(app, fakeProbe{goos: "linux"}); err == nil {
			t.Errorf("%q must be rejected", app)
		}
	}
}

func TestEnsure(t *testing.T) {
	p := FromRoot(filepath.Join(t.TempDir(), "app"))
	var seen []string
	acl := func(d string) error { seen = append(seen, d); return nil }
	for i := 0; i < 2; i++ { // idempotent
		if err := p.ensure(acl); err != nil {
			t.Fatal(err)
		}
	}
	for _, d := range []string{p.Config, p.Data, p.Logs, p.Secrets, p.Run} {
		if fi, err := os.Stat(d); err != nil || !fi.IsDir() {
			t.Errorf("%s not created: %v", d, err)
		}
	}
	if len(seen) != 10 {
		t.Errorf("acl called %d times, want 10", len(seen))
	}
	if runtime.GOOS != "windows" {
		if fi, _ := os.Stat(p.Secrets); fi.Mode().Perm() != 0o700 {
			t.Errorf("secrets mode %v", fi.Mode().Perm())
		}
		if fi, _ := os.Stat(p.Data); fi.Mode().Perm() != 0o750 {
			t.Errorf("data mode %v", fi.Mode().Perm())
		}
	}
}

func TestEnsureACLFailureIsSentinelAndDirsExist(t *testing.T) {
	p := FromRoot(filepath.Join(t.TempDir(), "app"))
	err := p.ensure(func(string) error { return errors.New("denied") })
	if !errors.Is(err, ErrACL) {
		t.Fatalf("want ErrACL, got %v", err)
	}
	if _, serr := os.Stat(p.Secrets); serr != nil {
		t.Errorf("dirs must exist despite ACL failure: %v", serr)
	}
}

func TestEnsureCreateFailure(t *testing.T) {
	file := filepath.Join(t.TempDir(), "f")
	if err := os.WriteFile(file, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	err := FromRoot(filepath.Join(file, "sub")).ensure(func(string) error { return nil })
	if err == nil || errors.Is(err, ErrACL) {
		t.Fatalf("want a plain create error, got %v", err)
	}
}

func TestDefaultSmoke(t *testing.T) {
	p, err := Default("obhod-test")
	if err != nil {
		t.Skipf("no default layout here: %v", err)
	}
	if p.Config == "" || p.Secrets == "" {
		t.Errorf("empty paths: %+v", p)
	}
}
