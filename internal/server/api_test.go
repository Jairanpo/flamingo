package server

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
)

const (
	tChan = "UCaaaaaaaaaaaaaaaaaaaaaa"
	tPl   = "PLtest"
	tV1   = "aaaaaaaaaa1"
	tV2   = "aaaaaaaaaa2"
	tV3   = "aaaaaaaaaa3"
)

// seed puts a small catalog in place: one channel with two uploads and a
// playlist holding the second and a third video, plus a device that has v1.
func seed(t *testing.T, pool *pgxpool.Pool) {
	t.Helper()
	for _, q := range []string{
		`INSERT INTO channels (chan_id, chan_handle, chan_name) VALUES ('` + tChan + `', '@alpha', 'Alpha')`,
		`INSERT INTO playlists (play_id, chan_id, plse_code, play_title) VALUES ('` + tPl + `', '` + tChan + `', 'COURSE', 'Alpha Course')`,
		`INSERT INTO videos (vid_id, chan_id, vid_title, vid_published_on, vid_channel_position, vid_duration_seconds) VALUES
		   ('` + tV1 + `', '` + tChan + `', 'One', '2024-01-01', 1, 60),
		   ('` + tV2 + `', '` + tChan + `', 'Two', '2024-02-01', 0, 120),
		   ('` + tV3 + `', NULL, 'Three', NULL, NULL, NULL)`,
		`INSERT INTO playlist_videos (play_id, vid_id, plvi_position) VALUES ('` + tPl + `', '` + tV3 + `', 0), ('` + tPl + `', '` + tV2 + `', 1)`,
		`INSERT INTO devices (devi_name, devi_token_hash) VALUES ('desk', sha256('desk-token'::bytea))`,
		`INSERT INTO device_videos (devi_id, vid_id, dvvi_size_bytes, dvvi_downloaded_at) SELECT devi_id, '` + tV1 + `', 100, now() FROM devices`,
	} {
		if _, err := pool.Exec(context.Background(), q); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
}

type client struct {
	t     *testing.T
	base  string
	http  *http.Client
	token string // the device token an agent would add; "" for none
}

func signIn(t *testing.T, base, name string) *client {
	c := &client{t: t, base: base, http: browser(t), token: "desk-token"}
	r := post(t, c.http, base+"/login", url.Values{"name": {name}, "password": {"secret-pass"}}, "")
	if r.StatusCode != http.StatusSeeOther {
		t.Fatalf("sign in %s: %d", name, r.StatusCode)
	}
	return c
}

func (c *client) do(method, path string, body any) (int, map[string]any) {
	c.t.Helper()
	var rd *bytes.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = bytes.NewReader(b)
	} else {
		rd = bytes.NewReader(nil)
	}
	req, _ := http.NewRequest(method, c.base+path, rd)
	if c.token != "" {
		req.Header.Set(deviceHeader, c.token)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		c.t.Fatal(err)
	}
	defer resp.Body.Close()
	out := map[string]any{}
	_ = json.NewDecoder(resp.Body).Decode(&out)
	return resp.StatusCode, out
}

func (c *client) state(query string) map[string]any {
	code, s := c.do("GET", "/api/state?"+query, nil)
	if code != 200 {
		c.t.Fatalf("state %s: %d %v", query, code, s)
	}
	return s
}

func ids(s map[string]any) []string {
	var out []string
	for _, v := range s["videos"].([]any) {
		out = append(out, v.(map[string]any)["id"].(string))
	}
	return out
}

func eq(t *testing.T, what string, got, want any) {
	t.Helper()
	g, _ := json.Marshal(got)
	w, _ := json.Marshal(want)
	if string(g) != string(w) {
		t.Errorf("%s: got %s, want %s", what, g, w)
	}
}

func TestLibraryAPI(t *testing.T) {
	ts, pool, s := testServer(t)
	ctx := context.Background()
	seed(t, pool)
	ana, _ := s.auth.CreateUser(ctx, "ana", "", "secret-pass", false)
	bob, _ := s.auth.CreateUser(ctx, "bob", "", "secret-pass", false)
	// Ana tracks the channel and the course; Bob only the course.
	for _, q := range []struct {
		sql  string
		args []any
	}{
		{`INSERT INTO user_channels (user_id, chan_id) VALUES ($1, $2)`, []any{ana.ID, tChan}},
		{`INSERT INTO user_playlists (user_id, play_id) VALUES ($1, $2)`, []any{ana.ID, tPl}},
		{`INSERT INTO user_playlists (user_id, play_id) VALUES ($1, $2)`, []any{bob.ID, tPl}},
	} {
		if _, err := pool.Exec(ctx, q.sql, q.args...); err != nil {
			t.Fatal(err)
		}
	}
	a, b := signIn(t, ts.URL, "ana"), signIn(t, ts.URL, "bob")

	// Everything holds the union of Ana's sources, newest first.
	st := a.state("")
	eq(t, "ana everything", ids(st), []string{tV2, tV1, tV3})
	eq(t, "bob everything", ids(b.state("")), []string{tV2, tV3})
	stats := st["stats"].(map[string]any)
	eq(t, "stats", []any{stats["total"], stats["have"], stats["bytes"], stats["channels"], stats["playlists"]},
		[]any{3, 1, 100, 1, 1})

	// Sources: grouped under Alpha, channel first, with counts.
	srcs := st["sources"].([]any)
	first, second := srcs[0].(map[string]any), srcs[1].(map[string]any)
	eq(t, "source order", []any{first["id"], second["id"]}, []any{tChan, tPl})
	eq(t, "course", []any{second["section"], second["owner"], second["creator"], second["total"], second["group"]},
		[]any{"course", "Alpha", "Alpha", 2, tChan})

	// A playlist keeps its author's order and says so.
	pl := a.state("source=" + tPl)
	eq(t, "playlist order", ids(pl), []string{tV3, tV2})
	eq(t, "ordered", pl["ordered"], true)
	eq(t, "default label", pl["sorts"].([]any)[0].(map[string]any)["label"], "Playlist order (the author's)")
	eq(t, "explicit sort", ids(a.state("source="+tPl+"&sort=title")), []string{tV3, tV2})
	eq(t, "have filter", ids(a.state("have=1")), []string{tV1})
	eq(t, "search", ids(a.state("q=thr")), []string{tV3})

	// Stars, watched and progress belong to the one who set them.
	if code, r := a.do("POST", "/api/star/"+tV2, nil); code != 200 || r["starred"] != true {
		t.Fatalf("star: %d %v", code, r)
	}
	a.do("POST", "/api/watched/"+tV2, map[string]any{"value": 1})
	a.do("POST", "/api/progress/"+tV3, map[string]any{"value": 42.5})
	eq(t, "starred", ids(a.state("starred=1")), []string{tV2})
	eq(t, "bob sees no star", ids(b.state("starred=1")), []string(nil))
	eq(t, "unwatched", ids(a.state("unwatched=1")), []string{tV1, tV3})
	if code, r := a.do("POST", "/api/watched/"+tV2, nil); code != 200 || r["watched"] != false {
		t.Errorf("watched toggles back: %v", r)
	}

	// Hiding takes a video out of every view of that user only.
	a.do("POST", "/api/hidden/"+tV1, map[string]any{"value": 1})
	eq(t, "hidden gone", ids(a.state("")), []string{tV2, tV3})
	eq(t, "hidden list", ids(a.state("hidden=1")), []string{tV1})

	// Marks: add, edit, drop.
	_, r := a.do("POST", "/api/marks/"+tV2, map[string]any{"t": 12.5, "note": " good bit "})
	marks := r["marks"].([]any)
	m := marks[0].(map[string]any)
	eq(t, "mark", []any{m["t"], m["note"]}, []any{12.5, "good bit"})
	_, r = a.do("POST", "/api/marks/"+tV2, map[string]any{"id": m["id"], "note": "better"})
	eq(t, "mark edited", r["marks"].([]any)[0].(map[string]any)["note"], "better")
	_, r = b.do("GET", "/api/marks/"+tV2, nil)
	eq(t, "bob has no marks", len(r["marks"].([]any)), 0)
	_, r = a.do("POST", "/api/marks/"+tV2, map[string]any{"drop": m["id"]})
	eq(t, "mark dropped", len(r["marks"].([]any)), 0)

	// Collections are private.
	_, r = a.do("POST", "/api/collections", map[string]any{"name": "Later"})
	cid := r["id"]
	_, r = a.do("POST", "/api/collections", map[string]any{"name": "later"})
	eq(t, "same collection", r["id"], cid)
	_, r = a.do("POST", "/api/collect/"+tV3, map[string]any{"collection": cid})
	eq(t, "collected", len(r["collections"].([]any)), 1)
	if code, _ := b.do("POST", "/api/collect/"+tV3, map[string]any{"collection": cid}); code != 400 {
		t.Errorf("bob used ana's collection: %d", code)
	}
	coll := a.state("collection=" + jsonNum(cid))
	eq(t, "collection view", ids(coll), []string{tV3})

	// Sort memory: per view, then the user's default.
	a.do("POST", "/api/sort", map[string]any{"sort": "oldest", "source": tChan})
	eq(t, "remembered sort", a.state("source=" + tChan)["sort"], "oldest")
	a.do("POST", "/api/sort", map[string]any{"sort": "title"})
	eq(t, "global sort", a.state("")["sort"], "title")
	eq(t, "view keeps its own", a.state("source=" + tChan)["sort"], "oldest")
	a.do("POST", "/api/sort", map[string]any{"sort": "default", "source": tChan})
	eq(t, "falls back to global", a.state("source=" + tChan)["sort"], "title")
	eq(t, "bob untouched", b.state("")["sort"], "default")

	// Pins: the dashboard lists the next unwatched video; cap applies.
	if code, r := a.do("POST", "/api/pin", map[string]any{"source": tPl}); code != 200 {
		t.Fatalf("pin: %d %v", code, r)
	}
	pins := a.state("")["pins"].([]any)
	eq(t, "pin next", pins[0].(map[string]any)["next"].(map[string]any)["id"], tV3)
	eq(t, "bob has no pins", len(b.state("")["pins"].([]any)), 0)
	if code, _ := a.do("POST", "/api/pin", map[string]any{"source": "PLnope"}); code != 400 {
		t.Errorf("pin unknown: %d", code)
	}
	for _, q := range []string{
		`INSERT INTO playlists (play_id, play_title) SELECT 'PLx' || g, 'x' FROM generate_series(1, 5) g`,
		`INSERT INTO user_playlists (user_id, play_id, uspl_pinned_at) SELECT $1::int, 'PLx' || g, now() FROM generate_series(1, 4) g`,
		`INSERT INTO user_playlists (user_id, play_id) VALUES ($1, 'PLx5')`,
	} {
		var args []any
		if strings.Contains(q, "$1") {
			args = []any{ana.ID}
		}
		if _, err := pool.Exec(ctx, q, args...); err != nil {
			t.Fatal(err)
		}
	}
	if code, r := a.do("POST", "/api/pin", map[string]any{"source": "PLx5"}); code != 400 {
		t.Errorf("sixth pin allowed: %d %v", code, r)
	}

	// Completing takes a source off Focus, frees its place and keeps the
	// date; moving it back to Focus clears the date.
	src := func(id string) map[string]any {
		for _, x := range a.state("")["sources"].([]any) {
			if m := x.(map[string]any); m["id"] == id {
				return m
			}
		}
		t.Fatalf("no source %s", id)
		return nil
	}
	if code, r := a.do("POST", "/api/complete", map[string]any{"source": "PLx1"}); code != 200 {
		t.Fatalf("complete: %d %v", code, r)
	}
	eq(t, "completed leaves focus", src("PLx1")["pinned"], false)
	if src("PLx1")["completed"] == nil {
		t.Error("completed has no date")
	}
	if code, r := a.do("POST", "/api/pin", map[string]any{"source": "PLx5"}); code != 200 {
		t.Errorf("place freed by completing: %d %v", code, r)
	}
	a.do("POST", "/api/complete", map[string]any{"source": "PLx5"})
	if code, r := a.do("POST", "/api/pin", map[string]any{"source": "PLx1"}); code != 200 {
		t.Fatalf("back to focus: %d %v", code, r)
	}
	eq(t, "back in focus", src("PLx1")["pinned"], true)
	eq(t, "no longer completed", src("PLx1")["completed"], nil)
	a.do("POST", "/api/complete", map[string]any{"source": "PLx5", "value": false})
	eq(t, "uncompleted goes to backlog", src("PLx5")["completed"], nil)
	eq(t, "and not to focus", src("PLx5")["pinned"], false)
	if code, _ := a.do("POST", "/api/complete", map[string]any{"source": "PLnope"}); code != 400 {
		t.Errorf("complete unknown: %d", code)
	}

	// Shelving keeps a video out of Everything only if nothing unshelved lists it.
	a.do("POST", "/api/shelve", map[string]any{"source": tPl})
	// tV2 is also in the channel; tV3 only in the shelved course (being in a
	// collection doesn't bring it back, as in the Python app).
	eq(t, "shelved", ids(a.state("")), []string{tV2})
	eq(t, "opening the shelved source shows it all", ids(a.state("source="+tPl)), []string{tV3, tV2})
	a.do("POST", "/api/collect/"+tV3, map[string]any{"collection": cid, "remove": true})
	a.do("POST", "/api/shelve", map[string]any{"source": tPl, "value": false})

	// Topics and Toggl rules.
	if code, r := a.do("POST", "/api/topic", map[string]any{"creator": tChan, "topic": "  Study   time "}); code != 200 {
		t.Fatalf("topic: %v", r)
	}
	eq(t, "topic", a.state("")["sources"].([]any)[0].(map[string]any)["topic"], "Study time")
	_, r = a.do("POST", "/api/toggl", map[string]any{"channel": "alpha", "project": "Learning", "tags": "Youtube, Focus, youtube"})
	ch := r["channels"].([]any)[0].(map[string]any)
	eq(t, "toggl rule", []any{ch["channel"], ch["project"], ch["tags"]}, []any{"Alpha", "Learning", "Youtube, Focus"})
	eq(t, "bob toggl", b.state("")["toggl"], map[string]any{})
	a.do("POST", "/api/toggl", map[string]any{"channel": "Alpha"})
	eq(t, "toggl cleared", a.state("")["toggl"], map[string]any{})

	// Forget drops the source for Ana only.
	a.do("POST", "/api/forget", map[string]any{"source": tPl})
	eq(t, "forgotten", ids(a.state("")), []string{tV2})
	eq(t, "bob keeps it", ids(b.state("")), []string{tV2, tV3})
	if code, _ := a.do("POST", "/api/forget", map[string]any{"source": tPl}); code != 404 {
		t.Errorf("forget twice: %d", code)
	}

	// Errors keep the old shapes.
	if code, _ := a.do("POST", "/api/star/short", nil); code != 400 {
		t.Errorf("bad id: %d", code)
	}
	if code, _ := a.do("POST", "/api/star/zzzzzzzzzzz", nil); code != 404 {
		t.Errorf("unknown video: %d", code)
	}
	if code, _ := a.do("POST", "/api/bogus/"+tV1, nil); code != 404 {
		t.Errorf("unknown action: %d", code)
	}
	if code, _ := a.do("GET", "/api/state?source=PLnope", nil); code != 404 {
		t.Errorf("unknown source view: %d", code)
	}
}

func jsonNum(v any) string {
	b, _ := json.Marshal(v)
	return string(b)
}
