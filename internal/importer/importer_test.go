package importer

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Jairanpo/yt/internal/db"
	"github.com/Jairanpo/yt/internal/ytdlp"
)

// The Python app's schema, trimmed to the columns the importer reads.
const pythonSchema = `
CREATE TABLE sources (id TEXT PRIMARY KEY, kind TEXT NOT NULL, handle TEXT, name TEXT, owner TEXT,
  channel_id TEXT, section TEXT, shelved INTEGER NOT NULL DEFAULT 0, pinned_at INTEGER,
  url TEXT NOT NULL, added_at INTEGER NOT NULL, last_sync INTEGER);
CREATE TABLE videos (id TEXT PRIMARY KEY, title TEXT NOT NULL, description TEXT, duration INTEGER,
  upload_date TEXT, date_approx INTEGER NOT NULL DEFAULT 0, first_seen INTEGER NOT NULL,
  downloaded_path TEXT, downloaded_at INTEGER, filesize INTEGER, starred INTEGER NOT NULL DEFAULT 0,
  watched INTEGER NOT NULL DEFAULT 0, watched_at INTEGER, progress REAL NOT NULL DEFAULT 0,
  unavailable INTEGER NOT NULL DEFAULT 0, left_source TEXT, hidden INTEGER NOT NULL DEFAULT 0, chapters TEXT);
CREATE TABLE source_videos (source_id TEXT, video_id TEXT, position INTEGER NOT NULL);
CREATE TABLE collections (id INTEGER PRIMARY KEY, name TEXT NOT NULL, created_at INTEGER NOT NULL);
CREATE TABLE collection_items (collection_id INTEGER, video_id TEXT, rank INTEGER NOT NULL, added_at INTEGER NOT NULL);
CREATE TABLE view_prefs (scope TEXT PRIMARY KEY, sort TEXT NOT NULL, set_at INTEGER NOT NULL);
CREATE TABLE toggl_map (channel TEXT PRIMARY KEY, name TEXT, project TEXT, tags TEXT, set_at INTEGER NOT NULL);
CREATE TABLE creator_topics (creator TEXT PRIMARY KEY, topic TEXT NOT NULL, set_at INTEGER NOT NULL);
CREATE TABLE marks (id INTEGER PRIMARY KEY, video_id TEXT NOT NULL, t REAL NOT NULL, note TEXT NOT NULL DEFAULT '', created_at INTEGER NOT NULL);
`

const (
	chanA = "UCaaaaaaaaaaaaaaaaaaaaaa" // tracked channel
	chanB = "UCbbbbbbbbbbbbbbbbbbbbbb" // owner of a playlist, never tracked
	chanC = "UCcccccccccccccccccccccc" // only known through a topic
)

const fixture = `
INSERT INTO sources VALUES
  ('UCaaaaaaaaaaaaaaaaaaaaaa','channel','@alpha','Alpha',NULL,'UCaaaaaaaaaaaaaaaaaaaaaa',NULL,0,NULL,'u',1700000000,1700000500),
  ('PLcourse','playlist','@alpha','Alpha Course','Alpha','UCaaaaaaaaaaaaaaaaaaaaaa','course',0,1700000900,'u',1700000100,NULL),
  ('PLmystery','playlist','@beta','Beta Mix','Beta Person',NULL,NULL,1,NULL,'u',1700000200,NULL),
  ('PICKS:UCaaaaaaaaaaaaaaaaaaaaaa','playlist','@alpha','Picked videos','Alpha','UCaaaaaaaaaaaaaaaaaaaaaa','picks',0,NULL,'u',1700000300,NULL);
INSERT INTO videos (id,title,upload_date,date_approx,first_seen,starred,watched,watched_at,progress,unavailable,hidden,chapters,downloaded_path,downloaded_at,filesize) VALUES
  ('aaaaaaaaaa1','One','2024-01-02',0,1700000000,1,1,1700001000,12.5,0,0,'[{"start":0,"end":10,"title":"Intro"},{"start":10,"end":null,"title":"Rest"}]','/x/one.mp4',1700002000,1234),
  ('aaaaaaaaaa2','Two','2024-02-01',1,1700000000,0,0,1700001000,0,0,1,'[]',NULL,NULL,NULL),
  ('bbbbbbbbbb1','Gone','',0,1700000000,0,0,NULL,0,1,2,NULL,NULL,NULL,NULL);
INSERT INTO source_videos VALUES
  ('UCaaaaaaaaaaaaaaaaaaaaaa','aaaaaaaaaa1',0),('UCaaaaaaaaaaaaaaaaaaaaaa','aaaaaaaaaa2',1),
  ('PLcourse','aaaaaaaaaa2',0),('PLcourse','aaaaaaaaaa1',1),
  ('PLmystery','bbbbbbbbbb1',0),
  ('PICKS:UCaaaaaaaaaaaaaaaaaaaaaa','aaaaaaaaaa1',0);
INSERT INTO collections VALUES (1,'Later',1700000000);
INSERT INTO collection_items VALUES (1,'aaaaaaaaaa2',1,1700000000);
INSERT INTO view_prefs VALUES ('global','newest',0),('source:PLcourse','oldest',0),('collection:1','title',0);
INSERT INTO toggl_map VALUES
  ('Alpha','Alpha Studies','Learning','Youtube, Focus, youtube',1700000000),
  ('Beta Person','','Fun','Fun',1700000000),
  ('Nobody','','','x',1700000000);
INSERT INTO creator_topics VALUES ('UCaaaaaaaaaaaaaaaaaaaaaa','Study',0),('name:beta person','Fun',0),
  ('UCcccccccccccccccccccccc','Study',0);
INSERT INTO marks VALUES (1,'aaaaaaaaaa1',5,'',1700000000);
`

type fakeResolver struct{}

func (fakeResolver) PlaylistOwner(_ context.Context, id string) (ytdlp.Owner, error) {
	if id == "PLmystery" {
		return ytdlp.Owner{ChannelID: chanB, Name: "Beta Person", Handle: "@beta"}, nil
	}
	return ytdlp.Owner{}, errors.New("unknown playlist")
}

func (fakeResolver) Channel(_ context.Context, id string) (ytdlp.Owner, error) {
	if id == chanC {
		return ytdlp.Owner{ChannelID: chanC, Name: "Gamma"}, nil
	}
	return ytdlp.Owner{}, errors.New("unknown channel")
}

func writeFixture(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "catalog.db")
	s, err := sql.Open("sqlite", "file:"+path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	for _, q := range []string{pythonSchema, fixture} {
		if _, err := s.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	return path
}

func TestPlanOffline(t *testing.T) {
	snap, err := readSQLite(context.Background(), writeFixture(t))
	if err != nil {
		t.Fatal(err)
	}
	p, err := buildPlan(context.Background(), snap, nil, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if len(p.playlists) != 2 || len(p.picks) != 1 || len(p.userChans) != 1 {
		t.Fatalf("playlists %d picks %d channels %d", len(p.playlists), len(p.picks), len(p.userChans))
	}
	for _, pl := range p.playlists {
		if pl.ID == "PLmystery" && pl.ChanID != nil {
			t.Error("offline, the mystery playlist's channel should stay unknown")
		}
	}
	if p.defaultSort != "NEWEST" {
		t.Errorf("default sort %q", p.defaultSort)
	}
	if len(p.warnings) == 0 {
		t.Error("offline gaps should be reported")
	}
}

func TestImportAgainstPostgres(t *testing.T) {
	url := os.Getenv("YT_TEST_DATABASE_URL")
	if url == "" {
		t.Skip("YT_TEST_DATABASE_URL not set")
	}
	ctx := context.Background()
	pool := migrated(t, ctx, url)
	path := writeFixture(t)

	opts := Options{SQLitePath: path, UserName: "ana", DeviceName: "desk", Resolver: fakeResolver{}}
	for run := 1; run <= 2; run++ { // the second run must change nothing
		rep, err := Run(ctx, pool, opts)
		if err != nil {
			t.Fatalf("run %d: %v", run, err)
		}
		if len(rep.Warnings) != 1 { // only "Nobody" has no channel
			t.Fatalf("run %d warnings: %v", run, rep.Warnings)
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
	checks := []struct {
		name string
		q    string
		want any
	}{
		{"one user, an admin", "SELECT count(*) FILTER (WHERE usro_code = 'ADMIN') FROM users", int64(1)},
		{"user default sort", "SELECT sort_code FROM users", "NEWEST"},
		{"channels incl. resolved", "SELECT count(*) FROM channels", int64(3)},
		{"mystery playlist owner", "SELECT chan_id FROM playlists WHERE play_id = 'PLmystery'", chanB},
		{"course section", "SELECT plse_code FROM playlists WHERE play_id = 'PLcourse'", "COURSE"},
		{"picks are not playlists", "SELECT count(*) FROM playlists WHERE play_id LIKE 'PICKS%'", int64(0)},
		{"picks list", "SELECT count(*) FROM user_pick_videos", int64(1)},
		{"uploader from channel", "SELECT chan_id FROM videos WHERE vid_id = 'aaaaaaaaaa2'", chanA},
		{"channel position", "SELECT vid_channel_position FROM videos WHERE vid_id = 'aaaaaaaaaa2'", int32(1)},
		{"month-only date", "SELECT vid_published_month_only FROM videos WHERE vid_id = 'aaaaaaaaaa2'", true},
		{"no date stays NULL", "SELECT vid_published_on IS NULL FROM videos WHERE vid_id = 'bbbbbbbbbb1'", true},
		{"unavailable is catalog-wide", "SELECT vid_unavailable_since IS NOT NULL FROM videos WHERE vid_id = 'bbbbbbbbbb1'", true},
		{"chapters", "SELECT count(*) FROM video_chapters", int64(2)},
		{"open-ended chapter", "SELECT vich_end_seconds = vich_start_seconds FROM video_chapters WHERE vich_title = 'Rest'", true},
		{"no-chapters is remembered", "SELECT vid_chapters_checked_at IS NOT NULL FROM videos WHERE vid_id = 'aaaaaaaaaa2'", true},
		{"playlist order", "SELECT vid_id FROM playlist_videos WHERE play_id = 'PLcourse' ORDER BY plvi_position LIMIT 1", "aaaaaaaaaa2"},
		{"pinned course", "SELECT uspl_pinned_at IS NOT NULL FROM user_playlists WHERE play_id = 'PLcourse'", true},
		{"shelved playlist", "SELECT uspl_shelved FROM user_playlists WHERE play_id = 'PLmystery'", true},
		{"per-view sort", "SELECT sort_code FROM user_playlists WHERE play_id = 'PLcourse'", "OLDEST"},
		{"user video rows", "SELECT count(*) FROM user_videos", int64(2)},
		{"watched_at dropped when unwatched", "SELECT usvi_watched_at IS NULL FROM user_videos WHERE vid_id = 'aaaaaaaaaa2'", true},
		{"hidden by you", "SELECT usvi_hidden FROM user_videos WHERE vid_id = 'aaaaaaaaaa2'", true},
		{"collection sort", "SELECT sort_code FROM collections", "TITLE"},
		{"empty note is NULL", "SELECT mark_note IS NULL FROM marks", true},
		{"topics", "SELECT count(*) FROM channel_topics", int64(3)},
		{"topic by name", "SELECT topi_name FROM channel_topics JOIN topics USING (user_id, topi_id) WHERE chan_id = $1", "Fun"},
		{"rules", "SELECT count(*) FROM toggl_channel_rules", int64(2)},
		{"blank entry name is NULL", "SELECT tcru_entry_name IS NULL FROM toggl_channel_rules WHERE chan_id = $1", true},
		{"tags deduped ignoring case, first spelling kept",
			"SELECT string_agg(tag_name, ',' ORDER BY tag_name) FROM tags", "Focus,Fun,Youtube"},
		{"downloads on the device", "SELECT count(*) FROM device_videos JOIN devices USING (devi_id) WHERE devi_name = 'desk'", int64(1)},
	}
	for _, c := range checks {
		var args []any
		if c.name == "topic by name" || c.name == "blank entry name is NULL" {
			args = []any{chanB}
		}
		if got := one(c.q, args...); got != c.want {
			t.Errorf("%s: got %v (%T), want %v", c.name, got, got, c.want)
		}
	}
}

func migrated(t *testing.T, ctx context.Context, url string) *pgxpool.Pool {
	t.Helper()
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
	// Start from nothing, end with nothing.
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
