package desktop

import (
	"errors"
	"net/http"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/nikoejento/obhod/internal/pc"
)

// Server-management endpoints (ADR 0004 milestone 4; contract in internal/pc/servers/API.md
// and "Server management" below in API.md). The gateway maps and validates; the
// parsing, the dry run and the storage are in internal/pc/servers behind pc.
// It never logs a request body and never returns a key, UUID, password, link or JSON.

const maxServerName = 60

// serverBody is the Server object of the API: no secret, no link, no option.
type serverBody struct {
	ID       string    `json:"id"`
	Name     string    `json:"name"`
	Protocol string    `json:"protocol"`
	Engine   string    `json:"engine"`
	Host     string    `json:"host"`
	Port     int       `json:"port"`
	Active   bool      `json:"active"`
	AddedAt  time.Time `json:"addedAt"`
}

func toServerBody(s pc.Server) serverBody {
	return serverBody{ID: s.ID, Name: s.Name, Protocol: s.Protocol, Engine: s.Engine, Host: s.Host, Port: s.Port, Active: s.Active, AddedAt: s.AddedAt.UTC()}
}

type addServerRequest struct {
	Input string `json:"input"`
	Name  string `json:"name"`
}

type activateBody struct {
	OK       bool   `json:"ok"`
	Active   string `json:"active"`
	Reloaded bool   `json:"reloaded"`
}

type testServerRequest struct {
	ID    string `json:"id"`
	Input string `json:"input"`
}

type testServerBody struct {
	OK       bool   `json:"ok"`
	Protocol string `json:"protocol"`
	Code     string `json:"code"`
}

func (s *server) getServers(w http.ResponseWriter, r *http.Request) {
	list, err := s.eng.Servers()
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	out := make([]serverBody, 0, len(list))
	for _, sv := range list {
		out = append(out, toServerBody(sv))
	}
	s.writeJSON(w, http.StatusOK, out)
}

func (s *server) postServer(w http.ResponseWriter, r *http.Request) {
	var req addServerRequest
	if !s.decodeBody(w, r, &req) {
		return
	}
	if req.Input == "" {
		s.writeError(w, http.StatusBadRequest, codeBadRequest, "input must be a server link or a JSON object.")
		return
	}
	if req.Name != "" && !validServerName(req.Name) {
		s.writeError(w, http.StatusBadRequest, codeBadRequest, "name must be 1 to 60 printable characters.")
		return
	}
	sv, err := s.eng.AddServer(r.Context(), req.Input, req.Name)
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	s.writeJSON(w, http.StatusCreated, toServerBody(sv))
}

func (s *server) postActivateServer(w http.ResponseWriter, r *http.Request) {
	if !s.decodeBody(w, r, &struct{}{}) {
		return
	}
	id := r.PathValue("id")
	reloaded, err := s.eng.ActivateServer(r.Context(), id)
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	code := http.StatusOK
	if reloaded {
		code = http.StatusAccepted
	}
	s.writeJSON(w, code, activateBody{OK: true, Active: id, Reloaded: reloaded})
}

func (s *server) deleteServer(w http.ResponseWriter, r *http.Request) {
	if err := s.eng.DeleteServer(r.Context(), r.PathValue("id")); err != nil {
		s.serverError(w, r, err)
		return
	}
	s.writeJSON(w, http.StatusOK, okBody{OK: true})
}

func (s *server) postServerTest(w http.ResponseWriter, r *http.Request) {
	var req testServerRequest
	if !s.decodeBody(w, r, &req) {
		return
	}
	if (req.ID == "") == (req.Input == "") {
		s.writeError(w, http.StatusBadRequest, codeBadRequest, "Send either id or input.")
		return
	}
	if req.ID != "" && !serverIDShape(req.ID) {
		s.writeError(w, http.StatusNotFound, codeNotFound, "There is no such server.")
		return
	}
	protocol, err := s.eng.TestServer(r.Context(), req.ID, req.Input)
	switch {
	case err == nil:
		s.writeJSON(w, http.StatusOK, testServerBody{OK: true, Protocol: protocol})
	case errors.Is(err, pc.ErrInvalidInput):
		s.writeJSON(w, http.StatusOK, testServerBody{Protocol: protocol, Code: codeInvalidInput})
	case errors.Is(err, pc.ErrUnsupportedProtocol):
		s.writeJSON(w, http.StatusOK, testServerBody{Protocol: protocol, Code: codeUnsupported})
	default:
		s.serverError(w, r, err)
	}
}

// userMessage is implemented by the errors of the servers module: one fixed
// sentence that names the kind of problem and never a value of the input.
type userMessage interface{ UserMessage() string }

// serverError answers a failed server call with a fixed code and a fixed
// sentence. Only the sentences of the servers module's own errors are passed
// on (they hold no value from the input); anything else becomes the default
// sentence of its code, and its text goes nowhere (pc logs the details).
func (s *server) serverError(w http.ResponseWriter, r *http.Request, err error) {
	say := func(def string) string {
		var um userMessage
		if errors.As(err, &um) {
			if m := um.UserMessage(); m != "" {
				return m
			}
		}
		return def
	}
	switch {
	case errors.Is(err, pc.ErrInvalidInput):
		s.writeError(w, http.StatusUnprocessableEntity, codeInvalidInput, say("The server settings are not valid."))
	case errors.Is(err, pc.ErrUnsupportedProtocol):
		s.writeError(w, http.StatusUnprocessableEntity, codeUnsupported, say("This kind of server is not supported."))
	case errors.Is(err, pc.ErrDuplicate):
		s.writeError(w, http.StatusConflict, codeDuplicate, say("This server is already in the list."))
	case errors.Is(err, pc.ErrLimit):
		s.writeError(w, http.StatusConflict, codeLimit, say("The list is full. Remove a server first."))
	case errors.Is(err, pc.ErrInputTooLarge):
		s.writeError(w, http.StatusRequestEntityTooLarge, codeTooLarge, "The link or JSON is too large.")
	case errors.Is(err, pc.ErrServerNotFound):
		s.writeError(w, http.StatusNotFound, codeNotFound, "There is no such server.")
	case errors.Is(err, pc.ErrBusy):
		s.writeError(w, http.StatusConflict, codeBusy, "The engine is busy connecting or disconnecting, or the server is in use. Try again in a moment.")
	case s.runCtx.Err() != nil:
		s.writeError(w, http.StatusServiceUnavailable, codeUnreachable, "The engine is shutting down.")
	default:
		s.log.Error("server call failed; see the engine log", "path", loggedPath(r.URL.Path))
		s.writeError(w, http.StatusInternalServerError, codeInternal, "Something went wrong inside the engine.")
	}
}

// validServerName is 1 to 60 printable characters, not only spaces.
func validServerName(n string) bool {
	if c := utf8.RuneCountInString(n); c < 1 || c > maxServerName {
		return false
	}
	blank := true
	for _, r := range n {
		if !unicode.IsPrint(r) {
			return false
		}
		if r != ' ' {
			blank = false
		}
	}
	return !blank
}
