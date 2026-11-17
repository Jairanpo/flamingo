package server

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"regexp"
	"strconv"
	"strings"

	"github.com/jackc/pgx/v5"

	"github.com/Jairanpo/flamingo/internal/library"
)

// The /api routes the page uses, in the JSON shapes the Python server gave
// them, answered from the signed-in user's library.

var vidRE = regexp.MustCompile(`^[A-Za-z0-9_-]{11}$`)

// deviceHeader names the computer a request comes through. The agent sets
// it (phase 6); without it nothing counts as downloaded.
const deviceHeader = "X-Flamingo-Device"

func (s *Server) lib(r *http.Request) (*library.Lib, error) {
	l := &library.Lib{Pool: s.pool, User: CurrentUser(r).ID}
	if name := strings.TrimSpace(r.Header.Get(deviceHeader)); name != "" {
		err := s.pool.QueryRow(r.Context(), `SELECT devi_id FROM devices WHERE devi_name = $1`, name).Scan(&l.Device)
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return nil, err
		}
	}
	return l, nil
}

func fail(w http.ResponseWriter, code int, msg string) {
	writeJSON(w, code, map[string]string{"error": msg})
}

// failErr turns a library error into the answer the page expects.
func (s *Server) failErr(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, library.ErrUnknownSource):
		fail(w, http.StatusNotFound, "unknown source")
	case errors.Is(err, library.ErrUnknownVideo):
		fail(w, http.StatusNotFound, "unknown video")
	case errors.Is(err, library.ErrBadInput):
		fail(w, http.StatusBadRequest, err.Error())
	case errors.Is(err, errNotFound):
		fail(w, http.StatusNotFound, "unknown action")
	default:
		s.log.Error("api", "err", err)
		fail(w, http.StatusInternalServerError, "something went wrong on the server")
	}
}

// body reads a JSON object; an empty or broken body is an empty object,
// as the Python server treated it.
func body(r *http.Request) map[string]any {
	out := map[string]any{}
	b, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	if err == nil && len(b) > 0 {
		_ = json.Unmarshal(b, &out)
	}
	return out
}

func str(b map[string]any, k string) string {
	switch v := b[k].(type) {
	case string:
		return v
	case float64:
		return strconv.FormatFloat(v, 'f', -1, 64)
	}
	return ""
}

func num(b map[string]any, k string) (float64, bool) {
	switch v := b[k].(type) {
	case float64:
		return v, true
	case string:
		f, err := strconv.ParseFloat(v, 64)
		return f, err == nil
	case bool:
		if v {
			return 1, true
		}
		return 0, true
	}
	return 0, false
}

// truthy follows Python's bool(): missing → def.
func truthy(b map[string]any, k string, def bool) bool {
	v, ok := b[k]
	if !ok || v == nil {
		return def
	}
	switch x := v.(type) {
	case bool:
		return x
	case float64:
		return x != 0
	case string:
		return x != ""
	}
	return true
}

// optBool reads a 0/1/true/false value; nil when absent (meaning "flip").
func optBool(b map[string]any, k string) *bool {
	if _, ok := b[k]; !ok {
		return nil
	}
	v := truthy(b, k, false)
	return &v
}

func (s *Server) routesAPI(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/state", s.requireUser(s.apiState))
	mux.HandleFunc("GET /api/collections/{vid}", s.requireUser(s.apiCollectionsFor))
	mux.HandleFunc("GET /api/marks/{vid}", s.requireUser(s.apiMarks))
	mux.HandleFunc("POST /api/collections", s.requireUser(s.apiCreateCollection))
	mux.HandleFunc("POST /api/shelve", s.requireUser(s.apiShelve))
	mux.HandleFunc("POST /api/pin", s.requireUser(s.apiPin))
	mux.HandleFunc("POST /api/forget", s.requireUser(s.apiForget))
	mux.HandleFunc("POST /api/toggl", s.requireUser(s.apiToggl))
	mux.HandleFunc("POST /api/topic", s.requireUser(s.apiTopic))
	mux.HandleFunc("POST /api/sort", s.requireUser(s.apiSort))
	mux.HandleFunc("POST /api/{action}/{vid}", s.requireUser(s.apiAction))
}

type sourceJSON struct {
	ID        string  `json:"id"`
	Kind      string  `json:"kind"`
	Section   *string `json:"section"`
	Name      string  `json:"name"`
	Creator   *string `json:"creator"`
	Handle    *string `json:"handle"`
	Group     string  `json:"group"`
	Topic     string  `json:"topic"`
	Owner     *string `json:"owner"`
	TgChannel string  `json:"tgchannel"`
	Shelved   bool    `json:"shelved"`
	Pinned    bool    `json:"pinned"`
	Total     int     `json:"total"`
	Have      int     `json:"have"`
	Seen      int     `json:"seen"`
}

type pinJSON struct {
	ID   string         `json:"id"`
	Next *library.Video `json:"next"`
	// After is a peek at what follows next, so the card shows the road ahead.
	After []*library.Video `json:"after"`
}

func (s *Server) apiState(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	l, err := s.lib(r)
	if err != nil {
		s.failErr(w, err)
		return
	}
	qs := r.URL.Query()
	q := library.Query{
		Source:    qs.Get("source"),
		Q:         strings.TrimSpace(qs.Get("q")),
		Starred:   qs.Get("starred") == "1",
		Unwatched: qs.Get("unwatched") == "1",
		Hidden:    qs.Get("hidden") == "1",
		Limit:     300,
	}
	switch qs.Get("have") {
	case "1":
		t := true
		q.Have = &t
	case "0":
		f := false
		q.Have = &f
	}
	if c := qs.Get("collection"); c != "" {
		n, err := strconv.ParseInt(c, 10, 32)
		if err != nil {
			fail(w, http.StatusBadRequest, "bad collection")
			return
		}
		q.Collection = int32(n)
	}
	if v := qs.Get("limit"); v != "" {
		if q.Limit, err = strconv.Atoi(v); err != nil {
			fail(w, http.StatusBadRequest, "bad limit")
			return
		}
	}
	if v := qs.Get("offset"); v != "" {
		if q.Offset, err = strconv.Atoi(v); err != nil {
			fail(w, http.StatusBadRequest, "bad offset")
			return
		}
	}
	// No sort asked for means whatever this view was last set to.
	q.Sort = qs.Get("sort")
	if q.Sort == "" {
		if q.Sort, err = l.SortFor(ctx, q.Source, q.Collection); err != nil {
			s.failErr(w, err)
			return
		}
	}
	if !library.ValidSort(q.Sort) {
		q.Sort = "default"
	}

	videos, err := l.Videos(ctx, q)
	if err != nil {
		s.failErr(w, err)
		return
	}
	srcs, err := l.Sources(ctx)
	if err != nil {
		s.failErr(w, err)
		return
	}
	var sources []sourceJSON
	isPlaylist := false
	for _, g := range library.ByCreator(srcs) {
		items := g.Items
		if g.Channel != nil {
			items = append([]*library.Source{g.Channel}, items...)
		}
		for _, src := range items {
			name := src.Title
			if name == "" && src.Handle != nil {
				name = *src.Handle
			}
			var owner *string
			if src.Kind != "channel" {
				owner = src.ChanName
			}
			tg := ""
			if src.ChanName != nil {
				tg = strings.TrimSpace(*src.ChanName)
			}
			sources = append(sources, sourceJSON{
				ID: src.ID, Kind: src.Kind, Section: src.Section, Name: name,
				Creator: g.Creator, Handle: src.Handle, Group: g.Key, Topic: src.Topic, Owner: owner,
				TgChannel: tg, Shelved: src.Shelved, Pinned: src.Pinned,
				Total: src.Total, Have: src.Have, Seen: src.Seen,
			})
			if src.ID == q.Source && src.Kind == "playlist" {
				isPlaylist = true
			}
		}
	}
	if sources == nil {
		sources = []sourceJSON{}
	}

	colls, err := l.Collections(ctx)
	if err != nil {
		s.failErr(w, err)
		return
	}
	stats, err := l.Stats(ctx)
	if err != nil {
		s.failErr(w, err)
		return
	}

	// The home dashboard: each pin with the next video to watch and the three
	// after it, oldest pin first.
	var pinned []*library.Source
	for _, src := range srcs {
		if src.Pinned {
			pinned = append(pinned, src)
		}
	}
	sortPins(pinned)
	pins := []pinJSON{}
	for _, p := range pinned {
		sort, err := l.SortFor(ctx, p.ID, 0)
		if err != nil {
			s.failErr(w, err)
			return
		}
		next, err := l.Videos(ctx, library.Query{Source: p.ID, Unwatched: true, Limit: 4, Sort: sort})
		if err != nil {
			s.failErr(w, err)
			return
		}
		pj := pinJSON{ID: p.ID, After: []*library.Video{}}
		if len(next) > 0 {
			pj.Next, pj.After = next[0], next[1:]
		}
		pins = append(pins, pj)
	}

	toggl, err := l.TogglMap(ctx)
	if err != nil {
		s.failErr(w, err)
		return
	}
	tgChannels, err := l.TogglChannels(ctx)
	if err != nil {
		s.failErr(w, err)
		return
	}

	// "default" means something different per view; name what it does here.
	sorts := make([]map[string]string, 0, len(library.Sorts))
	for _, so := range library.Sorts {
		label := so.Label
		if so.Key == "default" {
			if q.Collection != 0 {
				label = "Collection order (yours)"
			} else if isPlaylist {
				label = "Playlist order (the author's)"
			}
		}
		sorts = append(sorts, map[string]string{"key": so.Key, "label": label})
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"videos":         videos,
		"sources":        sources,
		"collections":    colls,
		"ordered":        (q.Collection != 0 || isPlaylist) && q.Sort == "default",
		"sort":           q.Sort,
		"sorts":          sorts,
		"stats":          stats,
		"jobs":           []any{}, // catalogue jobs arrive with step 4b
		"pins":           pins,
		"max_pins":       library.MaxPins,
		"toggl":          toggl,
		"toggl_channels": tgChannels,
	})
}

func sortPins(ps []*library.Source) {
	for i := 1; i < len(ps); i++ {
		for j := i; j > 0 && *ps[j].PinnedAt < *ps[j-1].PinnedAt; j-- {
			ps[j], ps[j-1] = ps[j-1], ps[j]
		}
	}
}

func (s *Server) apiCollectionsFor(w http.ResponseWriter, r *http.Request) {
	vid := r.PathValue("vid")
	if !vidRE.MatchString(vid) {
		fail(w, http.StatusBadRequest, "bad id")
		return
	}
	l, err := s.lib(r)
	if err != nil {
		s.failErr(w, err)
		return
	}
	refs, err := l.CollectionsFor(r.Context(), vid)
	if err != nil {
		s.failErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"collections": refs})
}

func (s *Server) apiMarks(w http.ResponseWriter, r *http.Request) {
	vid := r.PathValue("vid")
	if !vidRE.MatchString(vid) {
		fail(w, http.StatusBadRequest, "bad id")
		return
	}
	l, err := s.lib(r)
	if err != nil {
		s.failErr(w, err)
		return
	}
	marks, err := l.Marks(r.Context(), vid)
	if err != nil {
		s.failErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"marks": marks})
}

// withLib runs fn with the request's library and body, answering errors.
func (s *Server) withLib(fn func(ctx context.Context, l *library.Lib, b map[string]any) (any, error)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		b := body(r)
		l, err := s.lib(r)
		if err != nil {
			s.failErr(w, err)
			return
		}
		out, err := fn(r.Context(), l, b)
		if err != nil {
			s.failErr(w, err)
			return
		}
		writeJSON(w, http.StatusOK, out)
	}
}

func (s *Server) apiCreateCollection(w http.ResponseWriter, r *http.Request) {
	s.withLib(func(ctx context.Context, l *library.Lib, b map[string]any) (any, error) {
		c, err := l.CreateCollection(ctx, str(b, "name"))
		if err != nil {
			return nil, err
		}
		return map[string]any{"ok": true, "id": c.ID, "name": c.Name}, nil
	})(w, r)
}

func (s *Server) apiShelve(w http.ResponseWriter, r *http.Request) {
	s.withLib(func(ctx context.Context, l *library.Lib, b map[string]any) (any, error) {
		v := truthy(b, "value", true)
		if err := l.SetShelved(ctx, str(b, "source"), v); err != nil {
			return nil, err
		}
		return map[string]any{"ok": true, "shelved": v}, nil
	})(w, r)
}

func (s *Server) apiPin(w http.ResponseWriter, r *http.Request) {
	s.withLib(func(ctx context.Context, l *library.Lib, b map[string]any) (any, error) {
		if err := l.SetPinned(ctx, str(b, "source"), truthy(b, "value", true)); err != nil {
			if errors.Is(err, library.ErrUnknownSource) {
				// The Python server answered an unknown pin with 400.
				return nil, inputErr("unknown source")
			}
			return nil, err
		}
		return map[string]any{"ok": true}, nil
	})(w, r)
}

type inputErr string

func (e inputErr) Error() string { return string(e) }
func (e inputErr) Unwrap() error { return library.ErrBadInput }

func (s *Server) apiForget(w http.ResponseWriter, r *http.Request) {
	s.withLib(func(ctx context.Context, l *library.Lib, b map[string]any) (any, error) {
		if err := l.Forget(ctx, str(b, "source")); err != nil {
			return nil, err
		}
		return map[string]any{"ok": true}, nil
	})(w, r)
}

func (s *Server) apiToggl(w http.ResponseWriter, r *http.Request) {
	s.withLib(func(ctx context.Context, l *library.Lib, b map[string]any) (any, error) {
		if err := l.SetToggl(ctx, str(b, "channel"), str(b, "name"), str(b, "project"), str(b, "tags")); err != nil {
			return nil, err
		}
		chans, err := l.TogglChannels(ctx)
		if err != nil {
			return nil, err
		}
		return map[string]any{"ok": true, "channels": chans}, nil
	})(w, r)
}

func (s *Server) apiTopic(w http.ResponseWriter, r *http.Request) {
	s.withLib(func(ctx context.Context, l *library.Lib, b map[string]any) (any, error) {
		if err := l.SetTopic(ctx, str(b, "creator"), str(b, "topic")); err != nil {
			return nil, err
		}
		return map[string]any{"ok": true}, nil
	})(w, r)
}

func (s *Server) apiSort(w http.ResponseWriter, r *http.Request) {
	s.withLib(func(ctx context.Context, l *library.Lib, b map[string]any) (any, error) {
		sort := str(b, "sort")
		if sort == "" {
			sort = "default"
		}
		var coll int32
		if c, ok := num(b, "collection"); ok && c != 0 {
			coll = int32(c)
		}
		if err := l.SetSort(ctx, sort, str(b, "source"), coll); err != nil {
			return nil, err
		}
		return map[string]any{"ok": true, "sort": sort}, nil
	})(w, r)
}

// apiAction is POST /api/<action>/<video id>.
func (s *Server) apiAction(w http.ResponseWriter, r *http.Request) {
	action, vid := r.PathValue("action"), r.PathValue("vid")
	if !vidRE.MatchString(vid) {
		_ = body(r)
		fail(w, http.StatusBadRequest, "bad video id")
		return
	}
	s.withLib(func(ctx context.Context, l *library.Lib, b map[string]any) (any, error) {
		if _, err := l.Video(ctx, vid); err != nil {
			return nil, err
		}
		switch action {
		case "star":
			v, err := l.ToggleStar(ctx, vid)
			return map[string]any{"ok": true, "starred": v}, err
		case "watched":
			v, err := l.SetWatched(ctx, vid, optBool(b, "value"))
			return map[string]any{"ok": true, "watched": v}, err
		case "hidden":
			v, err := l.SetHidden(ctx, vid, optBool(b, "value"))
			return map[string]any{"ok": true, "hidden": v}, err
		case "progress":
			p, _ := num(b, "value")
			return map[string]any{"ok": true}, l.SetProgress(ctx, vid, p)
		case "marks":
			// One endpoint, three verbs: add {t, note}, note {id, note}, drop {id}.
			var err error
			if t, ok := num(b, "t"); ok {
				err = l.AddMark(ctx, vid, t, str(b, "note"))
			} else if d, ok := num(b, "drop"); ok && d != 0 {
				err = l.DeleteMark(ctx, vid, int32(d))
			} else if id, ok := num(b, "id"); ok {
				err = l.EditMark(ctx, vid, int32(id), str(b, "note"))
			} else {
				return nil, inputErr("say t, id or drop")
			}
			if err != nil {
				return nil, err
			}
			marks, err := l.Marks(ctx, vid)
			return map[string]any{"ok": true, "marks": marks}, err
		case "collect":
			c, ok := num(b, "collection")
			if !ok {
				return nil, inputErr("collection required")
			}
			var err error
			if truthy(b, "remove", false) {
				err = l.CollectionRemove(ctx, int32(c), vid)
			} else {
				err = l.CollectionAdd(ctx, int32(c), vid)
			}
			if err != nil {
				return nil, err
			}
			refs, err := l.CollectionsFor(ctx, vid)
			return map[string]any{"ok": true, "collections": refs}, err
		}
		return nil, errNotFound
	})(w, r)
}

var errNotFound = errors.New("not found")
