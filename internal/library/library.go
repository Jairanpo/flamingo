// Package library is one user's view of the catalog: what they track, how
// far they are, and how they arranged it. Every query here is scoped by the
// user id it was opened with, and "on disk" means on the device the request
// came from.
package library

import (
	"context"
	"errors"
	"sort"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// MaxPins: one priority is ideal, three is plenty, past five the dashboard
// stops being a short list.
const MaxPins = 5

const PicksPrefix = "PICKS:"

var (
	ErrUnknownSource = errors.New("unknown source")
	ErrUnknownVideo  = errors.New("unknown video")
	ErrBadInput      = errors.New("bad input") // wrapped with a message the page can show
)

type inputError struct{ msg string }

func (e inputError) Error() string { return e.msg }
func (e inputError) Unwrap() error { return ErrBadInput }

func badInput(msg string) error { return inputError{msg} }

// Lib is one user's library, seen from one device (Device 0: none known).
type Lib struct {
	Pool   *pgxpool.Pool
	User   int32
	Device int32
}

// Sorts, in the order the page lists them, with their labels.
var Sorts = []struct{ Key, Label string }{
	{"default", "This view's own order"},
	{"newest", "Newest first"},
	{"oldest", "Oldest first"},
	{"added", "Recently added"},
	{"title", "Title A–Z"},
	{"longest", "Longest first"},
	{"shortest", "Shortest first"},
}

func ValidSort(s string) bool {
	for _, x := range Sorts {
		if x.Key == s {
			return true
		}
	}
	return false
}

// sourceTable says where a source id's per-user row lives.
func sourceTable(id string) (table, key, prefix string, keyVal string) {
	switch {
	case strings.HasPrefix(id, PicksPrefix):
		return "user_picks", "chan_id", "uspi", strings.TrimPrefix(id, PicksPrefix)
	case strings.HasPrefix(id, "UC") && len(id) == 24:
		return "user_channels", "chan_id", "usch", id
	default:
		return "user_playlists", "play_id", "uspl", id
	}
}

// Source is one of the user's sources with its counts.
type Source struct {
	ID, Kind          string
	Section           *string
	Title             string
	ChanID, ChanName  *string
	Handle            *string
	Shelved           bool
	Pinned            bool
	PinnedAt          *int64
	Completed         bool
	CompletedAt       *int64
	Sort              *string
	Total, Have, Seen int
	Topic             string
}

func (l *Lib) Sources(ctx context.Context) ([]*Source, error) {
	rows, err := l.Pool.Query(ctx, `
		SELECT s.src_id, s.src_kind, s.src_section, s.src_title, s.chan_id, s.chan_name, s.chan_handle,
		       s.src_shelved, extract(epoch FROM s.src_pinned_at)::bigint, lower(s.sort_code),
		       count(sv.vid_id)::int,
		       count(dv.vid_id)::int,
		       count(*) FILTER (WHERE uv.usvi_watched)::int,
		       COALESCE(t.topi_name, ''),
		       extract(epoch FROM s.src_completed_at)::bigint
		  FROM user_sources($1) s
		  LEFT JOIN user_source_videos($1) sv ON sv.src_id = s.src_id
		  LEFT JOIN device_videos dv ON dv.devi_id = $2 AND dv.vid_id = sv.vid_id
		  LEFT JOIN user_videos uv ON uv.user_id = $1 AND uv.vid_id = sv.vid_id
		  LEFT JOIN channel_topics ct ON ct.user_id = $1 AND ct.chan_id = s.chan_id
		  LEFT JOIN topics t ON t.user_id = ct.user_id AND t.topi_id = ct.topi_id
		 GROUP BY s.src_id, s.src_kind, s.src_section, s.src_title, s.chan_id, s.chan_name, s.chan_handle,
		          s.src_shelved, s.src_pinned_at, s.sort_code, t.topi_name, s.src_completed_at`, l.User, l.Device)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*Source
	for rows.Next() {
		var s Source
		if err := rows.Scan(&s.ID, &s.Kind, &s.Section, &s.Title, &s.ChanID, &s.ChanName, &s.Handle,
			&s.Shelved, &s.PinnedAt, &s.Sort, &s.Total, &s.Have, &s.Seen, &s.Topic, &s.CompletedAt); err != nil {
			return nil, err
		}
		s.Pinned = s.PinnedAt != nil
		s.Completed = s.CompletedAt != nil
		out = append(out, &s)
	}
	return out, rows.Err()
}

var sectionRank = map[string]int{"course": 1, "live": 2, "": 3, "picks": 4}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

// Group is one creator with their channel (if tracked) and other sources.
type Group struct {
	Key     string
	Creator *string
	Channel *Source
	Items   []*Source
}

// ByCreator files sources under their creator: creators A–Z, unknown last;
// within one, the channel, then courses, live streams, playlists, picks.
func ByCreator(srcs []*Source) []*Group {
	groups := map[string]*Group{}
	var order []*Group
	for _, s := range srcs {
		key := deref(s.ChanID)
		if key == "" {
			key = "name:" + strings.ToLower(strings.TrimSpace(s.Title))
		}
		g := groups[key]
		if g == nil {
			g = &Group{Key: key}
			if s.ChanName != nil {
				n := strings.TrimSpace(*s.ChanName)
				g.Creator = &n
			}
			groups[key] = g
			order = append(order, g)
		}
		if s.Kind == "channel" {
			g.Channel = s
		} else {
			g.Items = append(g.Items, s)
		}
	}
	for _, g := range order {
		sort.SliceStable(g.Items, func(i, j int) bool {
			a, b := g.Items[i], g.Items[j]
			ra, rb := sectionRank[deref(a.Section)], sectionRank[deref(b.Section)]
			if ra != rb {
				return ra < rb
			}
			return strings.ToLower(a.Title) < strings.ToLower(b.Title)
		})
	}
	sort.SliceStable(order, func(i, j int) bool {
		a, b := order[i], order[j]
		if (a.Creator == nil) != (b.Creator == nil) {
			return b.Creator == nil
		}
		return strings.ToLower(deref(a.Creator)) < strings.ToLower(deref(b.Creator))
	})
	return order
}

// SortFor is the sort a view uses: its own, else the user's default.
// source and collection are mutually exclusive; both empty is the global one.
func (l *Lib) SortFor(ctx context.Context, source string, collection int32) (string, error) {
	var own *string
	var err error
	switch {
	case collection != 0:
		err = l.Pool.QueryRow(ctx, `SELECT lower(sort_code) FROM collections WHERE coll_id = $1 AND user_id = $2`,
			collection, l.User).Scan(&own)
	case source != "":
		table, key, _, val := sourceTable(source)
		err = l.Pool.QueryRow(ctx, `SELECT lower(sort_code) FROM `+table+` WHERE user_id = $1 AND `+key+` = $2`,
			l.User, val).Scan(&own)
	}
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return "", err
	}
	if own != nil && ValidSort(*own) {
		return *own, nil
	}
	var global string
	if err := l.Pool.QueryRow(ctx, `SELECT lower(sort_code) FROM users WHERE user_id = $1`, l.User).Scan(&global); err != nil {
		return "", err
	}
	if !ValidSort(global) {
		global = "default"
	}
	return global, nil
}

// SourceKind reports "channel" or "playlist" for one of the user's sources.
func (l *Lib) SourceKind(ctx context.Context, id string) (string, error) {
	var kind string
	err := l.Pool.QueryRow(ctx, `SELECT src_kind FROM user_sources($1) WHERE src_id = $2`, l.User, id).Scan(&kind)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", ErrUnknownSource
	}
	return kind, err
}
