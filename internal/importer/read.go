package importer

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	_ "modernc.org/sqlite" // registers the "sqlite" driver, pure Go
)

// The Python app's tables, read as they are. Nothing here is interpreted;
// plan.go decides what each row becomes.

type source struct {
	ID, Kind, URL               string
	Handle, Name, Owner, ChanID sql.NullString
	Section                     sql.NullString
	Shelved                     bool
	PinnedAt, AddedAt, LastSync sql.NullInt64
}

type video struct {
	ID, Title               string
	Description, UploadDate sql.NullString
	Duration                sql.NullInt64
	DateApprox              bool
	FirstSeen               int64
	DownloadedPath          sql.NullString
	DownloadedAt, Filesize  sql.NullInt64
	Starred, Watched        bool
	WatchedAt               sql.NullInt64
	Progress                float64
	Unavailable             bool
	LeftSource              sql.NullString
	Hidden                  int
	Chapters                sql.NullString
}

type membership struct {
	SourceID, VideoID string
	Position          int
}

type collection struct {
	ID        int64
	Name      string
	CreatedAt int64
}

type collectionItem struct {
	CollectionID int64
	VideoID      string
	Rank         int
	AddedAt      int64
}

type viewPref struct{ Scope, Sort string }

type togglMap struct {
	Channel             string
	Name, Project, Tags sql.NullString
	SetAt               int64
}

type creatorTopic struct{ Creator, Topic string }

type mark struct {
	VideoID   string
	T         float64
	Note      string
	CreatedAt int64
}

type snapshot struct {
	sources     []source
	videos      []video
	memberships []membership
	collections []collection
	items       []collectionItem
	prefs       []viewPref
	toggl       []togglMap
	topics      []creatorTopic
	marks       []mark
}

func readSQLite(ctx context.Context, path string) (*snapshot, error) {
	db, err := sql.Open("sqlite", "file:"+path+"?mode=ro")
	if err != nil {
		return nil, err
	}
	defer db.Close()
	if err := db.PingContext(ctx); err != nil {
		return nil, fmt.Errorf("opening %s: %w", path, err)
	}

	s := &snapshot{}
	steps := []struct {
		query string
		scan  func(*sql.Rows) error
	}{
		{`SELECT id, kind, url, handle, name, owner, channel_id, section, shelved, pinned_at, added_at, last_sync FROM sources`,
			func(r *sql.Rows) error {
				var x source
				err := r.Scan(&x.ID, &x.Kind, &x.URL, &x.Handle, &x.Name, &x.Owner, &x.ChanID, &x.Section,
					&x.Shelved, &x.PinnedAt, &x.AddedAt, &x.LastSync)
				s.sources = append(s.sources, x)
				return err
			}},
		{`SELECT id, title, description, upload_date, duration, date_approx, first_seen, downloaded_path,
		         downloaded_at, filesize, starred, watched, watched_at, progress, unavailable, left_source,
		         hidden, chapters FROM videos`,
			func(r *sql.Rows) error {
				var x video
				err := r.Scan(&x.ID, &x.Title, &x.Description, &x.UploadDate, &x.Duration, &x.DateApprox,
					&x.FirstSeen, &x.DownloadedPath, &x.DownloadedAt, &x.Filesize, &x.Starred, &x.Watched,
					&x.WatchedAt, &x.Progress, &x.Unavailable, &x.LeftSource, &x.Hidden, &x.Chapters)
				s.videos = append(s.videos, x)
				return err
			}},
		{`SELECT source_id, video_id, position FROM source_videos`,
			func(r *sql.Rows) error {
				var x membership
				err := r.Scan(&x.SourceID, &x.VideoID, &x.Position)
				s.memberships = append(s.memberships, x)
				return err
			}},
		{`SELECT id, name, created_at FROM collections`,
			func(r *sql.Rows) error {
				var x collection
				err := r.Scan(&x.ID, &x.Name, &x.CreatedAt)
				s.collections = append(s.collections, x)
				return err
			}},
		{`SELECT collection_id, video_id, rank, added_at FROM collection_items`,
			func(r *sql.Rows) error {
				var x collectionItem
				err := r.Scan(&x.CollectionID, &x.VideoID, &x.Rank, &x.AddedAt)
				s.items = append(s.items, x)
				return err
			}},
		{`SELECT scope, sort FROM view_prefs`,
			func(r *sql.Rows) error {
				var x viewPref
				err := r.Scan(&x.Scope, &x.Sort)
				s.prefs = append(s.prefs, x)
				return err
			}},
		{`SELECT channel, name, project, tags, set_at FROM toggl_map`,
			func(r *sql.Rows) error {
				var x togglMap
				err := r.Scan(&x.Channel, &x.Name, &x.Project, &x.Tags, &x.SetAt)
				s.toggl = append(s.toggl, x)
				return err
			}},
		{`SELECT creator, topic FROM creator_topics`,
			func(r *sql.Rows) error {
				var x creatorTopic
				err := r.Scan(&x.Creator, &x.Topic)
				s.topics = append(s.topics, x)
				return err
			}},
		{`SELECT video_id, t, note, created_at FROM marks`,
			func(r *sql.Rows) error {
				var x mark
				err := r.Scan(&x.VideoID, &x.T, &x.Note, &x.CreatedAt)
				s.marks = append(s.marks, x)
				return err
			}},
	}
	for _, st := range steps {
		rows, err := db.QueryContext(ctx, st.query)
		if err != nil {
			return nil, fmt.Errorf("reading %s: %w", path, err)
		}
		for rows.Next() {
			if err := st.scan(rows); err != nil {
				rows.Close()
				return nil, err
			}
		}
		if err := rows.Close(); err != nil {
			return nil, err
		}
		if err := rows.Err(); err != nil {
			return nil, err
		}
	}
	return s, nil
}

func epoch(n int64) time.Time { return time.Unix(n, 0).UTC() }

func nullEpoch(n sql.NullInt64) *time.Time {
	if !n.Valid {
		return nil
	}
	t := epoch(n.Int64)
	return &t
}
