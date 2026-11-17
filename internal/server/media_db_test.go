package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/Jairanpo/flamingo/internal/library"
	"github.com/Jairanpo/flamingo/internal/secret"
	"github.com/Jairanpo/flamingo/internal/toggl"
)

func TestChaptersAPI(t *testing.T) {
	ts, pool, s := testServer(t)
	ctx := context.Background()
	seed(t, pool)
	s.cat.YT = fakeYT{}
	s.auth.CreateUser(ctx, "ana", "", "secret-pass", false)
	a := signIn(t, ts.URL, "ana")

	code, got := a.do("GET", "/api/chapters/"+tV1, nil)
	eq(t, "v1 chapters", []any{code, got["chapters"]},
		[]any{200, []map[string]any{{"start": 0, "end": 30, "title": "Intro"}, {"start": 30, "end": 60, "title": "Rest"}}})
	// Stored: YouTube is not asked again, even if it would now say otherwise.
	s.cat.YT = nil
	code, got = a.do("GET", "/api/chapters/"+tV1, nil)
	eq(t, "v1 again", []any{code, len(got["chapters"].([]any))}, []any{200, 2})

	// None found is remembered as none.
	s.cat.YT = fakeYT{}
	code, got = a.do("GET", "/api/chapters/"+tV2, nil)
	eq(t, "v2", []any{code, got["chapters"]}, []any{200, []any{}})
	var checked bool
	pool.QueryRow(ctx, `SELECT vid_chapters_checked_at IS NOT NULL FROM videos WHERE vid_id = $1`, tV2).Scan(&checked)
	eq(t, "v2 checked", checked, true)

	// YouTube failing: none for now, asked again next time.
	code, got = a.do("GET", "/api/chapters/"+tV3, nil)
	eq(t, "v3", []any{code, got["chapters"]}, []any{200, []any{}})
	pool.QueryRow(ctx, `SELECT vid_chapters_checked_at IS NOT NULL FROM videos WHERE vid_id = $1`, tV3).Scan(&checked)
	eq(t, "v3 not checked", checked, false)

	code, _ = a.do("GET", "/api/chapters/zzzzzzzzzzz", nil)
	eq(t, "unknown", code, 404)
}

func TestTogglAPI(t *testing.T) {
	var mu sync.Mutex
	var started map[string]any
	running := false
	fake := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		if u, _, _ := r.BasicAuth(); u != "ana-token" {
			w.WriteHeader(http.StatusForbidden)
			return
		}
		switch {
		case r.URL.Path == "/me":
			w.Write([]byte(`{"default_workspace_id": 7}`))
		case r.URL.Path == "/me/time_entries/current":
			if running {
				w.Write([]byte(`{"id": 1, "workspace_id": 7, "description": "x", "tags": []}`))
			} else {
				w.Write([]byte(`null`))
			}
		case r.URL.Path == "/workspaces/7/projects":
			w.Write([]byte(`[{"id": 3, "name": "Learning"}]`))
		case r.Method == "POST":
			json.NewDecoder(r.Body).Decode(&started)
			running = true
			json.NewEncoder(w).Encode(map[string]any{"id": 1, "workspace_id": 7,
				"description": started["description"], "tags": started["tags"]})
		case r.Method == "PATCH" && r.URL.Path == "/workspaces/7/time_entries/1/stop":
			if !running {
				w.WriteHeader(http.StatusConflict)
				return
			}
			running = false
			w.Write([]byte(`{}`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer fake.Close()

	ts, pool, s := testServer(t)
	ctx := context.Background()
	seed(t, pool)
	s.toggl = &toggl.Client{API: fake.URL}
	s.box = secret.New([]byte("test key material"))
	ana, _ := s.auth.CreateUser(ctx, "ana", "", "secret-pass", false)
	s.auth.CreateUser(ctx, "bob", "", "secret-pass", false)
	pool.Exec(ctx, `INSERT INTO user_channels (user_id, chan_id) VALUES ($1, $2)`, ana.ID, tChan)
	pool.Exec(ctx, `INSERT INTO user_playlists (user_id, play_id) VALUES ($1, $2)`, ana.ID, tPl)
	a, b := signIn(t, ts.URL, "ana"), signIn(t, ts.URL, "bob")

	// Not connected: no button, and start says why.
	_, st := a.do("GET", "/api/toggl/status", nil)
	eq(t, "status before", st, map[string]any{"configured": false, "running": map[string]any{}})
	code, _ := a.do("POST", "/api/toggl/start", map[string]any{"video": tV1})
	eq(t, "start unconnected", code, http.StatusConflict)

	if err := library.SetTogglAccount(ctx, pool, s.box, "ana", " ana-token \n", 0); err != nil {
		t.Fatal(err)
	}
	var sealed []byte
	pool.QueryRow(ctx, `SELECT toac_token_sealed FROM toggl_accounts`).Scan(&sealed)
	if string(sealed) == "ana-token" || len(sealed) == 0 {
		t.Fatal("token stored in the clear")
	}
	lib := &library.Lib{Pool: pool, User: ana.ID}
	if err := lib.SetToggl(ctx, "Alpha", "", "learning", "Study, youtube"); err != nil {
		t.Fatal(err)
	}

	_, st = a.do("GET", "/api/toggl/status", nil)
	eq(t, "status after", st, map[string]any{"configured": true, "running": map[string]any{}})
	_, st = b.do("GET", "/api/toggl/status", nil)
	eq(t, "bob still off", st["configured"], false)

	// From the course: named after it, tagged, filed under the project.
	code, st = a.do("POST", "/api/toggl/start", map[string]any{"video": tV2, "source": tPl})
	eq(t, "start", []any{code, st["ok"], st["project_missing"]}, []any{200, true, nil})
	eq(t, "sent", []any{started["description"], started["tags"], started["project_id"], started["workspace_id"]},
		[]any{"Alpha Course", []string{"Study", "youtube", "Youtube:Alpha"}, 3, 7})
	// From the channel: the channel's name.
	a.do("POST", "/api/toggl/start", map[string]any{"video": tV1, "source": tChan})
	eq(t, "channel entry", started["description"], "Alpha")

	code, _ = a.do("POST", "/api/toggl/start", map[string]any{"video": "zzzzzzzzzzz"})
	eq(t, "unknown video", code, 404)

	// Stop with no id stops what is running; stopping again is no error.
	code, st = a.do("POST", "/api/toggl/stop", map[string]any{})
	eq(t, "stop", []any{code, st["running"]}, []any{200, map[string]any{}})
	code, st = a.do("POST", "/api/toggl/stop", map[string]any{"id": 1, "workspace": 7})
	eq(t, "stop stopped", []any{code, st["running"]}, []any{200, map[string]any{}})

	// A token sealed under another key: the button shows, with an error.
	s.box = secret.New([]byte("a different key"))
	_, st = a.do("GET", "/api/toggl/status", nil)
	eq(t, "wrong key", []any{st["configured"], st["error"] != nil}, []any{true, true})

	// Disconnecting removes the row.
	if err := library.SetTogglAccount(ctx, pool, nil, "ana", "", 0); err != nil {
		t.Fatal(err)
	}
	_, st = a.do("GET", "/api/toggl/status", nil)
	eq(t, "disconnected", st["configured"], false)

	// Each person sets their own token from the page.
	_, st = a.do("GET", "/api/toggl/account", nil)
	eq(t, "account before", []any{st["connected"], st["can_store"]}, []any{false, true})
	code, st = a.do("PUT", "/api/toggl/account", map[string]any{"token": "wrong"})
	if code != http.StatusBadGateway {
		t.Errorf("a token Toggl refuses = %d %v", code, st)
	}
	code, _ = a.do("PUT", "/api/toggl/account", map[string]any{"token": "ana-token", "workspace": -1})
	eq(t, "bad workspace", code, http.StatusBadRequest)
	code, st = a.do("PUT", "/api/toggl/account", map[string]any{"token": " ana-token ", "workspace": "7"})
	if code != 200 || st["connected"] != true || st["token"] != nil {
		t.Fatalf("connect = %d %v", code, st)
	}
	_, st = a.do("GET", "/api/toggl/account", nil)
	eq(t, "account after", []any{st["connected"], st["workspace"], st["token"]}, []any{true, 7.0, nil})
	_, st = a.do("GET", "/api/toggl/status", nil)
	eq(t, "timer back", st["configured"], true)
	_, st = b.do("GET", "/api/toggl/account", nil)
	eq(t, "bob untouched", st["connected"], false)
	code, _ = a.do("DELETE", "/api/toggl/account", nil)
	eq(t, "disconnect", code, 200)
	_, st = a.do("GET", "/api/toggl/account", nil)
	eq(t, "account gone", st["connected"], false)
}
