package lab

import (
	"errors"
	"net"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/nikoejento/obhod/internal/lib/route"
)

func TestMergeYAML(t *testing.T) {
	base := []byte("a:\n  b: 1\n  c: [x, y]\n  d: {e: 2}\nf: keep\n")
	over := []byte("a:\n  c: [z]\n  d: {g: 3}\nh: new\n")
	out, err := mergeYAML(base, over)
	if err != nil {
		t.Fatal(err)
	}
	got, err := decodeMapping(out)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]any{
		"a": map[string]any{"b": 1, "c": []any{"z"}, "d": map[string]any{"e": 2, "g": 3}},
		"f": "keep",
		"h": "new",
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("merged = %#v\nwant %#v", got, want)
	}
	if _, err := mergeYAML(base, []byte("a: [unclosed")); err == nil {
		t.Error("invalid override: want an error")
	}
	if out, err := mergeYAML(base, nil); err != nil || yamlString(out, "f", "") != "keep" {
		t.Errorf("empty override: %v, %s", err, out)
	}
}

func TestSplitterDefaults(t *testing.T) {
	d := splitterDefaults{
		Root: "/tmp/root", DoH: "https://198.51.100.2/dns-query", Echo: "http://198.51.100.3/",
		Controls: []string{"control-a.test"}, Canaries: []string{"canary.test"}, Neutral: []string{"neutral.test"},
		SingBox: "/opt/sing-box", StatusListen: "127.0.0.1:19091",
		Candidates: "/tmp/root/lists/candidates.txt", Proxy: "/tmp/root/lists/proxy.txt", Block: "/tmp/root/lists/block.txt",
	}
	out, err := d.yaml()
	if err != nil {
		t.Fatal(err)
	}
	for path, want := range map[string]string{
		"paths.root":                   "/tmp/root",
		"analyzer.options.fingerprint": "firefox",
		"singbox.dns.direct_doh":       d.DoH,
		"checker.schedule.interval":    "15s",
		"checker.unknowndefault":       "tunnel",
		"status.listen":                "127.0.0.1:19091",
	} {
		if got := yamlString(out, path, "<absent>"); got != want {
			t.Errorf("%s = %q, want %q", path, got, want)
		}
	}
	if v, ok := yamlPath(out, "checker.decide.confirmations"); !ok || v != labConfirmations {
		t.Errorf("checker.decide.confirmations = %v", v)
	}
	if _, ok := yamlPath(out, "desync"); ok {
		t.Error("desync set without a binary")
	}
	merged, err := mergeYAML(out, []byte("checker:\n  schedule:\n    interval: 1m\n"))
	if err != nil {
		t.Fatal(err)
	}
	if yamlString(merged, "checker.schedule.interval", "") != "1m" || yamlString(merged, "checker.schedule.mingap", "") != "10s" {
		t.Errorf("merged schedule:\n%s", merged)
	}
}

func TestListFile(t *testing.T) {
	out, err := listFile([]string{"B.test.", "a.test", "b.test"})
	if err != nil {
		t.Fatal(err)
	}
	if got := string(out); !strings.HasSuffix(got, "a.test\nb.test\n") || strings.Count(got, "b.test") != 1 {
		t.Errorf("list = %q", got)
	}
	if _, err := listFile([]string{"bad domain"}); err == nil {
		t.Error("invalid domain: want an error")
	}
}

func TestSeedDecisions(t *testing.T) {
	now := time.Date(2026, 10, 11, 12, 0, 0, 0, time.UTC)
	b, err := seedDecisions([]string{"asn:64500", "gw:0123456789abcdef"},
		map[string]route.Route{"Site.test": route.Desync, "other.test": route.Direct}, now)
	if err != nil {
		t.Fatal(err)
	}
	f, err := parseStore(b)
	if err != nil {
		t.Fatal(err)
	}
	if len(f.Networks) != 2 {
		t.Fatalf("networks = %v", f.Networks)
	}
	for id, n := range f.Networks {
		d, ok := n.Decisions["site.test"]
		if !ok || d.Route != route.Desync || d.Source != route.SourceChecked || d.Domain != "site.test" ||
			!d.ExpiresAt.Equal(now.Add(seedTTL)) || !n.LastSeen.Equal(now) {
			t.Errorf("%s: %+v", id, n)
		}
	}
	// The file uses the store's documented keys (schema 1).
	for _, key := range []string{`"schema": 1`, `"networks"`, `"last_seen"`, `"decisions"`, `"decided_at"`, `"checked_at"`, `"expires_at"`} {
		if !strings.Contains(string(b), key) {
			t.Errorf("decisions.json lacks %s:\n%s", key, b)
		}
	}
	if _, err := seedDecisions([]string{"asn:1"}, map[string]route.Route{"x.test": route.Proxy}, now); err == nil {
		t.Error("proxy decision: want an error")
	}
	if _, err := seedDecisions(nil, nil, now); err == nil {
		t.Error("no network id: want an error")
	}
	if _, err := parseStore([]byte(`{"schema":2}`)); err == nil {
		t.Error("schema 2: want an error")
	}
}

func TestNetworkIDs(t *testing.T) {
	mac, _ := net.ParseMAC("02:00:00:00:00:01")
	gw := gatewayNetworkID("salt", mac, "")
	// sha256("salt|02:00:00:00:00:01|")[:16 hex]
	if gw != "gw:759a73d4a8b79e77" {
		t.Errorf("gateway id %q", gw)
	}
	if gatewayNetworkID("other", mac, "") == gw {
		t.Error("the salt does not change the id")
	}
	if got := networkIDs(64500, gw, false); !reflect.DeepEqual(got, []string{"asn:64500", gw}) {
		t.Errorf("ids = %q", got)
	}
	if got := networkIDs(64500, gw, true); got[0] != "asn:64500/"+gw {
		t.Errorf("ids with gateway = %q", got)
	}
}

func TestSeedDecisionSets(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	b, err := seedDecisionSets([]decisionSet{
		{IDs: []string{"asn:1", "gw:a"}, Decisions: map[string]route.Route{"a.test": route.Direct}},
		{IDs: []string{"asn:2"}, Decisions: map[string]route.Route{"a.test": route.Desync, "b.test": route.Tunnel}},
	}, now)
	if err != nil {
		t.Fatal(err)
	}
	f, err := parseStore(b)
	if err != nil {
		t.Fatal(err)
	}
	if len(f.Networks) != 3 || f.Networks["asn:1"].Decisions["a.test"].Route != route.Direct ||
		f.Networks["gw:a"].Decisions["a.test"].Route != route.Direct ||
		f.Networks["asn:2"].Decisions["a.test"].Route != route.Desync || len(f.Networks["asn:2"].Decisions) != 2 {
		t.Errorf("store = %s", b)
	}
	if _, err := seedDecisionSets([]decisionSet{{IDs: []string{"x"}}, {IDs: []string{"x"}}}, now); err == nil {
		t.Error("the same network ID in two sets: want an error")
	}
	if _, err := seedDecisionSets([]decisionSet{{Decisions: map[string]route.Route{"a.test": route.Direct}}}, now); err == nil {
		t.Error("no network ID: want an error")
	}
	if _, err := seedDecisionSets([]decisionSet{{IDs: []string{"x"}, Decisions: map[string]route.Route{"a.test": route.Block}}}, now); err == nil {
		t.Error("a block decision: want an error")
	}
}

func TestLogLineTimes(t *testing.T) {
	log := `time=2026-10-14T12:00:00.250+02:00 level=INFO msg=process name=nfqws2 state=starting
time=2026-10-14T12:00:01.000+02:00 level=INFO msg="sing-box restarted with a new config"
time=2026-10-14T12:00:09.500+02:00 level=INFO msg=process name=nfqws2 state=starting
no time here process name=nfqws2 state=starting
`
	got := logLineTimes(log, "name=nfqws2 state=starting")
	if len(got) != 2 || got[1].Sub(got[0]) != 9250*time.Millisecond {
		t.Errorf("times = %v", got)
	}
	if got := logLineTimes(log, "sing-box restarted"); len(got) != 1 || got[0].Nanosecond() != 0 {
		t.Errorf("sing-box times = %v", got)
	}
	if got := logLineTimes(log, "absent"); len(got) != 0 {
		t.Errorf("absent = %v", got)
	}
}

func TestHostlistHas(t *testing.T) {
	hl := []byte("a.test\nb.example.test\n")
	if !hostlistHas(hl, "a.test") || !hostlistHas(hl, "b.example.test") || hostlistHas(hl, "example.test") || hostlistHas(hl, "b.test") {
		t.Error("hostlistHas matches whole lines only")
	}
}

func TestCurlReset(t *testing.T) {
	for msg, want := range map[string]bool{
		"exit status 35: curl: (35) TLS connect error: unexpected eof while reading": true,
		"exit status 56: curl: (56) Recv failure: Connection reset by peer":          true,
		"exit status 52: empty reply":                                                true, "exit status 55: x": true,
		"exit status 28: curl: (28) Connection timed out": false,
		"exit status 7: refused":                          false,
	} {
		if got := isCurlReset(errors.New(msg)); got != want {
			t.Errorf("isCurlReset(%q) = %v", msg, got)
		}
	}
	if isCurlReset(nil) || !isCurlTimeout(errors.New("exit status 28: x")) || isCurlTimeout(errors.New("exit status 35")) {
		t.Error("nil / timeout classification")
	}
}
