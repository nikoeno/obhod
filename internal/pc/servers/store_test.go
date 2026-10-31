package servers

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

var testNow = time.Date(2026, 10, 31, 10, 0, 0, 123, time.UTC)

// seqID returns ids 0000000000000001, 0000000000000002, ...
func seqID() func() string {
	n := 0
	return func() string { n++; return fmt.Sprintf("%016x", n) }
}

func newTestStore(t *testing.T, check Checker) (*Store, string) {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "servers")
	s, err := New(dir, nil, check, seqID(), WithClock(func() time.Time { return testNow }))
	if err != nil {
		t.Fatal(err)
	}
	return s, dir
}

func trojanLink(i int) string {
	return fmt.Sprintf("trojan://fake-password-%d@192.0.2.%d:443#T%d", i, i, i)
}

func TestAddListOutboundAndFiles(t *testing.T) {
	s, dir := newTestStore(t, nil)
	srv, err := s.Add(context.Background(), trojanLink(1), "")
	if err != nil {
		t.Fatal(err)
	}
	want := Server{ID: "0000000000000001", Name: "T1", Protocol: "trojan", Host: "192.0.2.1", Port: 443, Engine: "singbox", AddedAt: testNow.Truncate(time.Second)}
	if srv != want {
		t.Fatalf("Add = %+v, want %+v", srv, want)
	}
	list, _ := s.List()
	if len(list) != 1 || list[0] != want {
		t.Fatalf("List = %+v", list)
	}
	ob, err := s.Outbound(srv.ID)
	if err != nil || !strings.Contains(string(ob), `"password":"fake-password-1"`) {
		t.Fatalf("Outbound = %s, %v", ob, err)
	}
	// The index has the summary and no credential.
	idx, err := os.ReadFile(filepath.Join(dir, "index.json"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(idx), "fake-password") {
		t.Errorf("index.json holds a credential:\n%s", idx)
	}
	var parsed indexFile
	if err := json.Unmarshal(idx, &parsed); err != nil || parsed.Active != "" || len(parsed.Servers) != 1 {
		t.Fatalf("index = %s (%v)", idx, err)
	}
	// A new store on the same directory sees the same list.
	s2, err := New(dir, nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if list2, _ := s2.List(); len(list2) != 1 || list2[0] != want {
		t.Fatalf("reopened List = %+v", list2)
	}
	if empty, _ := newTestStoreList(t); empty == nil || len(empty) != 0 {
		t.Fatalf("an empty store lists %#v, want a non-nil empty slice", empty)
	}
}

func newTestStoreList(t *testing.T) ([]Server, error) {
	s, _ := newTestStore(t, nil)
	return s.List()
}

func TestNames(t *testing.T) {
	s, _ := newTestStore(t, nil)
	ctx := context.Background()
	a, _ := s.Add(ctx, "trojan://p1@192.0.2.1:443", "")
	b, _ := s.Add(ctx, "trojan://p2@192.0.2.2:443#From%20link", "")
	c, _ := s.Add(ctx, "trojan://p3@192.0.2.3:443#ignored", "Mine")
	if a.Name != "Server 1" || b.Name != "From link" || c.Name != "Mine" {
		t.Fatalf("names: %q %q %q", a.Name, b.Name, c.Name)
	}
	for _, bad := range []string{strings.Repeat("x", 61), "two\nlines", "\x00"} {
		if _, err := s.Add(ctx, "trojan://p9@192.0.2.9:443", bad); !errors.Is(err, ErrInvalidInput) {
			t.Errorf("name %q: %v", bad, err)
		}
	}
	// "Server N" skips a name that is taken.
	d, _ := s.Add(ctx, "trojan://p4@192.0.2.4:443", "Server 5")
	e, _ := s.Add(ctx, "trojan://p5@192.0.2.5:443", "")
	if d.Name != "Server 5" || e.Name == "Server 5" {
		t.Fatalf("default name collided: %q %q", d.Name, e.Name)
	}
}

func TestDuplicateAndLimit(t *testing.T) {
	s, _ := newTestStore(t, nil)
	ctx := context.Background()
	if _, err := s.Add(ctx, "trojan://same@Example.com:443", ""); err != nil {
		t.Fatal(err)
	}
	// Same protocol, host (case-insensitive), port and credential, another option and name.
	if _, err := s.Add(ctx, "trojan://same@example.com:443?sni=other.example#other", "Other"); !errors.Is(err, ErrDuplicate) {
		t.Fatalf("duplicate: %v", err)
	}
	// A different credential, port or protocol is not a duplicate.
	for _, in := range []string{
		"trojan://different@example.com:443", "trojan://same@example.com:444",
		`{"type":"hysteria2","server":"example.com","server_port":443,"password":"same","tls":{"enabled":true}}`,
	} {
		if _, err := s.Add(ctx, in, ""); err != nil {
			t.Fatalf("%s: %v", in, err)
		}
	}
	for i := 1; i <= MaxServers; i++ {
		_, err := s.Add(ctx, trojanLink(i), "")
		list, _ := s.List()
		if len(list) < MaxServers && err != nil {
			t.Fatalf("add %d: %v", i, err)
		}
		if len(list) == MaxServers {
			break
		}
	}
	if _, err := s.Add(ctx, "trojan://one-more@192.0.2.200:443", ""); !errors.Is(err, ErrLimit) {
		t.Fatalf("limit: %v", err)
	}
	// A duplicate at the limit is still reported as a duplicate.
	if _, err := s.Add(ctx, "trojan://same@example.com:443", ""); !errors.Is(err, ErrDuplicate) {
		t.Fatalf("duplicate at the limit: %v", err)
	}
}

func TestDryRun(t *testing.T) {
	var seen []json.RawMessage
	boom := errors.New("sing-box said: SECRET-MARKER-DRYRUN")
	var fail error
	s, dir := newTestStore(t, func(_ context.Context, ob json.RawMessage) error {
		seen = append(seen, ob)
		return fail
	})
	ctx := context.Background()
	if _, err := s.Add(ctx, trojanLink(1), ""); err != nil || len(seen) != 1 {
		t.Fatalf("add: %v, checker calls %d", err, len(seen))
	}
	fail = boom
	_, err := s.Add(ctx, trojanLink(2), "")
	if !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("rejected by the dry run: %v", err)
	}
	if strings.Contains(err.Error(), "SECRET-MARKER") {
		t.Errorf("the checker's text reached the error: %v", err)
	}
	list, _ := s.List()
	if len(list) != 1 {
		t.Fatalf("a rejected server was stored: %+v", list)
	}
	ents, _ := os.ReadDir(dir)
	if len(ents) != 2 { // index.json and the first server
		t.Errorf("files left behind: %v", ents)
	}
	// A cancelled context passes through unchanged.
	cctx, cancel := context.WithCancel(ctx)
	cancel()
	fail = context.Canceled
	if _, err := s.Add(cctx, trojanLink(3), ""); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled: %v", err)
	}
	// Verify and Test use the same checker.
	fail = nil
	if err := s.Verify(ctx, list[0].ID); err != nil {
		t.Fatal(err)
	}
	fail = boom
	if err := s.Verify(ctx, list[0].ID); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("Verify: %v", err)
	}
	proto, err := s.Test(ctx, "", trojanLink(7))
	if proto != "trojan" || !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("Test of input: %q %v", proto, err)
	}
	fail = nil
	if proto, err = s.Test(ctx, list[0].ID, ""); proto != "trojan" || err != nil {
		t.Fatalf("Test of id: %q %v", proto, err)
	}
	if _, err = s.Test(ctx, "", "trojan://@x:1"); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("Test of bad input: %v", err)
	}
	if _, err = s.Test(ctx, "ffffffffffffffff", ""); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Test of unknown id: %v", err)
	}
	if n, _ := s.List(); len(n) != 1 {
		t.Error("Test changed the list")
	}
}

func TestActivateExportDelete(t *testing.T) {
	s, dir := newTestStore(t, nil)
	ctx := context.Background()
	a, _ := s.Add(ctx, trojanLink(1), "")
	b, _ := s.Add(ctx, trojanLink(2), "")
	tunnel := filepath.Join(t.TempDir(), "tunnel")
	if err := os.WriteFile(tunnel, []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}

	if err := s.ExportTo(b.ID, tunnel); err != nil {
		t.Fatal(err)
	}
	got, _ := os.ReadFile(tunnel)
	want, _ := s.Outbound(b.ID)
	if strings.TrimSpace(string(got)) != string(want) {
		t.Fatalf("tunnel file = %s, want %s", got, want)
	}
	if err := s.SetActive(b.ID); err != nil {
		t.Fatal(err)
	}
	if s.Active() != b.ID {
		t.Fatal("Active did not change")
	}
	list, _ := s.List()
	if list[0].Active || !list[1].Active {
		t.Fatalf("active marks: %+v", list)
	}
	if err := s.SetActive("nope"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("SetActive unknown: %v", err)
	}
	// The mark survives a reopen.
	s2, _ := New(dir, nil, nil, nil)
	if s2.Active() != b.ID {
		t.Error("the active mark was not stored")
	}

	// Deleting the active server clears the mark; deleting another keeps it.
	if err := s.Delete(a.ID); err != nil || s.Active() != b.ID {
		t.Fatalf("delete other: %v active %q", err, s.Active())
	}
	if err := s.Delete(b.ID); err != nil || s.Active() != "" {
		t.Fatalf("delete active: %v active %q", err, s.Active())
	}
	if _, err := os.Stat(filepath.Join(dir, b.ID+".json")); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("server file not removed: %v", err)
	}
	if err := s.Delete(b.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("delete twice: %v", err)
	}
	if err := s.ExportTo(b.ID, tunnel); !errors.Is(err, ErrNotFound) {
		t.Fatalf("export deleted: %v", err)
	}
	if _, err := s.Outbound(b.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("outbound deleted: %v", err)
	}
	if list, _ := s.List(); len(list) != 0 {
		t.Fatalf("list after deletes: %+v", list)
	}
}

func TestIDsAreNotPaths(t *testing.T) {
	s, dir := newTestStore(t, nil)
	secret := filepath.Join(filepath.Dir(dir), "secret.json")
	if err := os.WriteFile(secret, []byte(`{"type":"trojan"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"../secret", "..\\secret", "", "ZZZZZZZZZZZZZZZZ", "0000000000000001", strings.Repeat("a", 17)} {
		if _, err := s.Outbound(id); !errors.Is(err, ErrNotFound) {
			t.Errorf("Outbound(%q) = %v", id, err)
		}
		if err := s.Delete(id); !errors.Is(err, ErrNotFound) {
			t.Errorf("Delete(%q) = %v", id, err)
		}
	}
	if _, err := os.Stat(secret); err != nil {
		t.Error("a file outside the store was touched")
	}
}

func TestRandomID(t *testing.T) {
	seen := map[string]bool{}
	for i := 0; i < 50; i++ {
		id := RandomID()
		if !validID(id) || seen[id] {
			t.Fatalf("bad or repeated id %q", id)
		}
		seen[id] = true
	}
}

func TestModes(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows has no POSIX modes; the secrets directory gets an ACL from paths.Ensure")
	}
	s, dir := newTestStore(t, nil)
	srv, _ := s.Add(context.Background(), trojanLink(1), "")
	tunnel := filepath.Join(t.TempDir(), "tunnel")
	if err := s.ExportTo(srv.ID, tunnel); err != nil {
		t.Fatal(err)
	}
	for path, want := range map[string]fs.FileMode{
		dir: 0o700, filepath.Join(dir, "index.json"): 0o600, filepath.Join(dir, srv.ID+".json"): 0o600, tunnel: 0o600,
	} {
		fi, err := os.Stat(path)
		if err != nil || fi.Mode().Perm() != want {
			t.Errorf("%s: mode %v (%v), want %v", filepath.Base(path), fi.Mode().Perm(), err, want)
		}
	}
}

// failFS fails the write whose path ends with failOn.
type failFS struct {
	FS
	failOn string
}

func (f failFS) WriteFileAtomic(path string, data []byte, perm fs.FileMode) error {
	if strings.HasSuffix(path, f.failOn) {
		return errors.New("disk full")
	}
	return f.FS.WriteFileAtomic(path, data, perm)
}

func TestFailedWritesChangeNothing(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "servers")
	s, err := New(dir, failFS{OSFS(), "index.json"}, nil, seqID())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Add(context.Background(), trojanLink(1), ""); err == nil {
		t.Fatal("Add succeeded though the index could not be written")
	}
	if list, _ := s.List(); len(list) != 0 {
		t.Errorf("memory changed: %+v", list)
	}
	ents, _ := os.ReadDir(dir)
	if len(ents) != 0 {
		t.Errorf("files left behind: %v", ents)
	}

	s2, _ := New(dir, failFS{OSFS(), ".json"}, nil, seqID()) // every write fails
	if _, err := s2.Add(context.Background(), trojanLink(1), ""); err == nil {
		t.Fatal("Add succeeded though nothing can be written")
	}
}

func TestAtomicWriteReplacesWholeFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "f.json")
	fsys := OSFS()
	if err := fsys.WriteFileAtomic(path, []byte(strings.Repeat("a", 5000)), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := fsys.WriteFileAtomic(path, []byte("short"), 0o600); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(path); string(b) != "short" {
		t.Fatalf("content = %q", b)
	}
	ents, _ := os.ReadDir(dir)
	if len(ents) != 1 {
		t.Errorf("temp file left: %v", ents)
	}
	// A write into a directory that does not exist fails and leaves nothing.
	if err := fsys.WriteFileAtomic(filepath.Join(dir, "missing", "f"), []byte("x"), 0o600); err == nil {
		t.Error("write into a missing directory succeeded")
	}
}

func TestLeftoverTempFilesAreRemoved(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "servers")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	for _, n := range []string{"index.json.tmp123", "0000000000000001.json.tmp456"} {
		if err := os.WriteFile(filepath.Join(dir, n), []byte("partial"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := New(dir, nil, nil, nil); err != nil {
		t.Fatal(err)
	}
	if ents, _ := os.ReadDir(dir); len(ents) != 0 {
		t.Errorf("leftovers: %v", ents)
	}
}

func TestCorruptIndex(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "servers")
	_ = os.MkdirAll(dir, 0o700)
	for _, content := range []string{"not json", `{"servers":[{"id":"../x"}]}`} {
		if err := os.WriteFile(filepath.Join(dir, "index.json"), []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := New(dir, nil, nil, nil); err == nil {
			t.Errorf("index %q accepted", content)
		}
	}
}

// TestNoSecretInErrorsOrIndex plants a marker in every secret field of every
// protocol and checks that no error, summary or index file carries it.
func TestNoSecretInErrorsOrIndex(t *testing.T) {
	const m = "SECRETMARKER0123"
	good := []string{
		"vless://" + fakeUUID + "@192.0.2.1:443?security=reality&sni=example.com&pbk=" + m + "&sid=" + m + "#n",
		"trojan://" + m + "@192.0.2.2:443",
		"ss://" + b64("aes-256-gcm:"+m) + "@192.0.2.3:8388",
		"hy2://" + m + "@192.0.2.4:443?obfs=salamander&obfs-password=" + m,
		"vmess://" + b64(`{"add":"192.0.2.5","port":"443","id":"`+fakeUUID+`","ps":"vm"}`),
		"tuic://" + fakeUUID + ":" + m + "@192.0.2.6:443",
		`{"type":"trojan","server":"192.0.2.7","server_port":443,"password":"` + m + `"}`,
	}
	bad := []string{
		"vless://" + m + "@example.com:443",
		"vless://" + fakeUUID + "@example.com:" + m,
		"vless://" + fakeUUID + "@example.com:443?security=reality&pbk=" + m,
		"vless://" + fakeUUID + "@exa mple" + m + ".com:443",
		"vless://" + fakeUUID + "@example.com:443?type=" + m,
		"vless://" + fakeUUID + "@example.com:443?security=" + m,
		"vless://" + fakeUUID + "@example.com:443?security=tls&fp=" + m,
		"vless://" + fakeUUID + "@example.com:443?flow=" + m,
		"trojan://" + m + "@:443",
		"trojan://" + m + "@example.com",
		"trojan://" + m + "@[" + m,
		"ss://" + m + "@example.com:8388",
		"ss://" + b64("nocipher:"+m) + "@example.com:8388",
		"ss://" + b64("aes-256-gcm:"+m) + "@example.com:8388?plugin=" + m,
		"hy2://" + m + "@example.com:" + m,
		"hy2://" + m + "@example.com:443?pinSHA256=" + m,
		"hy2://" + m + "@example.com:443?obfs=" + m,
		"vmess://" + m,
		"vmess://" + b64(m),
		"vmess://" + b64(`{"add":"example.com","port":"443","id":"`+m+`"}`),
		"vmess://" + b64(`{"add":"example.com","port":"443","id":"`+fakeUUID+`","scy":"`+m+`"}`),
		"tuic://" + fakeUUID + ":" + m + "@example.com",
		"tuic://" + m + ":" + m + "@example.com:443",
		"wireguard://" + m + "@example.com:1",
		m + "://" + m,
		m,
		`{"type":"` + m + `","server":"example.com","server_port":443}`,
		`{"type":"trojan","server":"example.com","server_port":"` + m + `","password":"` + m + `"}`,
		`{"type":"trojan","server":"example.com","server_port":443,"password":"` + m + `","detour":"` + m + `"}`,
		`{"type":"vless","server":"example.com","server_port":443,"uuid":"` + m + `","transport":{"type":"` + m + `"}}`,
		`{"type":"trojan","server":"` + m + ` x","server_port":443,"password":"` + m + `"}`,
		`{"type":"trojan","server":"example.com","server_port":443,"password":"` + m + `"} ` + m,
		`{"` + m + `":`,
	}
	s, dir := newTestStore(t, func(_ context.Context, ob json.RawMessage) error { return errors.New("check failed: " + string(ob)) })
	check := func(what string, err error) {
		t.Helper()
		if err != nil && strings.Contains(err.Error(), m) {
			t.Errorf("%s: error carries the marker: %v", what, err)
		}
		if um, ok := err.(interface{ UserMessage() string }); ok && strings.Contains(um.UserMessage(), m) {
			t.Errorf("%s: UserMessage carries the marker", what)
		}
	}
	for _, in := range bad {
		_, err := Parse(in)
		if err == nil {
			t.Errorf("%q parsed", in)
		}
		check("Parse", err)
		_, err = s.Add(context.Background(), in, "")
		check("Add", err)
		_, err = s.Test(context.Background(), "", in)
		check("Test", err)
	}
	// With a failing dry run the good inputs are rejected too, without the marker in the error.
	for _, in := range good {
		if _, err := Parse(in); err != nil {
			t.Fatalf("%q: %v", in, err)
		}
		_, err := s.Add(context.Background(), in, "")
		if !errors.Is(err, ErrInvalidInput) {
			t.Fatalf("Add with a failing dry run: %v", err)
		}
		check("Add (dry run)", err)
	}
	// With a passing dry run they are stored; summaries and the index never show a credential.
	ok, _ := New(filepath.Join(dir, "ok"), nil, nil, nil)
	for _, in := range good {
		if _, err := ok.Add(context.Background(), in, ""); err != nil {
			t.Fatalf("Add %q: %v", in, err)
		}
	}
	list, _ := ok.List()
	b, _ := json.Marshal(list)
	idx, _ := os.ReadFile(filepath.Join(dir, "ok", "index.json"))
	if strings.Contains(string(b), m) || strings.Contains(string(idx), m) {
		t.Errorf("a credential is in the summaries or index.json:\n%s\n%s", b, idx)
	}
}
