package desktop

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"mime"
	"net"
	"net/http"
	"net/netip"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/nikoejento/obhod/internal/lib/route"
	"github.com/nikoejento/obhod/internal/pc"
	"github.com/nikoejento/obhod/internal/pc/status"
)

// Limits and timeouts of the control API (api/desktop/API.md, "Listener and transport").
const (
	apiPrefix         = "/api/v1"
	maxBodyBytes      = 64 << 10
	maxStreams        = 4
	maxCheckDomains   = 100
	streamBuffer      = 64
	defaultPing       = 15 * time.Second
	disconnectTimeout = 60 * time.Second
	retryMillis       = 2000
)

// Error codes of the API.
const (
	codeBadRequest   = "bad_request"
	codeForbidden    = "forbidden"
	codeNotFound     = "not_found"
	codeUnknownDom   = "unknown_domain"
	codeMethod       = "method_not_allowed"
	codeBusy         = "busy"
	codeTooLarge     = "too_large"
	codeUnreachable  = "engine_unreachable"
	codeInternal     = "internal"
	codeInvalidInput = "invalid_input"
	codeUnsupported  = "unsupported_protocol"
	codeDuplicate    = "duplicate"
	codeLimit        = "limit_reached"
	contentTypeJSON  = "application/json; charset=utf-8"
	contentTypeEvent = "text/event-stream; charset=utf-8"
)

type handlerFunc func(w http.ResponseWriter, r *http.Request)

// server is the HTTP side of the control API. It maps requests to engine calls
// and validates them; it holds no business logic and no state beyond the list
// of open event streams and the connect/disconnect operations it started.
type server struct {
	eng     engine
	token   [sha256.Size]byte // SHA-256 of the token; the token itself is not kept
	log     *slog.Logger
	version string // the "splitter" field of GET /version
	port    string // the port requests must address ("" = any)

	pingEvery time.Duration

	runCtx  context.Context    // the context given to the engine's Run
	stopRun context.CancelFunc // ends it (POST /shutdown)

	routes   map[string]map[string]handlerFunc
	idRoutes map[string]map[string]handlerFunc // /servers/{id}[/suffix], keyed by the suffix ("" or "activate")

	streams   atomic.Int32
	authFails atomic.Int64
	down      chan struct{} // closed when the app is shutting down
	downOnce  sync.Once
	streamWG  sync.WaitGroup // open event streams
	opWG      sync.WaitGroup // connect and disconnect goroutines

	mu          sync.Mutex
	connCancel  context.CancelFunc // cancels the connect in flight
	connBusy    bool
	disconnBusy bool
}

// newServer returns the handler of the control API. token is hashed at once
// and not kept. runCtx and stopRun belong to the engine's Run.
func newServer(eng engine, token string, version string, log *slog.Logger, runCtx context.Context, stopRun context.CancelFunc) *server {
	if log == nil {
		log = slog.New(slog.NewTextHandler(io.Discard, nil))
	}
	s := &server{
		eng: eng, token: sha256.Sum256([]byte(token)), log: log, version: version,
		pingEvery: defaultPing, runCtx: runCtx, stopRun: stopRun, down: make(chan struct{}),
	}
	s.routes = map[string]map[string]handlerFunc{
		apiPrefix + "/state":      {http.MethodGet: s.noBody(s.getState)},
		apiPrefix + "/routes":     {http.MethodGet: s.noBody(s.getRoutes)},
		apiPrefix + "/processes":  {http.MethodGet: s.noBody(s.getProcesses)},
		apiPrefix + "/logs":       {http.MethodGet: s.noBody(s.getLogs)},
		apiPrefix + "/version":    {http.MethodGet: s.noBody(s.getVersion)},
		apiPrefix + "/events":     {http.MethodGet: s.noBody(s.getEvents)},
		apiPrefix + "/connect":    {http.MethodPost: s.postConnect},
		apiPrefix + "/disconnect": {http.MethodPost: s.postDisconnect},
		apiPrefix + "/check-now":  {http.MethodPost: s.postCheckNow},
		apiPrefix + "/shutdown":   {http.MethodPost: s.postShutdown},

		apiPrefix + "/servers":      {http.MethodGet: s.noBody(s.getServers), http.MethodPost: s.postServer},
		apiPrefix + "/servers/test": {http.MethodPost: s.postServerTest},
	}
	s.idRoutes = map[string]map[string]handlerFunc{
		"":         {http.MethodDelete: s.noBody(s.deleteServer)},
		"activate": {http.MethodPost: s.postActivateServer},
	}
	// Streams end with a final shutdown event whatever stops the app (the
	// shutdown endpoint, a signal, the parent process).
	go func() {
		<-runCtx.Done()
		s.beginShutdown()
	}()
	return s
}

// setAddr tells the server which port requests must address.
func (s *server) setAddr(a net.Addr) {
	if _, port, err := net.SplitHostPort(a.String()); err == nil {
		s.port = port
	}
}

func (s *server) beginShutdown() { s.downOnce.Do(func() { close(s.down) }) }

func (s *server) shuttingDown() bool {
	select {
	case <-s.down:
		return true
	default:
		return false
	}
}

// httpServer returns the http.Server with the contract's timeouts.
func (s *server) httpServer() *http.Server {
	return &http.Server{
		Handler:           s,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       10 * time.Second,
		WriteTimeout:      15 * time.Second,
		IdleTimeout:       60 * time.Second,
		MaxHeaderBytes:    16 << 10,
		ErrorLog:          slog.NewLogLogger(s.log.Handler(), slog.LevelDebug),
	}
}

// finish ends the open streams (each gets a final shutdown event), then closes
// the listener once nothing is in flight. It returns when the server is down.
func (s *server) finish(srv *http.Server) {
	s.beginShutdown()
	waitTimeout(&s.streamWG, 3*time.Second)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := srv.Shutdown(ctx); err != nil {
		_ = srv.Close()
	}
	waitTimeout(&s.opWG, 5*time.Second)
}

func waitTimeout(wg *sync.WaitGroup, d time.Duration) {
	done := make(chan struct{})
	go func() { wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(d):
	}
}

// statusWriter records the status code for the request log and keeps
// http.Flusher and http.ResponseController working.
type statusWriter struct {
	http.ResponseWriter
	code int
}

func (w *statusWriter) WriteHeader(c int) {
	if w.code == 0 {
		w.code = c
	}
	w.ResponseWriter.WriteHeader(c)
}

func (w *statusWriter) Write(b []byte) (int, error) {
	if w.code == 0 {
		w.code = http.StatusOK
	}
	return w.ResponseWriter.Write(b)
}

func (w *statusWriter) Flush() {
	if f, ok := w.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

func (w *statusWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }

// ServeHTTP applies the checks in the contract's order: Host (403), token
// (401), route and method (404, 405), then the body and the handler.
func (s *server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	start := time.Now()
	sw := &statusWriter{ResponseWriter: w}
	logPath := loggedPath(r.URL.Path) // a server id never reaches the log
	defer func() {
		if rec := recover(); rec != nil {
			if rec == http.ErrAbortHandler {
				panic(rec)
			}
			s.log.Error("handler panic", "path", logPath)
			if sw.code == 0 {
				s.writeError(sw, http.StatusInternalServerError, codeInternal, "Something went wrong inside the engine.")
			}
		}
		s.log.Debug("request", "method", r.Method, "path", logPath, "status", sw.code, "duration", time.Since(start))
	}()

	h := sw.Header()
	h.Set("Content-Security-Policy", "default-src 'self'")
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("Referrer-Policy", "no-referrer")
	h.Set("Cache-Control", "no-store")

	if !s.hostAllowed(r.Host) {
		s.log.Warn("request rejected: unexpected Host header")
		s.writeError(sw, http.StatusForbidden, codeForbidden, "This request is not allowed.")
		return
	}
	if !s.authorized(r) {
		if n := s.authFails.Add(1); n <= 10 || n%100 == 0 {
			s.log.Warn("request rejected: missing or wrong token", "count", n)
		}
		sw.Header().Set("WWW-Authenticate", "Bearer")
		sw.WriteHeader(http.StatusUnauthorized)
		return
	}
	methods, ok := s.routes[r.URL.Path]
	if !ok {
		methods, ok = s.serverRoute(r)
	}
	if !ok {
		s.writeError(sw, http.StatusNotFound, codeNotFound, "There is no such endpoint.")
		return
	}
	handler, ok := methods[r.Method]
	if !ok {
		allow := make([]string, 0, len(methods))
		for m := range methods {
			allow = append(allow, m)
		}
		sw.Header().Set("Allow", strings.Join(allow, ", "))
		s.writeError(sw, http.StatusMethodNotAllowed, codeMethod, "This endpoint does not accept that method.")
		return
	}
	if s.shuttingDown() && r.URL.Path != apiPrefix+"/shutdown" {
		s.writeError(sw, http.StatusServiceUnavailable, codeUnreachable, "The engine is shutting down.")
		return
	}
	handler(sw, r)
}

// loggedPath is the path for the request log: under /servers/ the segment
// after it is a server id (or garbage that looks like one) and is shown as {id}.
func loggedPath(p string) string {
	rest, found := strings.CutPrefix(p, apiPrefix+"/servers/")
	if !found || rest == "" || rest == "test" {
		return p
	}
	_, suffix, hasSuffix := strings.Cut(rest, "/")
	if hasSuffix {
		return apiPrefix + "/servers/{id}/" + suffix
	}
	return apiPrefix + "/servers/{id}"
}

// serverRoute matches /api/v1/servers/{id} and /api/v1/servers/{id}/activate.
// A well-formed id is stored in the request (PathValue "id"); an id of the
// wrong shape is no route (404).
func (s *server) serverRoute(r *http.Request) (map[string]handlerFunc, bool) {
	rest, found := strings.CutPrefix(r.URL.Path, apiPrefix+"/servers/")
	if !found || rest == "" {
		return nil, false
	}
	id, suffix, _ := strings.Cut(rest, "/")
	m, known := s.idRoutes[suffix]
	if !known || !serverIDShape(id) {
		return nil, false
	}
	r.SetPathValue("id", id)
	return m, true
}

// serverIDShape is 16 lowercase hex characters; the gateway does not pass
// anything else on as an id.
func serverIDShape(id string) bool {
	if len(id) != 16 {
		return false
	}
	for _, c := range id {
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f') {
			return false
		}
	}
	return true
}

// hostAllowed accepts only a loopback IP or "localhost" on our port, so a page
// on another origin that rebinds its DNS name to 127.0.0.1 (it still sends its
// own name in Host) is refused.
func (s *server) hostAllowed(hostport string) bool {
	host, port, err := net.SplitHostPort(hostport)
	if err != nil {
		host, port = hostport, "80"
		if len(host) > 1 && host[0] == '[' && host[len(host)-1] == ']' {
			host = host[1 : len(host)-1]
		}
	}
	if s.port != "" && port != s.port {
		return false
	}
	if host == "localhost" {
		return true
	}
	ip, err := netip.ParseAddr(host)
	return err == nil && ip.Zone() == "" && ip.IsLoopback()
}

// authorized checks the bearer token against the stored digest in constant time.
func (s *server) authorized(r *http.Request) bool {
	tok, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
	sum := sha256.Sum256([]byte(tok))
	match := subtle.ConstantTimeCompare(sum[:], s.token[:]) == 1
	return ok && match
}

// ---- responses ----

type errorBody struct {
	Error    string   `json:"error"`
	Code     string   `json:"code"`
	Commands []string `json:"commands,omitempty"`
}

func (s *server) writeJSON(w http.ResponseWriter, status int, v any) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		s.log.Error("encode response failed")
		http.Error(w, "", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", contentTypeJSON)
	w.WriteHeader(status)
	_, _ = w.Write(buf.Bytes())
}

// writeError sends {error, code}: a fixed sentence, never an internal error text.
func (s *server) writeError(w http.ResponseWriter, status int, code, msg string, commands ...string) {
	s.writeJSON(w, status, errorBody{Error: msg, Code: code, Commands: commands})
}

type okBody struct {
	OK       bool   `json:"ok"`
	State    string `json:"state,omitempty"`
	Checking *bool  `json:"checking,omitempty"`
}

// noBody wraps a GET handler: GETs take no body.
func (s *server) noBody(h handlerFunc) handlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.ContentLength > 0 || len(r.TransferEncoding) > 0 {
			s.writeError(w, http.StatusBadRequest, codeBadRequest, "This request takes no body.")
			return
		}
		h(w, r)
	}
}

// decodeBody reads the optional JSON body of a POST into dst. It answers the
// error itself and returns false when the request must stop.
func (s *server) decodeBody(w http.ResponseWriter, r *http.Request, dst any) bool {
	if r.ContentLength > maxBodyBytes {
		s.writeError(w, http.StatusRequestEntityTooLarge, codeTooLarge, "The request is too large.")
		return false
	}
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxBodyBytes))
	if err != nil {
		var tooBig *http.MaxBytesError
		if errors.As(err, &tooBig) {
			s.writeError(w, http.StatusRequestEntityTooLarge, codeTooLarge, "The request is too large.")
		} else {
			s.writeError(w, http.StatusBadRequest, codeBadRequest, "The request body could not be read.")
		}
		return false
	}
	if len(body) == 0 {
		return true
	}
	if mt, _, err := mime.ParseMediaType(r.Header.Get("Content-Type")); err != nil || mt != "application/json" {
		s.writeError(w, http.StatusUnsupportedMediaType, codeBadRequest, "The request body must be JSON (Content-Type: application/json).")
		return false
	}
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.DisallowUnknownFields()
	var extra json.RawMessage
	if err := dec.Decode(dst); err != nil || dec.Decode(&extra) != io.EOF {
		s.writeError(w, http.StatusBadRequest, codeBadRequest, "The request body is not valid for this endpoint.")
		return false
	}
	return true
}

// ---- payloads ----

type stateBody struct {
	State    string          `json:"state"`
	Error    *errorBody      `json:"error"`
	Checking bool            `json:"checking"`
	Snapshot status.Snapshot `json:"snapshot"`
	Networks []networkBody   `json:"networks"`
}

type networkBody struct {
	Label string    `json:"label"`
	Since time.Time `json:"since"`
}

type routeBody struct {
	Domain    string        `json:"domain"`
	Route     route.Route   `json:"route"`
	Source    route.Source  `json:"source"`
	Since     time.Time     `json:"since"`
	LastCheck time.Time     `json:"lastCheck"`
	History   []historyBody `json:"history"`
}

// historyBody has the Go field names, like status.Change without the domain.
type historyBody struct {
	From, To route.Route
	At       time.Time
}

type logsBody struct {
	Name  string   `json:"name"`
	Lines []string `json:"lines"`
}

type versionBody struct {
	Splitter string `json:"splitter"`
	SingBox  string `json:"singBox"`
	Desync   string `json:"desync"`
	API      string `json:"api"`
}

type routesEventBody struct {
	Changes []status.Change `json:"changes"`
}

type checkEventBody struct {
	Running bool     `json:"running"`
	Domains []string `json:"domains"`
	Changed *int     `json:"changed,omitempty"`
}

type trafficEventBody struct {
	UploadTotal   int64                 `json:"uploadTotal"`
	DownloadTotal int64                 `json:"downloadTotal"`
	Connections   int                   `json:"connections"`
	Delta         map[route.Route]int64 `json:"delta"`
	Traffic       map[route.Route]int64 `json:"Traffic"`
	Savings       float64               `json:"Savings"`
	At            time.Time             `json:"At"`
}

func opErrorBody(e *pc.OpError) *errorBody {
	if e == nil {
		return nil
	}
	return &errorBody{Error: e.Message, Code: e.Code, Commands: e.Commands}
}

func (s *server) buildState() stateBody {
	st := s.eng.State()
	snap := s.eng.Snapshot()
	snap.State = string(st)
	normalizeSnapshot(&snap)
	nets := make([]networkBody, 0, 10)
	for _, n := range s.eng.Networks() {
		nets = append(nets, networkBody{Label: n.Label, Since: n.Since.UTC()})
	}
	return stateBody{State: string(st), Error: opErrorBody(s.eng.LastError()), Checking: s.eng.Checking(), Snapshot: snap, Networks: nets}
}

// normalizeSnapshot makes the encoding stable: nil slices and maps become
// empty ones, times are UTC, and the process list uses the API's names.
func normalizeSnapshot(s *status.Snapshot) {
	s.LastCheck, s.NextCheck = s.LastCheck.UTC(), s.NextCheck.UTC()
	if s.Routes == nil {
		s.Routes = map[route.Route]int{}
	}
	if s.Traffic == nil {
		s.Traffic = map[route.Route]int64{}
	}
	s.Processes = apiProcesses(s.Processes)
	if s.Warnings == nil {
		s.Warnings = []string{}
	}
	s.Recent = apiChanges(s.Recent)
}

func apiChanges(in []status.Change) []status.Change {
	out := make([]status.Change, 0, len(in))
	for _, c := range in {
		c.At = c.At.UTC()
		out = append(out, c)
	}
	return out
}

// apiProcesses lists splitter (always running), sing-box and desync (the
// winws2 or nfqws2 engine); one that was never started is stopped.
func apiProcesses(in []status.Process) []status.Process {
	find := func(names ...string) (status.Process, bool) {
		for _, n := range names {
			for _, p := range in {
				if p.Name == n {
					return p, true
				}
			}
		}
		return status.Process{}, false
	}
	out := []status.Process{{Name: "splitter", State: "running"}}
	for _, e := range []struct {
		api   string
		names []string
	}{{"sing-box", []string{"sing-box"}}, {"desync", []string{"winws2", "nfqws2"}}} {
		p, ok := find(e.names...)
		if !ok {
			p = status.Process{State: "stopped"}
		}
		p.Name = e.api
		out = append(out, p)
	}
	return out
}

// ---- GET handlers ----

func (s *server) getState(w http.ResponseWriter, _ *http.Request) {
	s.writeJSON(w, http.StatusOK, s.buildState())
}

func (s *server) getRoutes(w http.ResponseWriter, _ *http.Request) {
	infos := s.eng.Routes()
	out := make([]routeBody, 0, len(infos))
	for _, i := range infos {
		hist := make([]historyBody, 0, len(i.History))
		for _, c := range i.History {
			hist = append(hist, historyBody{From: c.From, To: c.To, At: c.At.UTC()})
		}
		out = append(out, routeBody{Domain: i.Domain, Route: i.Route, Source: i.Source, Since: i.Since.UTC(), LastCheck: i.LastCheck.UTC(), History: hist})
	}
	s.writeJSON(w, http.StatusOK, out)
}

func (s *server) getProcesses(w http.ResponseWriter, _ *http.Request) {
	s.writeJSON(w, http.StatusOK, apiProcesses(s.eng.Snapshot().Processes))
}

func (s *server) getLogs(w http.ResponseWriter, r *http.Request) {
	name := r.URL.Query().Get("name")
	lines, err := s.eng.Logs(name)
	if errors.Is(err, pc.ErrUnknownLog) {
		s.writeError(w, http.StatusBadRequest, codeBadRequest, "name must be splitter, sing-box or desync.")
		return
	}
	if err != nil {
		s.log.Error("logs failed; see the engine log")
		s.writeError(w, http.StatusInternalServerError, codeInternal, "Something went wrong inside the engine.")
		return
	}
	if lines == nil {
		lines = []string{}
	}
	s.writeJSON(w, http.StatusOK, logsBody{Name: name, Lines: lines})
}

func (s *server) getVersion(w http.ResponseWriter, _ *http.Request) {
	s.writeJSON(w, http.StatusOK, versionBody{Splitter: s.version, API: "v1"})
}

// ---- POST handlers ----

func (s *server) postConnect(w http.ResponseWriter, r *http.Request) {
	if !s.decodeBody(w, r, &struct{}{}) {
		return
	}
	switch st := s.eng.State(); st {
	case pc.StateConnecting, pc.StateConnected:
		s.writeJSON(w, http.StatusOK, okBody{OK: true, State: string(st)})
	case pc.StateDisconnecting:
		s.writeError(w, http.StatusConflict, codeBusy, "The engine is still disconnecting; try again in a moment.")
	default:
		if !s.startConnect() { // one is already on its way
			s.writeJSON(w, http.StatusOK, okBody{OK: true, State: string(pc.StateConnecting)})
			return
		}
		s.writeJSON(w, http.StatusAccepted, okBody{OK: true, State: string(pc.StateConnecting)})
	}
}

// startConnect runs Connect on the app's context, not the request's. It
// returns false when a connect is already in flight.
func (s *server) startConnect() bool {
	s.mu.Lock()
	if s.connBusy {
		s.mu.Unlock()
		return false
	}
	ctx, cancel := context.WithCancel(s.runCtx)
	s.connBusy, s.connCancel = true, cancel
	s.mu.Unlock()
	s.opWG.Add(1)
	go func() {
		defer s.opWG.Done()
		err := s.eng.Connect(ctx)
		cancelled := ctx.Err() != nil
		cancel()
		s.mu.Lock()
		s.connBusy, s.connCancel = false, nil
		s.mu.Unlock()
		switch {
		case err == nil:
		case cancelled:
			s.log.Info("connect cancelled")
		default:
			code := ""
			if le := s.eng.LastError(); le != nil {
				code = le.Code
			}
			s.log.Warn("connect failed", "code", code)
		}
	}()
	return true
}

func (s *server) cancelConnect() {
	s.mu.Lock()
	cancel := s.connCancel
	s.mu.Unlock()
	if cancel != nil {
		cancel()
	}
}

func (s *server) postDisconnect(w http.ResponseWriter, r *http.Request) {
	if !s.decodeBody(w, r, &struct{}{}) {
		return
	}
	switch st := s.eng.State(); st {
	case pc.StateDisconnected, pc.StateDisconnecting:
		s.writeJSON(w, http.StatusOK, okBody{OK: true, State: string(st)})
	default:
		if st == pc.StateConnecting {
			s.cancelConnect() // ends in disconnected, without an error
		}
		s.startDisconnect()
		s.writeJSON(w, http.StatusAccepted, okBody{OK: true, State: string(pc.StateDisconnecting)})
	}
}

func (s *server) startDisconnect() {
	s.mu.Lock()
	if s.disconnBusy {
		s.mu.Unlock()
		return
	}
	s.disconnBusy = true
	s.mu.Unlock()
	s.opWG.Add(1)
	go func() {
		defer s.opWG.Done()
		ctx, cancel := context.WithTimeout(s.runCtx, disconnectTimeout)
		err := s.eng.Disconnect(ctx)
		cancel()
		s.mu.Lock()
		s.disconnBusy = false
		s.mu.Unlock()
		if err != nil && s.runCtx.Err() == nil {
			s.log.Warn("disconnect reported an error; see the engine log")
		}
	}()
}

type checkNowRequest struct {
	Domains []string `json:"domains"`
}

func (s *server) postCheckNow(w http.ResponseWriter, r *http.Request) {
	var req checkNowRequest
	if !s.decodeBody(w, r, &req) {
		return
	}
	if len(req.Domains) > maxCheckDomains {
		s.writeError(w, http.StatusBadRequest, codeBadRequest, "domains may list at most 100 names.")
		return
	}
	var domains []string
	for _, d := range req.Domains {
		n, err := route.Normalize(d)
		if err != nil {
			s.writeError(w, http.StatusBadRequest, codeBadRequest, "domains must be plain domain names such as example.org.")
			return
		}
		domains = append(domains, n)
	}
	switch err := s.eng.CheckNow(domains...); {
	case err == nil:
		yes := true
		s.writeJSON(w, http.StatusAccepted, okBody{OK: true, Checking: &yes})
	case errors.Is(err, pc.ErrUnknownDomain):
		s.writeError(w, http.StatusNotFound, codeUnknownDom, "None of these sites is on the list.")
	case errors.Is(err, route.ErrInvalidDomain):
		s.writeError(w, http.StatusBadRequest, codeBadRequest, "domains must be plain domain names such as example.org.")
	default:
		s.log.Error("check-now failed; see the engine log")
		s.writeError(w, http.StatusInternalServerError, codeInternal, "Something went wrong inside the engine.")
	}
}

// postShutdown answers, flushes, then ends the app's run context. A repeated
// call answers 200 again until the listener closes.
func (s *server) postShutdown(w http.ResponseWriter, r *http.Request) {
	if !s.decodeBody(w, r, &struct{}{}) {
		return
	}
	s.writeJSON(w, http.StatusOK, okBody{OK: true})
	_ = http.NewResponseController(w).Flush()
	s.log.Info("shutdown requested")
	s.stopRun()
}

// ---- events ----

func (s *server) acquireStream() bool {
	for {
		n := s.streams.Load()
		if n >= maxStreams {
			return false
		}
		if s.streams.CompareAndSwap(n, n+1) {
			return true
		}
	}
}

func (s *server) getEvents(w http.ResponseWriter, r *http.Request) {
	if !s.acquireStream() {
		s.writeError(w, http.StatusTooManyRequests, codeBusy, "Too many event streams are open.")
		return
	}
	defer s.streams.Add(-1)
	s.streamWG.Add(1)
	defer s.streamWG.Done()

	ch, unsub := s.eng.Subscribe()
	defer unsub()

	rc := http.NewResponseController(w)
	_ = rc.SetWriteDeadline(time.Time{}) // the stream outlives WriteTimeout and ReadTimeout
	_ = rc.SetReadDeadline(time.Time{})
	h := w.Header()
	h.Set("Content-Type", contentTypeEvent)
	h.Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)

	// A pump moves engine events into a buffer of streamBuffer; a client that
	// falls that far behind loses its stream (it reconnects and resyncs) and
	// never blocks the app or other streams.
	local := make(chan pc.Event, streamBuffer)
	lost := make(chan struct{})
	stop := make(chan struct{})
	pumpDone := make(chan struct{})
	go func() {
		defer close(pumpDone)
		for {
			select {
			case ev, ok := <-ch:
				if !ok {
					close(lost)
					return
				}
				select {
				case local <- ev:
				default:
					close(lost)
					return
				}
			case <-stop:
				return
			}
		}
	}()
	defer func() { close(stop); <-pumpDone }()

	flush := func() bool {
		err := rc.Flush()
		return err == nil || errors.Is(err, http.ErrNotSupported) // a test recorder cannot flush
	}
	id := 0
	send := func(name string, v any) bool {
		data, err := json.Marshal(v)
		if err != nil {
			s.log.Error("encode event failed", "event", name)
			return false
		}
		id++
		if _, err := fmt.Fprintf(w, "id: %d\nevent: %s\ndata: %s\n\n", id, name, data); err != nil {
			return false
		}
		return flush()
	}
	if _, err := fmt.Fprintf(w, "retry: %d\n\n", retryMillis); err != nil {
		return
	}
	if !send("state", s.buildState()) {
		return
	}

	ping := time.NewTicker(s.pingEvery)
	defer ping.Stop()
	for {
		select {
		case ev := <-local:
			name, payload := s.eventPayload(ev)
			if name == "" {
				continue
			}
			if !send(name, payload) {
				return
			}
		case <-ping.C:
			if _, err := io.WriteString(w, ": ping\n\n"); err != nil {
				return
			}
			if !flush() {
				return
			}
		case <-s.down:
			send("shutdown", struct{}{})
			return
		case <-lost:
			return
		case <-r.Context().Done():
			return
		}
	}
}

// eventPayload maps an engine event to its SSE name and JSON payload.
func (s *server) eventPayload(ev pc.Event) (string, any) {
	switch ev.Kind {
	case pc.EventState:
		return "state", s.buildState()
	case pc.EventRoutes:
		return "routes", routesEventBody{Changes: apiChanges(ev.Changes)}
	case pc.EventCheck:
		b := checkEventBody{Running: ev.Check.Running, Domains: ev.Check.Domains}
		if !ev.Check.Running {
			n := ev.Check.Changed
			b.Changed = &n
		}
		return "check", b
	case pc.EventTraffic:
		t := ev.Traffic
		b := trafficEventBody{
			UploadTotal: t.Upload, DownloadTotal: t.Download, Connections: t.Connections,
			Delta: t.Delta, Traffic: t.Total, Savings: t.Savings, At: t.At.UTC(),
		}
		if b.Delta == nil {
			b.Delta = map[route.Route]int64{}
		}
		if b.Traffic == nil {
			b.Traffic = map[route.Route]int64{}
		}
		return "traffic", b
	}
	return "", nil
}
