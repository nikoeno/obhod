package desktop

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/nikoejento/obhod/internal/pc"
)

const (
	id1 = "0000000000000001"
	id2 = "0000000000000002"
)

func (e *env) del(path string) result { e.t.Helper(); return e.do(http.MethodDelete, path, "") }

func fillServers(e *env) {
	e.eng.setServers(fakeServer(1, true), fakeServer(2, false))
}

func TestGetServersGolden(t *testing.T) {
	e := newEnv(t)
	r := e.get("/api/v1/servers")
	if r.code != 200 || strings.TrimSpace(string(r.body)) != "[]" {
		t.Fatalf("empty list: %d %s", r.code, r.body)
	}
	golden(t, "servers_empty", r.body)
	fillServers(e)
	r = e.get("/api/v1/servers")
	if r.code != 200 {
		t.Fatalf("%d %s", r.code, r.body)
	}
	golden(t, "servers_filled", r.body)
	// The object has exactly the contract's fields.
	var list []map[string]any
	r.json(t, &list)
	for _, o := range list {
		if len(o) != 8 {
			t.Errorf("server object has %d fields: %v", len(o), o)
		}
	}
	e.eng.failServerCall("Servers", errors.New("disk on fire at /secret/path"))
	r = e.get("/api/v1/servers")
	if r.code != 500 || r.errCode(t) != "internal" || strings.Contains(string(r.body), "/secret/path") {
		t.Fatalf("list error: %d %s", r.code, r.body)
	}
}

func TestPostServer(t *testing.T) {
	e := newEnv(t)
	r := e.do("POST", "/api/v1/servers", `{"input":"vless://x@example.com:443"}`)
	if r.code != http.StatusCreated {
		t.Fatalf("%d %s", r.code, r.body)
	}
	golden(t, "server_created", r.body)
	var sv struct {
		ID     string
		Name   string
		Active bool
	}
	r.json(t, &sv)
	if sv.Active || sv.Name != "Server 1" {
		t.Fatalf("created server = %+v", sv)
	}
	r = e.do("POST", "/api/v1/servers", `{"input":"vless://y@example.com:443","name":"Мой сервер"}`)
	r.json(t, &sv)
	if r.code != 201 || sv.Name != "Мой сервер" {
		t.Fatalf("named: %d %s", r.code, r.body)
	}
	if got := e.eng.called(); got[len(got)-1] != "AddServer name=Мой сервер" {
		t.Errorf("engine call: %v", got)
	}
	// Both the list and the new server never show anything but the summary.
	if body := string(e.get("/api/v1/servers").body); strings.Contains(body, "vless://") || strings.Contains(body, `"input"`) {
		t.Errorf("list leaks the input: %s", body)
	}
}

func TestPostServerValidation(t *testing.T) {
	e := newEnv(t)
	cases := []struct {
		name, body, ctype string
		code              int
		errcode           string
	}{
		{"no body", ``, "", 400, "bad_request"},
		{"empty object", `{}`, "application/json", 400, "bad_request"},
		{"empty input", `{"input":""}`, "application/json", 400, "bad_request"},
		{"unknown field", `{"input":"x","link":"y"}`, "application/json", 400, "bad_request"},
		{"wrong type", `{"input":5}`, "application/json", 400, "bad_request"},
		{"name wrong type", `{"input":"x","name":5}`, "application/json", 400, "bad_request"},
		{"name too long", `{"input":"x","name":"` + strings.Repeat("n", 61) + `"}`, "application/json", 400, "bad_request"},
		{"name with newline", `{"input":"x","name":"a\nb"}`, "application/json", 400, "bad_request"},
		{"name blank", `{"input":"x","name":"   "}`, "application/json", 400, "bad_request"},
		{"not json", `input=x`, "application/x-www-form-urlencoded", 415, "bad_request"},
		{"text/plain", `{"input":"x"}`, "text/plain", 415, "bad_request"},
		{"too big", `{"input":"` + strings.Repeat("a", maxBodyBytes) + `"}`, "application/json", 413, "too_large"},
		{"name at the limit", `{"input":"x","name":"` + strings.Repeat("n", 60) + `"}`, "application/json", 201, ""},
	}
	for _, c := range cases {
		var rd io.Reader
		hdr := []string{}
		if c.body != "" {
			rd = strings.NewReader(c.body)
			hdr = []string{"Content-Type", c.ctype}
		}
		r := e.send(e.request("POST", "/api/v1/servers", rd, hdr...))
		if r.code != c.code {
			t.Errorf("%s: %d %s, want %d", c.name, r.code, r.body, c.code)
		} else if c.errcode != "" && r.errCode(t) != c.errcode {
			t.Errorf("%s: code %s", c.name, r.errCode(t))
		}
	}
	golden(t, "error_server_name_bad_request", e.do("POST", "/api/v1/servers", `{"input":"x","name":"a\nb"}`).body)
	if n := e.eng.callCount("AddServer"); n != 1 {
		t.Errorf("a rejected request reached the engine: %d calls", n)
	}
}

func TestServerErrorCodes(t *testing.T) {
	e := newEnv(t)
	fillServers(e)
	cases := []struct {
		name    string
		err     error
		code    int
		errcode string
	}{
		{"invalid", &userErr{pc.ErrInvalidInput, "The link has no port."}, 422, "invalid_input"},
		{"unsupported", &userErr{pc.ErrUnsupportedProtocol, "This transport is not supported by the built-in engine."}, 422, "unsupported_protocol"},
		{"duplicate", &userErr{pc.ErrDuplicate, "This server is already in the list."}, 409, "duplicate"},
		{"limit", &userErr{pc.ErrLimit, "The list is full (20 servers). Remove one first."}, 409, "limit_reached"},
		{"too large", pc.ErrInputTooLarge, 413, "too_large"},
		{"busy", pc.ErrBusy, 409, "busy"},
		{"not found", pc.ErrServerNotFound, 404, "not_found"},
		{"wrapped invalid", fmt.Errorf("add: %w", &userErr{pc.ErrInvalidInput, "The JSON is not valid."}), 422, "invalid_input"},
		{"internal", errors.New("open /secret/path: denied"), 500, "internal"},
	}
	for _, c := range cases {
		e.eng.failServerCall("AddServer", c.err)
		r := e.do("POST", "/api/v1/servers", `{"input":"x"}`)
		if r.code != c.code || r.errCode(t) != c.errcode {
			t.Errorf("%s: %d %s", c.name, r.code, r.body)
			continue
		}
		if c.name != "wrapped invalid" {
			golden(t, "error_server_"+strings.ReplaceAll(c.name, " ", "_"), r.body)
		}
		if strings.Contains(string(r.body), "/secret/path") {
			t.Errorf("%s: internal text leaked: %s", c.name, r.body)
		}
	}
	// A message the engine did not mark as safe is replaced by the default sentence of its code.
	e.eng.failServerCall("AddServer", fmt.Errorf("%w: parse failed at %s", pc.ErrInvalidInput, secretMarker))
	r := e.do("POST", "/api/v1/servers", `{"input":"x"}`)
	if r.code != 422 || strings.Contains(string(r.body), secretMarker) {
		t.Errorf("unmarked error text reached the client: %d %s", r.code, r.body)
	}
}

func TestActivateServer(t *testing.T) {
	e := newEnv(t)
	fillServers(e)
	// Already active: 200, reloaded false.
	r := e.post("/api/v1/servers/" + id1 + "/activate")
	if r.code != 200 {
		t.Fatalf("already active: %d %s", r.code, r.body)
	}
	golden(t, "server_activate_disconnected", r.body)
	r = e.post("/api/v1/servers/" + id2 + "/activate")
	var b activateBody
	r.json(t, &b)
	if r.code != 200 || !b.OK || b.Active != id2 || b.Reloaded {
		t.Fatalf("disconnected: %d %s", r.code, r.body)
	}
	// Connected: 202, reloaded true.
	e.eng.setState(pc.StateConnected)
	r = e.post("/api/v1/servers/" + id1 + "/activate")
	if r.code != 202 {
		t.Fatalf("connected: %d %s", r.code, r.body)
	}
	golden(t, "server_activate_connected", r.body)
	// Busy while connecting or disconnecting.
	for _, st := range []pc.State{pc.StateConnecting, pc.StateDisconnecting} {
		e.eng.setState(st)
		r = e.post("/api/v1/servers/" + id2 + "/activate")
		if r.code != 409 || r.errCode(t) != "busy" {
			t.Errorf("%s: %d %s", st, r.code, r.body)
		}
	}
	// Not found, bad id shapes, the dry run failing, body rules.
	r = e.post("/api/v1/servers/ffffffffffffffff/activate")
	if r.code != 404 || r.errCode(t) != "not_found" {
		t.Errorf("unknown id: %d %s", r.code, r.body)
	}
	for _, id := range []string{"1", "..", "ZZZZZZZZZZZZZZZZ", "0000000000000001x", "0000000000000001%2f.."} {
		if r := e.post("/api/v1/servers/" + id + "/activate"); r.code != 404 {
			t.Errorf("id %q: %d %s", id, r.code, r.body)
		}
	}
	e.eng.failServerCall("ActivateServer", &userErr{pc.ErrInvalidInput, "The sing-box engine did not accept this server's settings."})
	r = e.post("/api/v1/servers/" + id2 + "/activate")
	if r.code != 422 || r.errCode(t) != "invalid_input" {
		t.Errorf("dry run failed: %d %s", r.code, r.body)
	}
	e.eng.failServerCall("ActivateServer", nil)
	if r := e.do("POST", "/api/v1/servers/"+id2+"/activate", `{"x":1}`); r.code != 400 {
		t.Errorf("body with a field: %d", r.code)
	}
	if r := e.do("POST", "/api/v1/servers/"+id2+"/activate", `{}`); r.code == 400 || r.code == 415 {
		t.Errorf("empty object body: %d %s", r.code, r.body)
	}
	if r := e.get("/api/v1/servers/" + id2 + "/activate"); r.code != 405 || r.hdr.Get("Allow") != "POST" {
		t.Errorf("GET activate: %d allow=%q", r.code, r.hdr.Get("Allow"))
	}
}

func TestDeleteServer(t *testing.T) {
	e := newEnv(t)
	fillServers(e)
	e.eng.setState(pc.StateConnected)
	r := e.del("/api/v1/servers/" + id1) // the active one while connected
	if r.code != 409 || r.errCode(t) != "busy" {
		t.Fatalf("active while connected: %d %s", r.code, r.body)
	}
	r = e.del("/api/v1/servers/" + id2)
	if r.code != 200 {
		t.Fatalf("another one: %d %s", r.code, r.body)
	}
	golden(t, "server_deleted", r.body)
	e.eng.setState(pc.StateDisconnected)
	if r := e.del("/api/v1/servers/" + id1); r.code != 200 {
		t.Fatalf("active while disconnected: %d %s", r.code, r.body)
	}
	if r := e.del("/api/v1/servers/" + id1); r.code != 404 || r.errCode(t) != "not_found" {
		t.Errorf("twice: %d %s", r.code, r.body)
	}
	if r := e.del("/api/v1/servers/not-an-id"); r.code != 404 {
		t.Errorf("bad id: %d", r.code)
	}
	if r := e.do("DELETE", "/api/v1/servers/"+id1, `{}`); r.code != 400 {
		t.Errorf("DELETE with a body: %d", r.code)
	}
	if r := e.get("/api/v1/servers/" + id1); r.code != 405 || r.hdr.Get("Allow") != "DELETE" {
		t.Errorf("GET one server: %d allow=%q", r.code, r.hdr.Get("Allow"))
	}
	if r := e.do("PUT", "/api/v1/servers", `{}`); r.code != 405 || !strings.Contains(r.hdr.Get("Allow"), "POST") {
		t.Errorf("PUT servers: %d allow=%q", r.code, r.hdr.Get("Allow"))
	}
	if r := e.get("/api/v1/servers/test"); r.code != 405 || r.hdr.Get("Allow") != "POST" {
		t.Errorf("GET test: %d allow=%q", r.code, r.hdr.Get("Allow"))
	}
	if r := e.get("/api/v1/servers/" + id1 + "/other"); r.code != 404 {
		t.Errorf("unknown suffix: %d", r.code)
	}
}

func TestServerTest(t *testing.T) {
	e := newEnv(t)
	fillServers(e)
	r := e.do("POST", "/api/v1/servers/test", `{"input":"vless://x@example.com:443"}`)
	if r.code != 200 {
		t.Fatalf("%d %s", r.code, r.body)
	}
	golden(t, "server_test_ok", r.body)
	if r = e.do("POST", "/api/v1/servers/test", `{"id":"`+id2+`"}`); r.code != 200 {
		t.Fatalf("by id: %d %s", r.code, r.body)
	}
	e.eng.mu.Lock()
	e.eng.srv.testFn = func(id, input string) (string, error) {
		switch {
		case strings.Contains(input, "bad"):
			return "vless", &userErr{pc.ErrInvalidInput, "The link has no port."}
		case strings.Contains(input, "wireguard"):
			return "", &userErr{pc.ErrUnsupportedProtocol, "This kind of link is not supported."}
		case strings.Contains(input, "huge"):
			return "", pc.ErrInputTooLarge
		}
		if id == id1 {
			return "", pc.ErrServerNotFound
		}
		return "trojan", nil
	}
	e.eng.mu.Unlock()
	r = e.do("POST", "/api/v1/servers/test", `{"input":"bad"}`)
	if r.code != 200 {
		t.Fatalf("invalid: %d %s", r.code, r.body)
	}
	golden(t, "server_test_invalid_input", r.body)
	golden(t, "server_test_unsupported_protocol", e.do("POST", "/api/v1/servers/test", `{"input":"wireguard"}`).body)
	if r := e.do("POST", "/api/v1/servers/test", `{"input":"huge"}`); r.code != 413 || r.errCode(t) != "too_large" {
		t.Errorf("too large: %d %s", r.code, r.body)
	}
	if r := e.do("POST", "/api/v1/servers/test", `{"id":"`+id1+`"}`); r.code != 404 || r.errCode(t) != "not_found" {
		t.Errorf("unknown id: %d %s", r.code, r.body)
	}
	for name, body := range map[string]string{
		"neither": `{}`, "both": `{"id":"` + id1 + `","input":"x"}`, "unknown field": `{"input":"x","name":"y"}`,
		"wrong type": `{"id":5}`,
	} {
		if r := e.do("POST", "/api/v1/servers/test", body); r.code != 400 || r.errCode(t) != "bad_request" {
			t.Errorf("%s: %d %s", name, r.code, r.body)
		}
	}
	if r := e.do("POST", "/api/v1/servers/test", `{"id":"../x"}`); r.code != 404 {
		t.Errorf("bad id shape: %d", r.code)
	}
	if r := e.do("POST", "/api/v1/servers/test", `{"input":"`+strings.Repeat("a", maxBodyBytes)+`"}`); r.code != 413 {
		t.Errorf("big body: %d", r.code)
	}
	// The test changes nothing.
	if e.eng.callCount("ActivateServer")+e.eng.callCount("AddServer")+e.eng.callCount("DeleteServer") != 0 {
		t.Error("test endpoint called a mutating method")
	}
}

func TestServerEndpointsRequireTheAuthAndHost(t *testing.T) {
	e := newEnv(t)
	fillServers(e)
	for _, tg := range allPaths {
		if !strings.Contains(tg.path, "/servers") {
			continue
		}
		r := e.do(tg.method, tg.path, "", "Authorization", "")
		if r.code != 401 || len(r.body) != 0 {
			t.Errorf("%s %s without a token: %d %q", tg.method, tg.path, r.code, r.body)
		}
		req := e.request(tg.method, tg.path, nil)
		req.Host = "evil.example"
		r = e.send(req)
		if r.code != 403 {
			t.Errorf("%s %s with a foreign Host: %d", tg.method, tg.path, r.code)
		}
	}
	if n := len(e.eng.called()); n != 0 {
		t.Errorf("unauthenticated requests reached the engine: %v", e.eng.called())
	}
}

// TestServerSecretsStayOutOfResponsesAndLog plants a marker in the request
// body and in an engine that echoes it: no response, error body or log line
// may carry it, and the log shows {id} instead of a server id.
func TestServerSecretsStayOutOfResponsesAndLog(t *testing.T) {
	e := newEnv(t)
	fillServers(e)
	link := "vless://" + secretMarker + "@example.com:443?pbk=" + secretMarker
	var bodies [][]byte
	keep := func(r result) { bodies = append(bodies, r.body) }

	keep(e.do("POST", "/api/v1/servers", `{"input":"`+link+`","name":"n"}`)) // success: no echo
	e.eng.failServerCall("AddServer", fmt.Errorf("%w: cannot parse %s", pc.ErrInvalidInput, link))
	keep(e.do("POST", "/api/v1/servers", `{"input":"`+link+`"}`))
	e.eng.failServerCall("AddServer", fmt.Errorf("write %s: disk full", link))
	keep(e.do("POST", "/api/v1/servers", `{"input":"`+link+`"}`))
	e.eng.failServerCall("AddServer", nil)
	keep(e.do("POST", "/api/v1/servers", `{"input":"`+link+`","extra":"`+secretMarker+`"}`)) // 400
	keep(e.do("POST", "/api/v1/servers", `{"input":"`+link+`","name":"`+strings.Repeat("n", 61)+`"}`))
	e.eng.failServerCall("TestServer", fmt.Errorf("test %s: %w", link, pc.ErrInvalidInput))
	keep(e.do("POST", "/api/v1/servers/test", `{"input":"`+link+`"}`))
	e.eng.failServerCall("TestServer", fmt.Errorf("boom %s", link))
	keep(e.do("POST", "/api/v1/servers/test", `{"input":"`+link+`"}`))
	keep(e.post("/api/v1/servers/" + id2 + "/activate"))
	keep(e.del("/api/v1/servers/" + id2))
	keep(e.get("/api/v1/servers"))
	keep(e.get("/api/v1/state"))

	for _, b := range bodies {
		if strings.Contains(string(b), secretMarker) {
			t.Errorf("a response carries the marker: %s", b)
		}
	}
	logs := e.log.String()
	if strings.Contains(logs, secretMarker) {
		t.Errorf("the log carries the marker:\n%s", logs)
	}
	for _, id := range []string{id1, id2} {
		if strings.Contains(logs, id) {
			t.Errorf("the log shows server id %s:\n%s", id, logs)
		}
	}
	if !strings.Contains(logs, "path=/api/v1/servers/{id}/activate") || !strings.Contains(logs, "path=/api/v1/servers/{id} ") {
		t.Errorf("the request log lacks the {id} paths:\n%s", logs)
	}
	// The same holds for a request that fails authentication.
	e.log.mu.Lock()
	e.log.b.Reset()
	e.log.mu.Unlock()
	e.do("DELETE", "/api/v1/servers/"+id1, "", "Authorization", "Bearer wrong")
	e.do("DELETE", "/api/v1/servers/"+id1+"x", "")
	if strings.Contains(e.log.String(), id1) {
		t.Errorf("the log shows a server id after a failed request:\n%s", e.log)
	}
}

func TestServerCallsUseTheRequestContext(t *testing.T) {
	e := newEnv(t)
	fillServers(e)
	// The engine reports a cancelled request: not a secret, not a 5xx text from the engine.
	e.eng.failServerCall("ActivateServer", fmt.Errorf("lock: %w", errors.New("context canceled")))
	r := e.post("/api/v1/servers/" + id2 + "/activate")
	if r.code != 500 || r.errCode(t) != "internal" {
		t.Errorf("unexpected error: %d %s", r.code, r.body)
	}
	// While the app shuts down the same call answers engine_unreachable.
	e.eng.failServerCall("ActivateServer", nil)
	var raw json.RawMessage
	_ = raw
	e.cancel()
	waitFor(t, "shutdown", func() bool { return e.srv.shuttingDown() })
	if r := e.post("/api/v1/servers/" + id2 + "/activate"); r.code != 503 || r.errCode(t) != "engine_unreachable" {
		t.Errorf("during shutdown: %d %s", r.code, r.body)
	}
}
