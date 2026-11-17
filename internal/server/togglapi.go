package server

import (
	"errors"
	"net/http"
	"strings"

	"github.com/Jairanpo/flamingo/internal/library"
	"github.com/Jairanpo/flamingo/internal/toggl"
)

// The timer button: each person's own Toggl account, reached from here.

func (s *Server) routesToggl(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/toggl/status", s.requireUser(s.apiTogglStatus))
	mux.HandleFunc("POST /api/toggl/start", s.requireUser(s.apiTogglStart))
	mux.HandleFunc("POST /api/toggl/stop", s.requireUser(s.apiTogglStop))
	mux.HandleFunc("GET /api/toggl/account", s.requireUser(s.apiTogglAccount))
	mux.HandleFunc("PUT /api/toggl/account", s.requireUser(s.apiTogglConnect))
	mux.HandleFunc("DELETE /api/toggl/account", s.requireUser(s.apiTogglDisconnect))
}

// apiTogglAccount says whether the signed-in person has connected Toggl.
// The token never comes back: it can be replaced or removed, not read.
func (s *Server) apiTogglAccount(w http.ResponseWriter, r *http.Request) {
	l, err := s.lib(r)
	if err != nil {
		s.failErr(w, err)
		return
	}
	on, wid, err := l.TogglConnected(r.Context())
	if err != nil {
		s.failErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"connected": on, "workspace": wid, "can_store": s.box != nil})
}

// apiTogglConnect stores the person's own API token, after Toggl has
// confirmed it works.
func (s *Server) apiTogglConnect(w http.ResponseWriter, r *http.Request) {
	if s.box == nil {
		fail(w, http.StatusConflict, "this server has no secret key, so it cannot keep Toggl tokens")
		return
	}
	b := body(r)
	tok := strings.TrimSpace(str(b, "token"))
	if tok == "" || len(tok) > 200 {
		fail(w, http.StatusBadRequest, "paste your Toggl API token")
		return
	}
	var wid int64
	if f, ok := num(b, "workspace"); ok {
		if f < 0 || f != float64(int64(f)) {
			fail(w, http.StatusBadRequest, "the workspace id is a whole number")
			return
		}
		wid = int64(f)
	}
	l, err := s.lib(r)
	if err != nil {
		s.failErr(w, err)
		return
	}
	if err := s.toggl.Check(r.Context(), toggl.Account{Token: tok, Workspace: wid}); err != nil {
		s.togglFail(w, err)
		return
	}
	if err := l.SetMyToggl(r.Context(), s.box, tok, wid); err != nil {
		s.failErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"connected": true, "workspace": wid, "can_store": true})
}

func (s *Server) apiTogglDisconnect(w http.ResponseWriter, r *http.Request) {
	l, err := s.lib(r)
	if err != nil {
		s.failErr(w, err)
		return
	}
	if err := l.SetMyToggl(r.Context(), s.box, "", 0); err != nil {
		s.failErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"connected": false, "workspace": 0, "can_store": s.box != nil})
}

// togglAccount is the signed-in user's account; ok is false (and the
// answer written) when there is none to use.
func (s *Server) togglAccount(w http.ResponseWriter, r *http.Request, l *library.Lib) (toggl.Account, bool) {
	a, err := l.TogglAccount(r.Context(), s.box)
	if errors.Is(err, library.ErrNoToggl) {
		fail(w, http.StatusConflict, "Toggl is not connected for you — add your API token in your account (your name, top right)")
		return a, false
	}
	if err != nil {
		s.log.Error("toggl account", "user", l.User, "err", err)
		fail(w, http.StatusInternalServerError, "your Toggl token could not be read on the server")
		return a, false
	}
	return a, true
}

func (s *Server) togglFail(w http.ResponseWriter, err error) {
	if toggl.IsTogglError(err) {
		fail(w, http.StatusBadGateway, err.Error())
		return
	}
	s.failErr(w, err)
}

// apiTogglStatus says whether the button should be there at all, and what
// is running.
func (s *Server) apiTogglStatus(w http.ResponseWriter, r *http.Request) {
	l, err := s.lib(r)
	if err != nil {
		s.failErr(w, err)
		return
	}
	a, err := l.TogglAccount(r.Context(), s.box)
	if errors.Is(err, library.ErrNoToggl) {
		writeJSON(w, http.StatusOK, map[string]any{"configured": false, "running": map[string]any{}})
		return
	}
	if err != nil {
		s.log.Error("toggl account", "user", l.User, "err", err)
		writeJSON(w, http.StatusOK, map[string]any{"configured": true, "running": map[string]any{},
			"error": "your Toggl token could not be read on the server"})
		return
	}
	cur, err := s.toggl.Current(r.Context(), a)
	if err != nil {
		// Connected but unreachable is worth saying, rather than looking
		// the same as "not set up".
		writeJSON(w, http.StatusOK, map[string]any{"configured": true, "running": map[string]any{}, "error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"configured": true, "running": toggl.AsJSON(cur)})
}

func (s *Server) apiTogglStart(w http.ResponseWriter, r *http.Request) {
	b := body(r)
	l, err := s.lib(r)
	if err != nil {
		s.failErr(w, err)
		return
	}
	vid := str(b, "video")
	if !vidRE.MatchString(vid) {
		fail(w, http.StatusNotFound, "unknown video")
		return
	}
	entry, err := l.TogglEntryFor(r.Context(), vid, str(b, "source"))
	if err != nil {
		s.failErr(w, err)
		return
	}
	a, ok := s.togglAccount(w, r, l)
	if !ok {
		return
	}
	e, missing, err := s.toggl.Start(r.Context(), a, entry.Description, entry.Project, entry.Tags)
	if err != nil {
		s.togglFail(w, err)
		return
	}
	var projectMissing any
	if missing {
		projectMissing = entry.Project
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "running": toggl.AsJSON(e), "project_missing": projectMissing})
}

func (s *Server) apiTogglStop(w http.ResponseWriter, r *http.Request) {
	b := body(r)
	l, err := s.lib(r)
	if err != nil {
		s.failErr(w, err)
		return
	}
	a, ok := s.togglAccount(w, r, l)
	if !ok {
		return
	}
	ctx := r.Context()
	id, _ := num(b, "id")
	wid, _ := num(b, "workspace")
	eid := int64(id)
	if eid == 0 {
		cur, err := s.toggl.Current(ctx, a)
		if err != nil {
			s.togglFail(w, err)
			return
		}
		if cur == nil {
			writeJSON(w, http.StatusOK, map[string]any{"ok": true, "running": map[string]any{}})
			return
		}
		eid = cur.ID
	}
	if err := s.toggl.Stop(ctx, a, eid, int64(wid)); err != nil {
		// Most often the timer was already stopped in Toggl itself: answer
		// with what is really running instead of an error.
		if now, cerr := s.toggl.Current(ctx, a); cerr == nil && (now == nil || now.ID != eid) {
			writeJSON(w, http.StatusOK, map[string]any{"ok": true, "running": toggl.AsJSON(now)})
			return
		}
		s.togglFail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "running": map[string]any{}})
}
