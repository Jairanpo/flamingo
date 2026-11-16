package catalog

import (
	"context"
	"os"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Jairanpo/flamingo/internal/db"
	"github.com/Jairanpo/flamingo/internal/ytdlp"
)

const (
	chA = "UCaaaaaaaaaaaaaaaaaaaaaa"
	chB = "UCbbbbbbbbbbbbbbbbbbbbbb"
)

// fake answers List from a table of URL -> listing.
type fake map[string]struct {
	meta    ytdlp.Meta
	entries []ytdlp.Entry
}

func (f fake) List(_ context.Context, url string, limit int) (ytdlp.Meta, []ytdlp.Entry, error) {
	l, ok := f[url]
	if !ok {
		return ytdlp.Meta{}, nil, ytdlp.ErrNothing
	}
	e := l.entries
	if limit > 0 && limit < len(e) {
		e = e[:limit]
	}
	return l.meta, e, nil
}

func (f fake) Browse(context.Context, string) (string, []ytdlp.BrowseItem, error) {
	return "", nil, ytdlp.ErrNothing
}

func (f fake) Chapters(context.Context, string) ([]ytdlp.Chapter, error) {
	return nil, ytdlp.ErrNothing
}

func vids(titles ...string) []ytdlp.Entry {
	var out []ytdlp.Entry
	for i, t := range titles {
		out = append(out, ytdlp.Entry{ID: strings.Repeat(t[:1], 10) + t[1:2], Title: t,
			Published: "2024-01-0" + string(rune('1'+i%9)), MonthOnly: true, Position: i})
	}
	return out
}

func testPool(t *testing.T) *pgxpool.Pool {
	url := os.Getenv("FLAMINGO_TEST_DATABASE_URL")
	if url == "" {
		t.Skip("FLAMINGO_TEST_DATABASE_URL not set")
	}
	ctx := context.Background()
	pool, err := db.Open(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	mig, err := db.NewMigrator(pool)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { mig.Close() })
	reset := func() {
		for {
			if _, err := mig.Down(ctx); err != nil {
				return
			}
		}
	}
	reset()
	if _, err := mig.Up(ctx); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(reset)
	return pool
}

func TestAddAndSync(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	var ana, bob int32
	for _, n := range []string{"ana", "bob"} {
		var id int32
		if err := pool.QueryRow(ctx, `INSERT INTO users (user_name, user_display_name) VALUES ($1, $1) RETURNING user_id`, n).Scan(&id); err != nil {
			t.Fatal(err)
		}
		if n == "ana" {
			ana = id
		} else {
			bob = id
		}
	}
	one := func(q string, args ...any) any {
		t.Helper()
		var v any
		if err := pool.QueryRow(ctx, q, args...).Scan(&v); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
		return v
	}
	eq := func(what string, got, want any) {
		t.Helper()
		if got != want {
			t.Errorf("%s: got %v (%T), want %v (%T)", what, got, got, want, want)
		}
	}

	chURL := ytdlp.ChannelURL(chA)
	f := fake{}
	f[chURL] = struct {
		meta    ytdlp.Meta
		entries []ytdlp.Entry
	}{ytdlp.Meta{Kind: "channel", ID: chA, Name: "Alpha", Handle: "@alpha", ChannelID: chA}, vids("c3", "c2", "c1")}
	f["https://www.youtube.com/@alpha/videos"] = f[chURL]
	cat := &Catalog{Pool: pool, YT: f}

	res, err := cat.Add(ctx, ana, "@alpha", "", nil)
	if err != nil {
		t.Fatal(err)
	}
	eq("added", res, Result{SourceID: chA, Kind: "channel", Name: "Alpha", New: 3, Total: 3})
	eq("tracked", one(`SELECT count(*) FROM user_channels WHERE user_id = $1`, ana), int64(1))
	eq("position", one(`SELECT vid_channel_position FROM videos WHERE vid_id = 'cccccccccc1'`), int32(2))

	// Two new uploads; a short sync sees only the top three and pushes the
	// rest down by two.
	f[chURL] = struct {
		meta    ytdlp.Meta
		entries []ytdlp.Entry
	}{f[chURL].meta, vids("c5", "c4", "c3", "c2", "c1")}
	targets, _, err := cat.Targets(ctx, SyncOptions{})
	if err != nil || len(targets) != 1 {
		t.Fatalf("targets: %v %v", targets, err)
	}
	res, err = cat.Sync(ctx, targets[0], 3)
	if err != nil {
		t.Fatal(err)
	}
	eq("short sync new", res.New, 2)
	eq("pushed down", one(`SELECT vid_channel_position FROM videos WHERE vid_id = 'cccccccccc1'`), int32(4))

	// A full sync after c2 was removed: it leaves the channel's listing but
	// stays in the catalog.
	f[chURL] = struct {
		meta    ytdlp.Meta
		entries []ytdlp.Entry
	}{f[chURL].meta, vids("c5", "c4", "c3", "c1")}
	if _, err := cat.Sync(ctx, targets[0], 0); err != nil {
		t.Fatal(err)
	}
	eq("left channel", one(`SELECT vid_channel_position IS NULL FROM videos WHERE vid_id = 'cccccccccc2'`), true)
	eq("c1 position", one(`SELECT vid_channel_position FROM videos WHERE vid_id = 'cccccccccc1'`), int32(3))

	// An exact date is never replaced by a rough one; a video gone private
	// keeps its title and is marked.
	if _, err := pool.Exec(ctx, `UPDATE videos SET vid_published_on = '2023-05-06', vid_published_month_only = false WHERE vid_id = 'cccccccccc5'`); err != nil {
		t.Fatal(err)
	}
	gone := vids("c5", "c4", "c3", "c1")
	gone[1].Title = ""
	f[chURL] = struct {
		meta    ytdlp.Meta
		entries []ytdlp.Entry
	}{f[chURL].meta, gone}
	if _, err := cat.Sync(ctx, targets[0], 0); err != nil {
		t.Fatal(err)
	}
	eq("exact date kept", one(`SELECT to_char(vid_published_on, 'YYYY-MM-DD') FROM videos WHERE vid_id = 'cccccccccc5'`), "2023-05-06")
	eq("title kept", one(`SELECT vid_title FROM videos WHERE vid_id = 'cccccccccc4'`), "c4")
	eq("unavailable", one(`SELECT vid_unavailable_since IS NOT NULL FROM videos WHERE vid_id = 'cccccccccc4'`), true)
	f[chURL] = struct {
		meta    ytdlp.Meta
		entries []ytdlp.Entry
	}{f[chURL].meta, vids("c5", "c4", "c3", "c1")}
	if _, err := cat.Sync(ctx, targets[0], 0); err != nil {
		t.Fatal(err)
	}
	eq("back again", one(`SELECT vid_unavailable_since IS NULL FROM videos WHERE vid_id = 'cccccccccc4'`), true)

	// A course from Browse, owned by another channel, holding one of
	// Alpha's videos and one of Beta's.
	plURL := ytdlp.PlaylistURL("PLcourse1")
	pl := vids("p1", "c3")
	pl[0].ChannelID, pl[0].ChannelName = chB, "Beta"
	f[plURL] = struct {
		meta    ytdlp.Meta
		entries []ytdlp.Entry
	}{ytdlp.Meta{Kind: "playlist", ID: "PLcourse1", Name: "Course", Owner: "Beta", ChannelID: chB}, pl}
	res, err = cat.Add(ctx, bob, "PLcourse1", "course", nil)
	if err != nil {
		t.Fatal(err)
	}
	eq("course new", res.New, 2)
	eq("section", one(`SELECT plse_code FROM playlists WHERE play_id = 'PLcourse1'`), "COURSE")
	eq("uploader filled", one(`SELECT chan_id FROM videos WHERE vid_id = 'pppppppppp1'`), chB)
	eq("c3 still alpha's", one(`SELECT chan_id FROM videos WHERE vid_id = 'cccccccccc3'`), chA)
	// A plain sync keeps the shelf Browse set, and a removal drops the slot.
	f[plURL] = struct {
		meta    ytdlp.Meta
		entries []ytdlp.Entry
	}{f[plURL].meta, pl[:1]}
	targets, _, _ = cat.Targets(ctx, SyncOptions{Source: "PLcourse1"})
	if _, err := cat.Sync(ctx, targets[0], 0); err != nil {
		t.Fatal(err)
	}
	eq("still a course", one(`SELECT plse_code FROM playlists WHERE play_id = 'PLcourse1'`), "COURSE")
	eq("slot dropped", one(`SELECT count(*) FROM playlist_videos WHERE play_id = 'PLcourse1'`), int64(1))

	// Picks: two videos picked one at a time land in one list, in order.
	for i, v := range []string{"aaaaaaaaaa1", "aaaaaaaaaa2"} {
		f[ytdlp.PicksURL+v] = struct {
			meta    ytdlp.Meta
			entries []ytdlp.Entry
		}{ytdlp.Meta{Kind: "picks", ID: chA, Name: "Picked videos", Owner: "Alpha", ChannelID: chA, Section: "picks"},
			[]ytdlp.Entry{{ID: v, Title: "pick " + v, ChannelID: chA, Published: "2024-03-0" + string(rune('1'+i))}}}
		res, err = cat.Add(ctx, bob, "https://youtu.be/"+v, "", nil)
		if err != nil {
			t.Fatal(err)
		}
	}
	eq("picks result", res, Result{SourceID: "PICKS:" + chA, Kind: "playlist", Name: "pick aaaaaaaaaa2", New: 1, Total: 2})
	eq("pick order", one(`SELECT upvi_position FROM user_pick_videos WHERE vid_id = 'aaaaaaaaaa2'`), int32(1))
	eq("exact pick date", one(`SELECT vid_published_month_only FROM videos WHERE vid_id = 'aaaaaaaaaa2'`), false)

	// Shelved by everyone: skipped unless asked.
	if _, err := pool.Exec(ctx, `UPDATE user_channels SET usch_shelved = true`); err != nil {
		t.Fatal(err)
	}
	targets, skipped, err := cat.Targets(ctx, SyncOptions{})
	if err != nil {
		t.Fatal(err)
	}
	eq("skipped", skipped, 1)
	eq("left", len(targets), 2) // the course and bob's picks
	// Adding it again takes it off the shelf.
	if _, err := cat.Add(ctx, ana, "@alpha", "", nil); err != nil {
		t.Fatal(err)
	}
	eq("unshelved", one(`SELECT usch_shelved FROM user_channels WHERE user_id = $1`, ana), false)
}
