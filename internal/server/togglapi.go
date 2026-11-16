package server

import (
	"errors"
	"net/http"

	"github.com/Jairanpo/flamingo/internal/library"
	"github.com/Jairanpo/flamingo/internal/toggl"
)

// The timer button: each person's own Toggl account, reached from here.

func (s *Server) routesToggl(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/toggl/status", s.requireUser(s.apiTogglStatus))
	mux.HandleFunc("POST /api/toggl/start", s.requireUser(s.apiTogglStart))
	mux.HandleFunc("POST /api/toggl/stop", s.requireUser(s.apiTogglStop))
}

// togglAccount is the signed-in user's account; ok is false (and the
// answer written) when there is none to use.
func (s *Server) togglAccount(w http.ResponseWriter, r *http.Request, l *library.Lib) (toggl.Account, bool) {
	a, err := l.TogglAccount(r.Context(), s.box)
	if errors.Is(err, library.ErrNoToggl) {
		fail(w, http.StatusConflict, "Toggl is not connected for you — ask an admin to run `flamingo user toggl`")
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
