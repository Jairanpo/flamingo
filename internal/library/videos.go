package library

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
)

// Video is one card, in the JSON shape the page has always read.
type Video struct {
	ID          string  `json:"id"`
	Title       string  `json:"title"`
	Channel     string  `json:"channel"`
	Date        *string `json:"date"`
	DateApprox  bool    `json:"date_approx"`
	Duration    *int32  `json:"duration"`
	Description string  `json:"description"`
	Have        bool    `json:"have"`
	Audio       bool    `json:"audio"`
	Size        *int64  `json:"size"`
	Starred     bool    `json:"starred"`
	Watched     bool    `json:"watched"`
	Hidden      bool    `json:"hidden"`
	Unavailable bool    `json:"unavailable"`
	Progress    float64 `json:"progress"`
	Marks       int     `json:"marks"`
}

// videoCols reads one card. $1 is the user, $2 the device.
const videoCols = `
	v.vid_id, v.vid_title,
	COALESCE(vc.chan_name,
	         (SELECT pc.chan_name FROM playlist_videos pv
	            JOIN playlists p ON p.play_id = pv.play_id
	            JOIN channels pc ON pc.chan_id = p.chan_id
	           WHERE pv.vid_id = v.vid_id ORDER BY p.play_title LIMIT 1),
	         'unknown'),
	to_char(v.vid_published_on, 'YYYY-MM-DD'), v.vid_published_month_only, v.vid_duration_seconds,
	COALESCE(v.vid_description, ''),
	dv.vid_id IS NOT NULL, COALESCE(dv.dvvi_audio_only, false), dv.dvvi_size_bytes,
	COALESCE(uv.usvi_starred, false), COALESCE(uv.usvi_watched, false),
	COALESCE(uv.usvi_hidden, false) OR v.vid_unavailable_since IS NOT NULL,
	v.vid_unavailable_since IS NOT NULL,
	COALESCE(uv.usvi_progress_seconds, 0)::float8,
	(SELECT count(*) FROM marks m WHERE m.user_id = $1 AND m.vid_id = v.vid_id)::int`

const videoJoins = `
	FROM videos v
	LEFT JOIN channels vc ON vc.chan_id = v.chan_id
	LEFT JOIN user_videos uv ON uv.user_id = $1 AND uv.vid_id = v.vid_id
	LEFT JOIN device_videos dv ON dv.devi_id = $2 AND dv.vid_id = v.vid_id`

func scanVideo(row pgx.Row) (*Video, error) {
	var v Video
	err := row.Scan(&v.ID, &v.Title, &v.Channel, &v.Date, &v.DateApprox, &v.Duration, &v.Description,
		&v.Have, &v.Audio, &v.Size, &v.Starred, &v.Watched, &v.Hidden, &v.Unavailable, &v.Progress, &v.Marks)
	if err != nil {
		return nil, err
	}
	if utf8.RuneCountInString(v.Description) > 4000 {
		v.Description = string([]rune(v.Description)[:4000])
	}
	return &v, nil
}

// Video returns one card, or ErrUnknownVideo when the catalog lacks it.
func (l *Lib) Video(ctx context.Context, id string) (*Video, error) {
	v, err := scanVideo(l.Pool.QueryRow(ctx, `SELECT `+videoCols+videoJoins+` WHERE v.vid_id = $3`,
		l.User, l.Device, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrUnknownVideo
	}
	return v, err
}

// Query picks the cards for one view.
type Query struct {
	Source     string
	Collection int32
	Q          string
	Have       *bool
	Starred    bool
	Unwatched  bool
	Hidden     bool
	Shelved    bool // include videos only shelved sources carry (Everything only)
	Sort       string
	Limit      int
	Offset     int
}

var explicitOrder = map[string]string{
	"newest":   "(v.vid_published_on IS NULL), v.vid_published_on DESC",
	"oldest":   "(v.vid_published_on IS NULL), v.vid_published_on ASC",
	"added":    "v.vid_created_at DESC",
	"title":    "lower(v.vid_title) COLLATE \"C\" ASC",
	"longest":  "(v.vid_duration_seconds IS NULL), v.vid_duration_seconds DESC",
	"shortest": "(v.vid_duration_seconds IS NULL), v.vid_duration_seconds ASC",
}

func (l *Lib) Videos(ctx context.Context, q Query) ([]*Video, error) {
	args := []any{l.User, l.Device}
	arg := func(v any) string {
		args = append(args, v)
		return fmt.Sprintf("$%d", len(args))
	}
	joins, where := "", []string{}
	order := ""

	switch {
	case q.Source != "":
		if _, err := l.SourceKind(ctx, q.Source); err != nil {
			return nil, err
		}
		joins += ` JOIN user_source_videos($1) sv ON sv.vid_id = v.vid_id AND sv.src_id = ` + arg(q.Source)
		// A playlist's author's order is the reason it exists; a channel
		// is newest first, which its uploads list already is.
		order = "sv.src_position ASC"
	case q.Collection != 0:
		joins += ` JOIN collection_videos cv ON cv.vid_id = v.vid_id AND cv.coll_id = ` + arg(q.Collection) +
			` JOIN collections co ON co.coll_id = cv.coll_id AND co.user_id = $1`
		order = "cv.covi_rank ASC"
	default:
		// Everything: what this user's sources hold, plus videos they kept
		// a relationship with (watched, starred, collected) after the source
		// went. A video only shelved sources carry is out of sight unless
		// asked for.
		inUnshelved := `EXISTS (SELECT 1 FROM user_source_videos($1) x JOIN user_sources($1) s ON s.src_id = x.src_id
		                         WHERE x.vid_id = v.vid_id AND NOT s.src_shelved)`
		inAny := `EXISTS (SELECT 1 FROM user_source_videos($1) x WHERE x.vid_id = v.vid_id)`
		kept := `(uv.vid_id IS NOT NULL OR EXISTS (SELECT 1 FROM collection_videos y JOIN collections c
		           ON c.coll_id = y.coll_id WHERE c.user_id = $1 AND y.vid_id = v.vid_id))`
		if q.Shelved {
			where = append(where, "("+inAny+" OR "+kept+")")
		} else {
			where = append(where, "("+inUnshelved+" OR (NOT "+inAny+" AND "+kept+"))")
		}
	}

	if s := strings.TrimSpace(q.Q); s != "" {
		like := arg("%" + escapeLike(s) + "%")
		where = append(where, "(v.vid_title ILIKE "+like+" OR COALESCE(v.vid_description, '') ILIKE "+like+")")
		if q.Source == "" && q.Collection == 0 {
			order = "(CASE WHEN v.vid_title ILIKE " + like + " THEN 0 ELSE 1 END)"
		}
	}
	if q.Have != nil {
		if *q.Have {
			where = append(where, "dv.vid_id IS NOT NULL")
		} else {
			where = append(where, "dv.vid_id IS NULL")
		}
	}
	if q.Starred {
		where = append(where, "uv.usvi_starred")
	}
	if q.Unwatched {
		where = append(where, "NOT COALESCE(uv.usvi_watched, false)")
	}
	hidden := "(COALESCE(uv.usvi_hidden, false) OR v.vid_unavailable_since IS NOT NULL)"
	if q.Hidden {
		where = append(where, hidden)
	} else {
		where = append(where, "NOT "+hidden)
	}

	sql := `SELECT ` + videoCols + videoJoins + joins
	if len(where) > 0 {
		sql += " WHERE " + strings.Join(where, " AND ")
	}
	if e, ok := explicitOrder[q.Sort]; ok {
		// An explicit sort overrides curated order; that is what it is for.
		sql += " ORDER BY " + e + ", v.vid_id COLLATE \"C\""
	} else if q.Source != "" || q.Collection != 0 {
		sql += " ORDER BY " + order + ", v.vid_id COLLATE \"C\""
	} else {
		if order != "" {
			order += ", "
		}
		sql += " ORDER BY " + order + "v.vid_published_on DESC NULLS LAST, v.vid_created_at DESC, v.vid_id COLLATE \"C\""
	}
	if q.Limit > 0 {
		sql += " LIMIT " + arg(q.Limit) + " OFFSET " + arg(max(q.Offset, 0))
	}

	rows, err := l.Pool.Query(ctx, sql, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*Video{}
	for rows.Next() {
		v, err := scanVideo(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
}

func escapeLike(s string) string {
	return strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(s)
}

// Stats describe the library this user sees on this device.
type Stats struct {
	Total       int   `json:"total"`
	Have        int   `json:"have"`
	Bytes       int64 `json:"bytes"`
	Starred     int   `json:"starred"`
	Unwatched   int   `json:"unwatched"`
	Hidden      int   `json:"hidden"`
	Channels    int   `json:"channels"`
	Playlists   int   `json:"playlists"`
	Collections int   `json:"collections"`
}

func (l *Lib) Stats(ctx context.Context) (*Stats, error) {
	var s Stats
	err := l.Pool.QueryRow(ctx, `
		WITH lib AS (
			SELECT vid_id FROM user_source_videos($1)
			UNION SELECT vid_id FROM user_videos WHERE user_id = $1
			UNION SELECT y.vid_id FROM collection_videos y JOIN collections c ON c.coll_id = y.coll_id
			       WHERE c.user_id = $1
		), cards AS (
			SELECT dv.vid_id IS NOT NULL AS have, COALESCE(dv.dvvi_size_bytes, 0) AS bytes,
			       COALESCE(uv.usvi_starred, false) AS starred, COALESCE(uv.usvi_watched, false) AS watched,
			       COALESCE(uv.usvi_hidden, false) OR v.vid_unavailable_since IS NOT NULL AS hidden
			  FROM lib JOIN videos v USING (vid_id)
			  LEFT JOIN user_videos uv ON uv.user_id = $1 AND uv.vid_id = v.vid_id
			  LEFT JOIN device_videos dv ON dv.devi_id = $2 AND dv.vid_id = v.vid_id
		)
		SELECT count(*) FILTER (WHERE NOT hidden)::int,
		       count(*) FILTER (WHERE NOT hidden AND have)::int,
		       COALESCE(sum(bytes) FILTER (WHERE NOT hidden), 0)::bigint,
		       count(*) FILTER (WHERE NOT hidden AND starred)::int,
		       count(*) FILTER (WHERE NOT hidden AND have AND NOT watched)::int,
		       count(*) FILTER (WHERE hidden)::int,
		       (SELECT count(*) FROM user_channels WHERE user_id = $1)::int,
		       (SELECT count(*) FROM user_sources($1) WHERE src_kind = 'playlist')::int,
		       (SELECT count(*) FROM collections WHERE user_id = $1)::int
		  FROM cards`, l.User, l.Device).Scan(&s.Total, &s.Have, &s.Bytes, &s.Starred, &s.Unwatched,
		&s.Hidden, &s.Channels, &s.Playlists, &s.Collections)
	return &s, err
}

// Collection is one of the user's collections with its counts.
type Collection struct {
	ID    int32  `json:"id"`
	Name  string `json:"name"`
	Total int    `json:"total"`
	Have  int    `json:"have"`
	Seen  int    `json:"seen"`
}

func (l *Lib) Collections(ctx context.Context) ([]*Collection, error) {
	rows, err := l.Pool.Query(ctx, `
		SELECT c.coll_id, c.coll_name, count(cv.vid_id)::int, count(dv.vid_id)::int,
		       count(*) FILTER (WHERE uv.usvi_watched)::int
		  FROM collections c
		  LEFT JOIN collection_videos cv ON cv.coll_id = c.coll_id
		  LEFT JOIN device_videos dv ON dv.devi_id = $2 AND dv.vid_id = cv.vid_id
		  LEFT JOIN user_videos uv ON uv.user_id = $1 AND uv.vid_id = cv.vid_id
		 WHERE c.user_id = $1
		 GROUP BY c.coll_id, c.coll_name
		 ORDER BY lower(c.coll_name)`, l.User, l.Device)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*Collection{}
	for rows.Next() {
		var c Collection
		if err := rows.Scan(&c.ID, &c.Name, &c.Total, &c.Have, &c.Seen); err != nil {
			return nil, err
		}
		out = append(out, &c)
	}
	return out, rows.Err()
}

type Ref struct {
	ID   int32  `json:"id"`
	Name string `json:"name"`
}

// CollectionsFor lists the user's collections holding a video.
func (l *Lib) CollectionsFor(ctx context.Context, vid string) ([]Ref, error) {
	rows, err := l.Pool.Query(ctx, `
		SELECT c.coll_id, c.coll_name FROM collections c
		  JOIN collection_videos cv ON cv.coll_id = c.coll_id
		 WHERE c.user_id = $1 AND cv.vid_id = $2 ORDER BY lower(c.coll_name)`, l.User, vid)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (Ref, error) {
		var x Ref
		return x, r.Scan(&x.ID, &x.Name)
	})
}

type Mark struct {
	ID   int32   `json:"id"`
	T    float64 `json:"t"`
	Note string  `json:"note"`
}

func (l *Lib) Marks(ctx context.Context, vid string) ([]Mark, error) {
	rows, err := l.Pool.Query(ctx, `
		SELECT mark_id, mark_seconds::float8, COALESCE(mark_note, '') FROM marks
		 WHERE user_id = $1 AND vid_id = $2 ORDER BY mark_seconds, mark_id`, l.User, vid)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (Mark, error) {
		var m Mark
		return m, r.Scan(&m.ID, &m.T, &m.Note)
	})
}
