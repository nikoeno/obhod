package desktop

import (
	"errors"
	"net/http"
	"time"

	"github.com/nikoejento/obhod/internal/pc"
)

// Desync preset endpoints (2026-11-01, stage 2 of 3; contract in "Desync presets" in API.md). The gateway
// maps and validates the shape of the request; the catalogue, the choice, the search and the persistence are
// internal/pc (internal/pc/autodesync, internal/pc/desync). No site name is in any desync payload or event,
// and no log line has a body.

const codeProgramMissing = "program_missing"

type desyncTryBody struct {
	Preset string `json:"preset"`
	Fixed  int    `json:"fixed"`
	Total  int    `json:"total"`
}

type desyncSearchSummary struct {
	At      time.Time       `json:"at"`
	Tried   []desyncTryBody `json:"tried"`
	Chosen  string          `json:"chosen"`
	Outcome string          `json:"outcome"`
}

type desyncBody struct {
	Mode       string               `json:"mode"`
	Preset     string               `json:"preset"`
	Searching  bool                 `json:"searching"`
	LastSearch *desyncSearchSummary `json:"lastSearch"`
}

type desyncPresetBody struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description"`
}

type desyncSearchStarted struct {
	OK        bool `json:"ok"`
	Searching bool `json:"searching"`
}

type setDesyncRequest struct {
	Preset string `json:"preset"`
}

// The desync event payloads: one shape per phase (counts and catalogue ids only).
type desyncSearchingEvent struct {
	Phase string `json:"phase"`
	Total int    `json:"total"`
}

type desyncTryingEvent struct {
	Phase  string `json:"phase"`
	Index  int    `json:"index"`
	Total  int    `json:"total"`
	Preset string `json:"preset"`
}

type desyncDoneEvent struct {
	Phase   string `json:"phase"`
	Outcome string `json:"outcome"`
	Chosen  string `json:"chosen"`
	Fixed   int    `json:"fixed"`
	Of      int    `json:"of"`
}

func (s *server) desyncPayload() desyncBody {
	d := s.eng.Desync()
	b := desyncBody{Mode: string(d.Mode), Preset: d.Preset, Searching: d.Searching}
	if l := d.LastSearch; l != nil {
		sum := &desyncSearchSummary{At: l.At.UTC(), Tried: make([]desyncTryBody, 0, len(l.Tried)), Chosen: l.Chosen, Outcome: l.Outcome}
		for _, t := range l.Tried {
			sum.Tried = append(sum.Tried, desyncTryBody{Preset: t.Preset, Fixed: t.Fixed, Total: t.Total})
		}
		b.LastSearch = sum
	}
	return b
}

func (s *server) getDesync(w http.ResponseWriter, _ *http.Request) {
	s.writeJSON(w, http.StatusOK, s.desyncPayload())
}

func (s *server) getDesyncPresets(w http.ResponseWriter, _ *http.Request) {
	ps := s.eng.DesyncPresets()
	out := make([]desyncPresetBody, 0, len(ps))
	for _, p := range ps {
		out = append(out, desyncPresetBody{ID: p.ID, Name: p.Name, Description: p.Description})
	}
	s.writeJSON(w, http.StatusOK, out)
}

func (s *server) postDesync(w http.ResponseWriter, r *http.Request) {
	var req setDesyncRequest
	if !s.decodeBody(w, r, &req) {
		return
	}
	if req.Preset == "" {
		s.writeError(w, http.StatusBadRequest, codeBadRequest, "preset must be auto or the id of a desync preset.")
		return
	}
	err := s.eng.SetDesync(r.Context(), req.Preset)
	switch {
	case err == nil:
		s.writeJSON(w, http.StatusOK, s.desyncPayload())
	case errors.Is(err, pc.ErrDesyncPreset):
		s.writeError(w, http.StatusUnprocessableEntity, codeInvalidInput, "That is not a desync preset. Use auto or an id from the preset list.")
	case errors.Is(err, pc.ErrProgramMissing):
		s.writeError(w, http.StatusUnprocessableEntity, codeProgramMissing, "A required program is missing next to the app. Reinstall the app.")
	case s.runCtx.Err() != nil:
		s.writeError(w, http.StatusServiceUnavailable, codeUnreachable, "The engine is shutting down.")
	default:
		s.log.Error("desync call failed; see the engine log", "path", loggedPath(r.URL.Path))
		s.writeError(w, http.StatusInternalServerError, codeInternal, "Something went wrong inside the engine.")
	}
}

func (s *server) postDesyncSearch(w http.ResponseWriter, r *http.Request) {
	if !s.decodeBody(w, r, &struct{}{}) {
		return
	}
	err := s.eng.StartDesyncSearch()
	switch {
	case err == nil:
		s.writeJSON(w, http.StatusAccepted, desyncSearchStarted{OK: true, Searching: true})
	case errors.Is(err, pc.ErrDesyncBusy):
		s.writeError(w, http.StatusConflict, codeBusy, "A search is already running, or the app is not connected.")
	case errors.Is(err, pc.ErrDesyncNotAuto):
		s.writeError(w, http.StatusConflict, codeNotAuto, "The desync preset is not automatic. Choose auto first.")
	case errors.Is(err, pc.ErrDesyncNoDomain):
		s.writeError(w, http.StatusConflict, codeNoDomains, "No site is routed to desync, so there is nothing to search for.")
	case s.runCtx.Err() != nil:
		s.writeError(w, http.StatusServiceUnavailable, codeUnreachable, "The engine is shutting down.")
	default:
		s.log.Error("desync search failed; see the engine log", "path", loggedPath(r.URL.Path))
		s.writeError(w, http.StatusInternalServerError, codeInternal, "Something went wrong inside the engine.")
	}
}

// desyncEventPayload maps a pc desync event to its JSON shape.
func desyncEventPayload(e pc.DesyncEvent) any {
	switch e.Phase {
	case "trying":
		return desyncTryingEvent{Phase: e.Phase, Index: e.Index, Total: e.Total, Preset: e.Preset}
	case "done":
		return desyncDoneEvent{Phase: e.Phase, Outcome: e.Outcome, Chosen: e.Chosen, Fixed: e.Fixed, Of: e.Of}
	}
	return desyncSearchingEvent{Phase: e.Phase, Total: e.Total}
}
