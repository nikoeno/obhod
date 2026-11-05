package desktop

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/nikoejento/obhod/internal/lib/route"
	"github.com/nikoejento/obhod/internal/pc"
)

func (e *env) connected() {
	e.t.Helper()
	e.eng.setState(pc.StateConnected)
}

func TestGetDesyncGoldens(t *testing.T) {
	e := newEnv(t)
	r := e.get("/api/v1/desync")
	if r.code != 200 {
		t.Fatalf("%d %s", r.code, r.body)
	}
	golden(t, "desync_preset", r.body)

	e.eng.desy.mu.Lock()
	e.eng.desy.mode, e.eng.desy.preset, e.eng.desy.searching = pc.DesyncModeAuto, "alt3-hostfakesplit", true
	e.eng.desy.last = &pc.DesyncSearch{At: t1, Chosen: "alt3-hostfakesplit", Outcome: "chosen",
		Tried: []pc.DesyncTry{{Preset: "alt2-multisplit", Fixed: 0, Total: 3}, {Preset: "alt3-hostfakesplit", Fixed: 3, Total: 3}}}
	e.eng.desy.mu.Unlock()
	r = e.get("/api/v1/desync")
	golden(t, "desync_auto_searching", r.body)

	e.eng.desy.mu.Lock()
	e.eng.desy.mode, e.eng.desy.preset, e.eng.desy.searching = pc.DesyncModeCustom, "custom", false
	e.eng.desy.last = &pc.DesyncSearch{At: t1, Outcome: "none"}
	e.eng.desy.mu.Unlock()
	r = e.get("/api/v1/desync")
	golden(t, "desync_custom_nothing_fixed", r.body)
	if !strings.Contains(string(r.body), `"tried": []`) && !strings.Contains(string(r.body), `"tried":[]`) {
		t.Errorf("tried must be [] and never null: %s", r.body)
	}
	if r := e.do("GET", "/api/v1/desync", `{}`); r.code != 400 {
		t.Errorf("GET with a body: %d", r.code)
	}
}

func TestGetDesyncPresetsGolden(t *testing.T) {
	e := newEnv(t)
	r := e.get("/api/v1/desync/presets")
	if r.code != 200 {
		t.Fatalf("%d %s", r.code, r.body)
	}
	golden(t, "desync_presets", r.body)
	var list []map[string]any
	r.json(t, &list)
	for _, o := range list {
		if len(o) != 3 {
			t.Errorf("preset object has %d fields: %v", len(o), o)
		}
	}
}

func TestPostDesync(t *testing.T) {
	e := newEnv(t)
	r := e.do("POST", "/api/v1/desync", `{"preset":"alt3-hostfakesplit"}`)
	if r.code != 200 {
		t.Fatalf("%d %s", r.code, r.body)
	}
	golden(t, "desync_set_preset", r.body)
	r = e.do("POST", "/api/v1/desync", `{"preset":"auto"}`)
	if r.code != 200 || !strings.Contains(string(r.body), `"mode":"auto"`) {
		t.Fatalf("%d %s", r.code, r.body)
	}
	if got := e.eng.callCount("SetDesync"); got != 2 {
		t.Errorf("SetDesync called %d times", got)
	}
}

func TestPostDesyncValidation(t *testing.T) {
	e := newEnv(t)
	cases := []struct {
		name, body string
		code       int
		errCode    string
		golden     string
	}{
		{"no body", ``, 400, "bad_request", ""},
		{"empty object", `{}`, 400, "bad_request", "error_desync_preset_missing"},
		{"unknown field", `{"preset":"auto","x":1}`, 400, "bad_request", ""},
		{"wrong type", `{"preset":5}`, 400, "bad_request", ""},
		{"unknown preset", `{"preset":"nope"}`, 422, "invalid_input", "error_desync_invalid_preset"},
		{"path as preset", `{"preset":"../fake/x.bin"}`, 422, "invalid_input", ""},
	}
	for _, c := range cases {
		r := e.do("POST", "/api/v1/desync", c.body)
		if r.code != c.code || r.errCode(t) != c.errCode {
			t.Errorf("%s: %d %s", c.name, r.code, r.body)
			continue
		}
		if c.golden != "" {
			golden(t, c.golden, r.body)
		}
	}
	if r := e.do("POST", "/api/v1/desync", `{"preset":"auto"}`, "Content-Type", "text/plain"); r.code != 415 {
		t.Errorf("wrong Content-Type: %d", r.code)
	}
	big := `{"preset":"auto","pad":"` + strings.Repeat("a", 70<<10) + `"}`
	if r := e.do("POST", "/api/v1/desync", big); r.code != 413 {
		t.Errorf("big body: %d", r.code)
	}
	if st := e.eng.Desync(); st.Mode != pc.DesyncModePreset {
		t.Errorf("a rejected request changed the choice: %+v", st)
	}
}

func TestPostDesyncProgramMissingMapsLikeConnect(t *testing.T) {
	e := newEnv(t)
	// The engine's error for a missing payload file classifies as program_missing, as for Connect.
	e.eng.failDesyncCall("SetDesync", fmt.Errorf("%w: /secret/fake/x.bin", pc.ErrProgramMissing))
	r := e.do("POST", "/api/v1/desync", `{"preset":"alt3-hostfakesplit"}`)
	if r.code != 422 || r.errCode(t) != "program_missing" {
		t.Fatalf("%d %s", r.code, r.body)
	}
	golden(t, "error_desync_program_missing", r.body)
	if strings.Contains(string(r.body), "/secret") {
		t.Fatal("the error text leaked")
	}
	e.eng.failDesyncCall("SetDesync", errors.New("disk on fire at /secret/path"))
	r = e.do("POST", "/api/v1/desync", `{"preset":"auto"}`)
	if r.code != 500 || r.errCode(t) != "internal" || strings.Contains(string(r.body), "/secret/path") {
		t.Fatalf("%d %s", r.code, r.body)
	}
}

func TestPostDesyncSearch(t *testing.T) {
	e := newEnv(t)
	r := e.post("/api/v1/desync/search")
	if r.code != 409 || r.errCode(t) != "busy" {
		t.Fatalf("not connected: %d %s", r.code, r.body)
	}
	golden(t, "error_desync_search_busy", r.body)

	e.connected()
	r = e.post("/api/v1/desync/search")
	if r.code != 409 || r.errCode(t) != "not_auto" {
		t.Fatalf("named preset: %d %s", r.code, r.body)
	}
	golden(t, "error_desync_not_auto", r.body)

	e.do("POST", "/api/v1/desync", `{"preset":"auto"}`)
	e.eng.desy.mu.Lock()
	e.eng.desy.noDomains = true
	e.eng.desy.mu.Unlock()
	r = e.post("/api/v1/desync/search")
	if r.code != 409 || r.errCode(t) != "no_domains" {
		t.Fatalf("no sites: %d %s", r.code, r.body)
	}
	golden(t, "error_desync_no_domains", r.body)

	e.eng.desy.mu.Lock()
	e.eng.desy.noDomains = false
	e.eng.desy.mu.Unlock()
	r = e.post("/api/v1/desync/search")
	if r.code != 202 {
		t.Fatalf("%d %s", r.code, r.body)
	}
	golden(t, "desync_search_started", r.body)
	r = e.post("/api/v1/desync/search")
	if r.code != 409 || r.errCode(t) != "busy" {
		t.Fatalf("second search: %d %s", r.code, r.body)
	}
	e.eng.failDesyncCall("StartDesyncSearch", errors.New("boom /secret"))
	if r := e.post("/api/v1/desync/search"); r.code != 500 || strings.Contains(string(r.body), "/secret") {
		t.Fatalf("%d %s", r.code, r.body)
	}
}

func TestDesyncMethods(t *testing.T) {
	e := newEnv(t)
	if r := e.do("PUT", "/api/v1/desync", `{}`); r.code != 405 || !strings.Contains(r.hdr.Get("Allow"), "POST") || !strings.Contains(r.hdr.Get("Allow"), "GET") {
		t.Errorf("PUT /desync: %d %q", r.code, r.hdr.Get("Allow"))
	}
	if r := e.do("GET", "/api/v1/desync/search", ""); r.code != 405 {
		t.Errorf("GET /desync/search: %d", r.code)
	}
	if r := e.do("POST", "/api/v1/desync/presets", ""); r.code != 405 {
		t.Errorf("POST /desync/presets: %d", r.code)
	}
	if r := e.get("/api/v1/desync/other"); r.code != 404 {
		t.Errorf("unknown path: %d", r.code)
	}
}

func TestDesyncEventsOnTheStream(t *testing.T) {
	e := newEnv(t)
	s := e.openStream()
	s.until(t, "state")
	e.eng.publish(pc.Event{Kind: pc.EventDesync, Desync: pc.DesyncEvent{Phase: "searching", Total: 6}})
	e.eng.publish(pc.Event{Kind: pc.EventDesync, Desync: pc.DesyncEvent{Phase: "trying", Index: 2, Total: 6, Preset: "alt3-hostfakesplit"}})
	e.eng.publish(pc.Event{Kind: pc.EventDesync, Desync: pc.DesyncEvent{Phase: "done", Outcome: "none", Fixed: 0, Of: 4}})
	e.eng.publish(pc.Event{Kind: pc.EventDesync, Desync: pc.DesyncEvent{Phase: "done", Outcome: "chosen", Chosen: "alt3-hostfakesplit", Fixed: 3, Of: 4}})
	want := []string{
		`{"phase":"searching","total":6}`,
		`{"phase":"trying","index":2,"total":6,"preset":"alt3-hostfakesplit"}`,
		`{"phase":"done","outcome":"none","chosen":"","fixed":0,"of":4}`,
		`{"phase":"done","outcome":"chosen","chosen":"alt3-hostfakesplit","fixed":3,"of":4}`,
	}
	for i, w := range want {
		ev := s.until(t, "desync")
		if ev.Data != w {
			t.Errorf("event %d: %s, want %s", i, ev.Data, w)
		}
	}
}

func TestRoutesCarryTheDesyncState(t *testing.T) {
	e := newEnv(t)
	e.eng.fillFixtures()
	e.eng.mu.Lock()
	e.eng.routes = append(e.eng.routes,
		pc.RouteInfo{Domain: "verified.example", Route: route.Desync, Source: route.SourceChecked, Since: t0, LastCheck: t1, Desync: pc.DesyncVerified},
		pc.RouteInfo{Domain: "failed.example", Route: route.Tunnel, Source: route.SourceChecked, Since: t0, LastCheck: t1, Desync: pc.DesyncFailed, Degraded: true})
	e.eng.mu.Unlock()
	r := e.get("/api/v1/routes")
	golden(t, "routes_desync_states", r.body)
	var list []map[string]any
	r.json(t, &list)
	for _, o := range list {
		d, has := o["desync"]
		if has && d == "" {
			t.Errorf("an empty desync must be absent: %v", o)
		}
		if o["domain"] == "example.org" && has {
			t.Errorf("a plain route has a desync field: %v", o)
		}
	}
}

func TestDesyncResponsesHoldNoSiteNames(t *testing.T) {
	e := newEnv(t)
	e.eng.fillFixtures()
	e.eng.desy.mu.Lock()
	e.eng.desy.mode, e.eng.desy.preset = pc.DesyncModeAuto, "alt2-multisplit"
	e.eng.desy.last = &pc.DesyncSearch{At: t1, Chosen: "alt2-multisplit", Outcome: "chosen", Tried: []pc.DesyncTry{{Preset: "alt2-multisplit", Fixed: 1, Total: 1}}}
	e.eng.desy.mu.Unlock()
	for _, p := range []string{"/api/v1/desync", "/api/v1/desync/presets"} {
		if b := e.get(p).body; strings.Contains(string(b), "video.example") || strings.Contains(string(b), "example.org") {
			t.Errorf("%s names a site: %s", p, b)
		}
	}
	// And no request body reaches the log.
	e.do("POST", "/api/v1/desync", `{"preset":"MARKER-IN-BODY"}`)
	if strings.Contains(e.log.String(), "MARKER-IN-BODY") {
		t.Error("a request body reached the log")
	}
}

func TestDesyncDuringShutdown(t *testing.T) {
	e := newEnv(t)
	e.cancel()
	waitFor(t, "shutdown", func() bool { return e.srv.shuttingDown() })
	for _, p := range []string{"/api/v1/desync", "/api/v1/desync/presets"} {
		if r := e.get(p); r.code != 503 || r.errCode(t) != "engine_unreachable" {
			t.Errorf("%s: %d", p, r.code)
		}
	}
}
