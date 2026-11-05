package desktop

import (
	"errors"
	"net/http"
	"time"

	"github.com/nikoejento/obhod/internal/pc"
)

// Per-site rule endpoints (user-approved 2026-11-01; contract in "Per-site rules"
// in API.md). The gateway maps and validates the shape of the request; the
// normalization of the domain, the action check, the limit, the storage and the
// live apply are in internal/pc (internal/pc/sites). A site the user names is
// never logged: the request log shows {domain}, and no log line has a body.

// siteBody is the Rule object of the API.
type siteBody struct {
	Domain  string    `json:"domain"`
	Action  string    `json:"action"`
	AddedAt time.Time `json:"addedAt"`
}

func toSiteBody(r pc.SiteRule) siteBody {
	return siteBody{Domain: r.Domain, Action: string(r.Action), AddedAt: r.AddedAt.UTC()}
}

type setSiteRequest struct {
	Domain string `json:"domain"`
	Action string `json:"action"`
}

func (s *server) getSites(w http.ResponseWriter, r *http.Request) {
	list, err := s.eng.Sites()
	if err != nil {
		s.siteError(w, r, err)
		return
	}
	out := make([]siteBody, 0, len(list))
	for _, rule := range list {
		out = append(out, toSiteBody(rule))
	}
	s.writeJSON(w, http.StatusOK, out)
}

func (s *server) postSite(w http.ResponseWriter, r *http.Request) {
	var req setSiteRequest
	if !s.decodeBody(w, r, &req) {
		return
	}
	if req.Domain == "" {
		s.writeError(w, http.StatusBadRequest, codeBadRequest, "domain must be a site name such as example.com.")
		return
	}
	if req.Action == "" {
		s.writeError(w, http.StatusBadRequest, codeBadRequest, "action must be direct, desync, tunnel or block.")
		return
	}
	rule, created, err := s.eng.SetSite(req.Domain, req.Action)
	if err != nil {
		s.siteError(w, r, err)
		return
	}
	code := http.StatusOK
	if created {
		code = http.StatusCreated
	}
	s.writeJSON(w, code, toSiteBody(rule))
}

func (s *server) deleteSite(w http.ResponseWriter, r *http.Request) {
	if err := s.eng.DeleteSite(r.PathValue("domain")); err != nil {
		s.siteError(w, r, err)
		return
	}
	s.writeJSON(w, http.StatusOK, okBody{OK: true})
}

// siteError answers a failed site call with a fixed code and a fixed sentence;
// the text of the engine's error goes nowhere (pc logs the details).
func (s *server) siteError(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, pc.ErrSiteDomain):
		s.writeError(w, http.StatusUnprocessableEntity, codeInvalidInput, "That is not a plain site name. Enter only the name, for example example.com.")
	case errors.Is(err, pc.ErrSiteAction):
		s.writeError(w, http.StatusUnprocessableEntity, codeInvalidInput, "The action must be direct, desync, tunnel or block.")
	case errors.Is(err, pc.ErrSiteLimit):
		s.writeError(w, http.StatusConflict, codeLimit, "The list of site rules is full. Remove a rule first.")
	case errors.Is(err, pc.ErrSiteNotFound):
		s.writeError(w, http.StatusNotFound, codeNotFound, "There is no rule for that site.")
	case s.runCtx.Err() != nil:
		s.writeError(w, http.StatusServiceUnavailable, codeUnreachable, "The engine is shutting down.")
	default:
		s.log.Error("site call failed; see the engine log", "path", loggedPath(r.URL.Path))
		s.writeError(w, http.StatusInternalServerError, codeInternal, "Something went wrong inside the engine.")
	}
}
