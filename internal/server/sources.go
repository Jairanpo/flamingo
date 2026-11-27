package server

import (
	"context"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/Jairanpo/flamingo/internal/catalog"
	"github.com/Jairanpo/flamingo/internal/ytdlp"
)

// Adding sources and browsing a creator, as the Python server did: Add goes
// on a queue and reports through job cards; Browse is answered while the
// request waits, since it takes a few seconds and the sheet says so.

// A card is a source being added, keyed by what was typed, or (once the
// agent arrives) a download keyed by video id.
var jobIDRE = regexp.MustCompile(`(?s)^(?:src:.{1,400}|[A-Za-z0-9_-]{11})$`)

func (s *Server) routesSources(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/jobs", s.requireUser(s.apiJobs))
	mux.HandleFunc("POST /api/jobs/clear", s.requireUser(s.apiJobsClear))
	mux.HandleFunc("POST /api/sources", s.requireUser(s.apiAddSources))
	mux.HandleFunc("POST /api/sources/browse", s.requireUser(s.apiBrowse))
	mux.HandleFunc("POST /api/sources/refresh", s.requireUser(s.apiRefreshSource))
}

// apiRefreshSource takes {source} and lists that one source again, as
// `flamingo sync --source` does, on the same queue as adding. Only a source
// the user tracks: anyone else's would be a free way to make us call YouTube.
func (s *Server) apiRefreshSource(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimSpace(str(body(r), "source"))
	if id == "" || len(id) > 64 {
		fail(w, http.StatusBadRequest, "which source?")
		return
	}
	user := CurrentUser(r).ID
	var mine bool
	err := s.pool.QueryRow(r.Context(), `
		SELECT EXISTS (SELECT 1 FROM user_channels WHERE user_id = $1 AND chan_id = $2)
		    OR EXISTS (SELECT 1 FROM user_playlists WHERE user_id = $1 AND play_id = $2)
		    OR EXISTS (SELECT 1 FROM user_picks WHERE user_id = $1
		                  AND $3 || chan_id = $2)`,
		user, id, catalog.PicksPrefix).Scan(&mine)
	if err != nil {
		fail(w, http.StatusInternalServerError, "could not check the source")
		return
	}
	if !mine {
		fail(w, http.StatusNotFound, "you don't track that source")
		return
	}
	targets, _, err := s.jobs.Cat.Targets(r.Context(), catalog.SyncOptions{Source: id})
	if err != nil || len(targets) == 0 {
		fail(w, http.StatusNotFound, "nothing to refresh")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "job": s.jobs.EnqueueRefresh(user, targets[0])})
}

func (s *Server) apiJobs(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"jobs": s.jobs.Snapshot(CurrentUser(r).ID)})
}

func (s *Server) apiJobsClear(w http.ResponseWriter, r *http.Request) {
	user := CurrentUser(r).ID
	id := strings.TrimSpace(str(body(r), "id"))
	if id == "" {
		s.jobs.ForgetFinished(user)
		writeJSON(w, http.StatusOK, map[string]any{"ok": true})
		return
	}
	if !jobIDRE.MatchString(id) {
		fail(w, http.StatusBadRequest, "bad job id")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": s.jobs.Forget(user, id)})
}

// apiAddSources takes {ref} or {refs: [...]}, where a browsed pick arrives
// as {ref, section}: only Browse can tell a course from any other playlist.
func (s *Server) apiAddSources(w http.ResponseWriter, r *http.Request) {
	b := body(r)
	raw, ok := b["refs"]
	if !ok || raw == nil {
		raw = []any{b["ref"]}
	}
	list, ok := raw.([]any)
	if !ok {
		fail(w, http.StatusBadRequest, "refs must be a list")
		return
	}
	if len(list) < 1 || len(list) > 50 {
		fail(w, http.StatusBadRequest, "pick between 1 and 50 at a time")
		return
	}
	type pick struct{ ref, section string }
	var picks []pick
	for _, item := range list {
		var p pick
		switch v := item.(type) {
		case string:
			p.ref = v
		case map[string]any:
			p.ref = str(v, "ref")
			if str(v, "section") == "course" {
				p.section = "course"
			}
		}
		ref, err := ytdlp.CleanSourceRef(p.ref)
		if err != nil {
			fail(w, http.StatusBadRequest, err.Error())
			return
		}
		p.ref = ref
		picks = append(picks, p)
	}
	user := CurrentUser(r).ID
	jobs := make([]catalog.Job, 0, len(picks))
	for _, p := range picks {
		jobs = append(jobs, s.jobs.Enqueue(user, p.ref, p.section))
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "job": jobs[0], "jobs": jobs})
}

type browsed struct {
	ID      string `json:"id"`
	Title   string `json:"title"`
	Section string `json:"section"`
	Tracked bool   `json:"tracked"`
}

func (s *Server) apiBrowse(w http.ResponseWriter, r *http.Request) {
	ref, err := ytdlp.CleanCreatorRef(str(body(r), "creator"))
	if err != nil {
		fail(w, http.StatusBadRequest, err.Error())
		return
	}
	// Capped, so a wedged yt-dlp cannot hold the request for good.
	ctx, cancel := context.WithTimeout(r.Context(), 3*time.Minute)
	defer cancel()
	owner, found, err := s.cat.YT.Browse(ctx, ref)
	if err != nil {
		// YouTube or yt-dlp said no; the page shows the reason.
		fail(w, http.StatusBadGateway, err.Error())
		return
	}
	var courses []string
	for _, it := range found {
		if it.Section == "course" {
			courses = append(courses, it.ID)
		}
	}
	if err := s.cat.MarkCourses(r.Context(), courses); err != nil {
		s.failErr(w, err)
		return
	}
	tracked := map[string]bool{}
	rows, err := s.pool.Query(r.Context(), `SELECT play_id FROM user_playlists WHERE user_id = $1`, CurrentUser(r).ID)
	if err != nil {
		s.failErr(w, err)
		return
	}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			s.failErr(w, err)
			return
		}
		tracked[id] = true
	}
	rows.Close()
	out := make([]browsed, 0, len(found))
	for _, it := range found {
		out = append(out, browsed{ID: it.ID, Title: it.Title, Section: it.Section, Tracked: tracked[it.ID]})
	}
	if owner == "" {
		owner = ref
	}
	writeJSON(w, http.StatusOK, map[string]any{"creator": owner, "playlists": out})
}
