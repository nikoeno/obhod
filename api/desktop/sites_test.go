package desktop

import (
	"errors"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/nikoejento/obhod/internal/pc"
)

func fillSites(e *env) {
	e.eng.setSites(fakeRule("example.com", "tunnel", 1), fakeRule("xn--e1afmkfd.xn--p1ai", "block", 2))
}

func TestGetSitesGolden(t *testing.T) {
	e := newEnv(t)
	r := e.get("/api/v1/sites")
	if r.code != 200 || strings.TrimSpace(string(r.body)) != "[]" {
		t.Fatalf("empty list: %d %s", r.code, r.body)
	}
	golden(t, "sites_empty", r.body)
	fillSites(e)
	r = e.get("/api/v1/sites")
	if r.code != 200 {
		t.Fatalf("%d %s", r.code, r.body)
	}
	golden(t, "sites_filled", r.body)
	var list []map[string]any
	r.json(t, &list)
	for _, o := range list {
		if len(o) != 3 {
			t.Errorf("rule object has %d fields: %v", len(o), o)
		}
	}
	e.eng.failSiteCall("Sites", errors.New("disk on fire at /secret/path"))
	r = e.get("/api/v1/sites")
	if r.code != 500 || r.errCode(t) != "internal" || strings.Contains(string(r.body), "/secret/path") {
		t.Fatalf("list error: %d %s", r.code, r.body)
	}
	golden(t, "error_site_internal", r.body)
	if r := e.do("GET", "/api/v1/sites", `{}`); r.code != 400 {
		t.Errorf("GET with a body: %d", r.code)
	}
}

func TestPostSiteCreatesAndUpdates(t *testing.T) {
	e := newEnv(t)
	r := e.do("POST", "/api/v1/sites", `{"domain":"Example.COM","action":"tunnel"}`)
	if r.code != http.StatusCreated {
		t.Fatalf("%d %s", r.code, r.body)
	}
	golden(t, "site_created", r.body)
	r = e.do("POST", "/api/v1/sites", `{"domain":"example.com","action":"direct"}`)
	if r.code != http.StatusOK {
		t.Fatalf("update: %d %s", r.code, r.body)
	}
	golden(t, "site_updated", r.body)
	if got := e.eng.callCount("SetSite"); got != 2 {
		t.Errorf("SetSite called %d times", got)
	}
	list, _ := e.eng.Sites()
	if len(list) != 1 || list[0].Action != "direct" {
		t.Errorf("rules = %+v", list)
	}
}

func TestPostSiteValidation(t *testing.T) {
	e := newEnv(t)
	cases := []struct {
		name, body string
		code       int
		errCode    string
		golden     string
	}{
		{"no body", ``, 400, "bad_request", ""},
		{"empty object", `{}`, 400, "bad_request", "error_site_domain_missing"},
		{"no action", `{"domain":"example.com"}`, 400, "bad_request", "error_site_action_missing"},
		{"no domain", `{"action":"direct"}`, 400, "bad_request", ""},
		{"unknown field", `{"domain":"example.com","action":"direct","x":1}`, 400, "bad_request", ""},
		{"wrong type", `{"domain":5,"action":"direct"}`, 400, "bad_request", ""},
		{"a URL is rejected, not cleaned", `{"domain":"https://example.com/path","action":"direct"}`, 422, "invalid_input", "error_site_invalid_domain"},
		{"a port", `{"domain":"example.com:443","action":"direct"}`, 422, "invalid_input", ""},
		{"an IP", `{"domain":"203.0.113.7","action":"direct"}`, 422, "invalid_input", ""},
		{"inner wildcard", `{"domain":"a.*.example.com","action":"direct"}`, 422, "invalid_input", ""},
		{"spaces", `{"domain":"exa mple.com","action":"direct"}`, 422, "invalid_input", ""},
		{"none is not an action", `{"domain":"example.com","action":"none"}`, 422, "invalid_input", "error_site_invalid_action"},
		{"proxy is not an action", `{"domain":"example.com","action":"proxy"}`, 422, "invalid_input", ""},
	}
	for _, c := range cases {
		r := e.do("POST", "/api/v1/sites", c.body)
		if r.code != c.code || r.errCode(t) != c.errCode {
			t.Errorf("%s: %d %s", c.name, r.code, r.body)
			continue
		}
		if c.golden != "" {
			golden(t, c.golden, r.body)
		}
	}
	if r := e.do("POST", "/api/v1/sites", `{"domain":"example.com","action":"direct"}`, "Content-Type", "text/plain"); r.code != 415 {
		t.Errorf("wrong Content-Type: %d", r.code)
	}
	big := `{"domain":"example.com","action":"direct","pad":"` + strings.Repeat("a", 70<<10) + `"}`
	if r := e.do("POST", "/api/v1/sites", big); r.code != 413 || r.errCode(t) != "too_large" {
		t.Errorf("big body: %d", r.code)
	}
	if list, _ := e.eng.Sites(); len(list) != 0 {
		t.Errorf("a rejected request stored %+v", list)
	}
}

func TestPostSiteLimit(t *testing.T) {
	e := newEnv(t)
	for i := 0; i < 500; i++ {
		r := e.do("POST", "/api/v1/sites", fmt.Sprintf(`{"domain":"d%d.example","action":"direct"}`, i))
		if r.code != 201 {
			t.Fatalf("rule %d: %d %s", i, r.code, r.body)
		}
	}
	r := e.do("POST", "/api/v1/sites", `{"domain":"over.example","action":"direct"}`)
	if r.code != 409 || r.errCode(t) != "limit_reached" {
		t.Fatalf("501st: %d %s", r.code, r.body)
	}
	golden(t, "error_site_limit", r.body)
	// An update at the limit still works.
	if r := e.do("POST", "/api/v1/sites", `{"domain":"d3.example","action":"block"}`); r.code != 200 {
		t.Errorf("update at the limit: %d %s", r.code, r.body)
	}
}

func TestDeleteSite(t *testing.T) {
	e := newEnv(t)
	fillSites(e)
	r := e.del("/api/v1/sites/Example.com")
	if r.code != 200 {
		t.Fatalf("%d %s", r.code, r.body)
	}
	golden(t, "site_deleted", r.body)
	r = e.del("/api/v1/sites/example.com")
	if r.code != 404 || r.errCode(t) != "not_found" {
		t.Fatalf("twice: %d %s", r.code, r.body)
	}
	golden(t, "error_site_not_found", r.body)
	if r := e.del("/api/v1/sites/" + "xn--e1afmkfd.xn--p1ai"); r.code != 200 {
		t.Errorf("punycode name: %d %s", r.code, r.body)
	}
	if r := e.del("/api/v1/sites/%D0%BF%D1%80%D0%B8%D0%BC%D0%B5%D1%80.%D1%80%D1%84"); r.code != 404 {
		t.Errorf("IDN name (normalized to the deleted punycode form): %d %s", r.code, r.body)
	}
	if r := e.del("/api/v1/sites/not%20a%20domain"); r.code != 422 || r.errCode(t) != "invalid_input" {
		t.Errorf("garbage: %d %s", r.code, r.body)
	}
	if r := e.del("/api/v1/sites/a%2Fb"); r.code != 404 {
		t.Errorf("slash in the name: %d", r.code)
	}
	if r := e.del("/api/v1/sites/"); r.code != 404 {
		t.Errorf("empty name: %d", r.code)
	}
	if r := e.do("DELETE", "/api/v1/sites/example.com", `{}`); r.code != 400 {
		t.Errorf("DELETE with a body: %d", r.code)
	}
	if r := e.get("/api/v1/sites/example.com"); r.code != 405 || r.hdr.Get("Allow") != "DELETE" {
		t.Errorf("GET one rule: %d allow=%q", r.code, r.hdr.Get("Allow"))
	}
	if r := e.do("PUT", "/api/v1/sites", `{}`); r.code != 405 || !strings.Contains(r.hdr.Get("Allow"), "POST") || !strings.Contains(r.hdr.Get("Allow"), "GET") {
		t.Errorf("PUT sites: %d allow=%q", r.code, r.hdr.Get("Allow"))
	}
	if r := e.do("DELETE", "/api/v1/sites", ``); r.code != 405 {
		t.Errorf("DELETE the collection: %d", r.code)
	}
}

func TestSiteEndpointsRequireTheAuthAndHost(t *testing.T) {
	e := newEnv(t)
	fillSites(e)
	for _, tg := range allPaths {
		if !strings.Contains(tg.path, "/sites") {
			continue
		}
		r := e.do(tg.method, tg.path, "", "Authorization", "")
		if r.code != 401 || len(r.body) != 0 {
			t.Errorf("%s %s without a token: %d %q", tg.method, tg.path, r.code, r.body)
		}
		req := e.request(tg.method, tg.path, nil)
		req.Host = "evil.example"
		if r = e.send(req); r.code != 403 {
			t.Errorf("%s %s with a foreign Host: %d", tg.method, tg.path, r.code)
		}
	}
	if n := len(e.eng.called()); n != 0 {
		t.Errorf("unauthenticated requests reached the engine: %v", e.eng.called())
	}
	// An unknown rule path without a token is 401 as well (no probing).
	if r := e.do("DELETE", "/api/v1/sites/whatever.example", "", "Authorization", "Bearer wrong"); r.code != 401 {
		t.Errorf("wrong token: %d", r.code)
	}
}

func TestSiteErrorCodes(t *testing.T) {
	e := newEnv(t)
	for name, tc := range map[string]struct {
		err  error
		code int
		app  string
	}{
		"domain":   {fmt.Errorf("wrap: %w", pc.ErrSiteDomain), 422, "invalid_input"},
		"action":   {pc.ErrSiteAction, 422, "invalid_input"},
		"limit":    {pc.ErrSiteLimit, 409, "limit_reached"},
		"notfound": {pc.ErrSiteNotFound, 404, "not_found"},
		"other":    {errors.New("write /secret/path: disk full"), 500, "internal"},
	} {
		e.eng.failSiteCall("SetSite", tc.err)
		r := e.do("POST", "/api/v1/sites", `{"domain":"example.com","action":"direct"}`)
		if r.code != tc.code || r.errCode(t) != tc.app || strings.Contains(string(r.body), "secret") || strings.Contains(string(r.body), "wrap") {
			t.Errorf("%s: %d %s", name, r.code, r.body)
		}
	}
	e.eng.failSiteCall("SetSite", nil)
	e.cancel() // the app is shutting down
	e.eng.failSiteCall("Sites", errors.New("closed"))
	if r := e.get("/api/v1/sites"); r.code != 503 || r.errCode(t) != "engine_unreachable" {
		t.Errorf("during shutdown: %d %s", r.code, r.body)
	}
}

// TestSiteNamesStayOutOfTheLog: the domain a user types is not logged, in a
// path, a body or an error, and the log shows {domain} for the rule path.
func TestSiteNamesStayOutOfTheLog(t *testing.T) {
	e := newEnv(t)
	dom := "secret-marker-9f3a.example"
	e.eng.setSites(fakeRule(dom, "block", 1))
	e.do("POST", "/api/v1/sites", `{"domain":"`+dom+`","action":"direct"}`)
	e.do("POST", "/api/v1/sites", `{"domain":"https://`+dom+`/x","action":"direct"}`)
	e.eng.failSiteCall("SetSite", fmt.Errorf("cannot store %s", dom))
	e.do("POST", "/api/v1/sites", `{"domain":"`+dom+`","action":"direct"}`)
	e.eng.failSiteCall("SetSite", nil)
	e.get("/api/v1/sites")
	e.del("/api/v1/sites/" + dom)
	e.del("/api/v1/sites/" + dom)
	e.do("DELETE", "/api/v1/sites/"+dom, "", "Authorization", "Bearer wrong")
	e.do("GET", "/api/v1/sites/"+dom, "")
	logs := e.log.String()
	if strings.Contains(logs, "secret-marker") {
		t.Errorf("the log has the site name:\n%s", logs)
	}
	if !strings.Contains(logs, "path=/api/v1/sites/{domain}") || !strings.Contains(logs, "path=/api/v1/sites ") {
		t.Errorf("the request log lacks the paths:\n%s", logs)
	}
}
