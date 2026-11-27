package server

import (
	"context"
	"testing"
	"time"

	"github.com/Jairanpo/flamingo/internal/ytdlp"
)

type fakeYT struct{}

func (fakeYT) List(_ context.Context, url string, _ int) (ytdlp.Meta, []ytdlp.Entry, error) {
	if url != "https://www.youtube.com/@alpha/videos" {
		return ytdlp.Meta{}, nil, ytdlp.ErrNothing
	}
	return ytdlp.Meta{Kind: "channel", ID: tChan, Name: "Alpha", ChannelID: tChan},
		[]ytdlp.Entry{{ID: tV2, Title: "Two", Position: 0}, {ID: tV1, Title: "One", Position: 1}}, nil
}

func (fakeYT) Browse(_ context.Context, ref string) (string, []ytdlp.BrowseItem, error) {
	return "Alpha", []ytdlp.BrowseItem{
		{ID: tPl, Section: "course", Title: "Alpha Course"},
		{ID: "PLother", Section: "playlist", Title: "Other"},
	}, nil
}

// Details: v1 has two chapters and a description with a link, v2 has
// neither, anything else is YouTube failing.
func (fakeYT) Details(_ context.Context, vid string) (ytdlp.Details, error) {
	switch vid {
	case tV1:
		return ytdlp.Details{
			Chapters:    []ytdlp.Chapter{{Start: 0, End: 30, Title: "Intro"}, {Start: 30, End: 60, Title: "Rest"}},
			Description: "Slides: https://example.com/slides",
		}, nil
	case tV2:
		return ytdlp.Details{}, nil
	}
	return ytdlp.Details{}, ytdlp.ErrNothing
}

func TestSourcesAPI(t *testing.T) {
	ts, pool, s := testServer(t)
	ctx := context.Background()
	s.cat.YT = fakeYT{}
	ana, _ := s.auth.CreateUser(ctx, "ana", "", "secret-pass", false)
	s.auth.CreateUser(ctx, "bob", "", "secret-pass", false)
	a, b := signIn(t, ts.URL, "ana"), signIn(t, ts.URL, "bob")

	// Bad input never reaches yt-dlp.
	for _, bad := range []map[string]any{
		{"ref": "https://evil.example/@x"}, {"ref": ""}, {"refs": "x"}, {"refs": []any{}},
		{"ref": "https://www.youtube.com/@alpha/playlists"},
	} {
		if code, r := a.do("POST", "/api/sources", bad); code != 400 {
			t.Errorf("%v: %d %v", bad, code, r)
		}
	}

	code, r := a.do("POST", "/api/sources", map[string]any{"ref": "@alpha"})
	if code != 200 || r["job"].(map[string]any)["id"] != "src:@alpha" {
		t.Fatalf("add: %d %v", code, r)
	}
	var job map[string]any
	for deadline := time.Now().Add(10 * time.Second); time.Now().Before(deadline); time.Sleep(20 * time.Millisecond) {
		_, r = a.do("GET", "/api/jobs", nil)
		job = r["jobs"].([]any)[0].(map[string]any)
		if job["state"] == "done" || job["state"] == "error" {
			break
		}
	}
	eq(t, "job", []any{job["state"], job["title"], job["detail"], job["result"].(map[string]any)["source_id"]},
		[]any{"done", "Alpha", "2 catalogued · 2 new", tChan})
	eq(t, "in ana's library", ids(a.state("source="+tChan)), []string{tV2, tV1})
	_, r = b.do("GET", "/api/jobs", nil)
	eq(t, "bob sees no cards", len(r["jobs"].([]any)), 0)
	eq(t, "bob's library", len(b.state("")["videos"].([]any)), 0)

	// A failure is a card too.
	a.do("POST", "/api/sources", map[string]any{"refs": []any{map[string]any{"ref": "@nobody", "section": "course"}}})
	for deadline := time.Now().Add(10 * time.Second); time.Now().Before(deadline); time.Sleep(20 * time.Millisecond) {
		_, r = a.do("GET", "/api/jobs", nil)
		if len(r["jobs"].([]any)) == 2 && r["jobs"].([]any)[1].(map[string]any)["state"] == "error" {
			break
		}
	}
	eq(t, "failed card", r["jobs"].([]any)[1].(map[string]any)["detail"], "failed")
	_, r = a.do("POST", "/api/jobs/clear", map[string]any{"id": "src:@alpha"})
	eq(t, "cleared one", r["ok"], true)
	a.do("POST", "/api/jobs/clear", nil)
	_, r = a.do("GET", "/api/jobs", nil)
	eq(t, "cleared all", len(r["jobs"].([]any)), 0)
	if code, _ := a.do("POST", "/api/jobs/clear", map[string]any{"id": "nope"}); code != 400 {
		t.Errorf("bad job id: %d", code)
	}

	// Browse marks known courses and says what this user tracks.
	if _, err := pool.Exec(ctx, `INSERT INTO playlists (play_id, play_title) VALUES ($1, 'x')`, tPl); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO user_playlists (user_id, play_id) VALUES ($1, $2)`, ana.ID, tPl); err != nil {
		t.Fatal(err)
	}
	code, r = a.do("POST", "/api/sources/browse", map[string]any{"creator": "@alpha"})
	if code != 200 {
		t.Fatalf("browse: %d %v", code, r)
	}
	pls := r["playlists"].([]any)
	eq(t, "browse", []any{r["creator"], pls[0].(map[string]any)["tracked"], pls[1].(map[string]any)["tracked"]},
		[]any{"Alpha", true, false})
	var section string
	pool.QueryRow(ctx, `SELECT plse_code FROM playlists WHERE play_id = $1`, tPl).Scan(&section)
	eq(t, "marked course", section, "COURSE")
	_, r = b.do("POST", "/api/sources/browse", map[string]any{"creator": "@alpha"})
	eq(t, "bob tracks none", r["playlists"].([]any)[0].(map[string]any)["tracked"], false)
	if code, _ := a.do("POST", "/api/sources/browse", map[string]any{"creator": "PLabc"}); code != 400 {
		t.Errorf("a playlist is not a creator: %d", code)
	}
	// Refresh: only what you track, and it lands on your cards.
	if code, _ := b.do("POST", "/api/sources/refresh", map[string]any{"source": tPl}); code != 404 {
		t.Errorf("bob refreshed ana's playlist: %d", code)
	}
	if code, _ := a.do("POST", "/api/sources/refresh", map[string]any{}); code != 400 {
		t.Errorf("refresh without a source: %d", code)
	}
	code, r = a.do("POST", "/api/sources/refresh", map[string]any{"source": tPl})
	if code != 200 {
		t.Fatalf("refresh: %d %v", code, r)
	}
	eq(t, "refresh card", r["job"].(map[string]any)["kind"], "refresh")
}
