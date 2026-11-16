package library

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// ensureVideo refuses ids the catalog has never seen.
func (l *Lib) ensureVideo(ctx context.Context, tx pgx.Tx, vid string) error {
	var ok bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM videos WHERE vid_id = $1)`, vid).Scan(&ok); err != nil {
		return err
	}
	if !ok {
		return ErrUnknownVideo
	}
	return nil
}

// userVideo makes sure the user's row for a video exists, then runs set on it.
func (l *Lib) userVideo(ctx context.Context, vid, set string, args ...any) error {
	return pgx.BeginFunc(ctx, l.Pool, func(tx pgx.Tx) error {
		if err := l.ensureVideo(ctx, tx, vid); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `INSERT INTO user_videos (user_id, vid_id) VALUES ($1, $2) ON CONFLICT DO NOTHING`,
			l.User, vid); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, `UPDATE user_videos SET `+set+` WHERE user_id = $1 AND vid_id = $2`,
			append([]any{l.User, vid}, args...)...)
		return err
	})
}

// ToggleStar flips the star and reports the new value.
func (l *Lib) ToggleStar(ctx context.Context, vid string) (bool, error) {
	if err := l.userVideo(ctx, vid, `usvi_starred = NOT usvi_starred`); err != nil {
		return false, err
	}
	v, err := l.Video(ctx, vid)
	if err != nil {
		return false, err
	}
	return v.Starred, nil
}

// SetWatched marks a video watched or not; nil value flips it.
func (l *Lib) SetWatched(ctx context.Context, vid string, value *bool) (bool, error) {
	// Inside one UPDATE every expression sees the old row, so "NOT
	// usvi_watched" is the new value in both places.
	set := `usvi_watched = NOT usvi_watched, usvi_watched_at = CASE WHEN NOT usvi_watched THEN now() END`
	var args []any
	if value != nil {
		set, args = `usvi_watched = $3, usvi_watched_at = CASE WHEN $3 THEN now() END`, []any{*value}
	}
	if err := l.userVideo(ctx, vid, set, args...); err != nil {
		return false, err
	}
	v, err := l.Video(ctx, vid)
	if err != nil {
		return false, err
	}
	return v.Watched, nil
}

// SetHidden hides a video from every view of this user; nil flips it.
// Unavailable videos stay hidden for everyone whatever this says.
func (l *Lib) SetHidden(ctx context.Context, vid string, value *bool) (bool, error) {
	set, args := `usvi_hidden = NOT usvi_hidden`, []any(nil)
	if value != nil {
		set, args = `usvi_hidden = $3`, []any{*value}
	}
	if err := l.userVideo(ctx, vid, set, args...); err != nil {
		return false, err
	}
	var hidden bool
	err := l.Pool.QueryRow(ctx, `SELECT usvi_hidden FROM user_videos WHERE user_id = $1 AND vid_id = $2`,
		l.User, vid).Scan(&hidden)
	return hidden, err
}

func (l *Lib) SetProgress(ctx context.Context, vid string, seconds float64) error {
	return l.userVideo(ctx, vid, `usvi_progress_seconds = $3`, max(seconds, 0))
}

// --- marks ------------------------------------------------------------------

func clip(s string, n int) string {
	s = strings.TrimSpace(s)
	if r := []rune(s); len(r) > n {
		return string(r[:n])
	}
	return s
}

func nullIfEmpty(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

func (l *Lib) AddMark(ctx context.Context, vid string, t float64, note string) error {
	return pgx.BeginFunc(ctx, l.Pool, func(tx pgx.Tx) error {
		if err := l.ensureVideo(ctx, tx, vid); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, `INSERT INTO marks (user_id, vid_id, mark_seconds, mark_note) VALUES ($1, $2, $3, $4)`,
			l.User, vid, max(t, 0), nullIfEmpty(clip(note, 500)))
		return err
	})
}

func (l *Lib) EditMark(ctx context.Context, vid string, id int32, note string) error {
	_, err := l.Pool.Exec(ctx, `UPDATE marks SET mark_note = $4 WHERE user_id = $1 AND vid_id = $2 AND mark_id = $3`,
		l.User, vid, id, nullIfEmpty(clip(note, 500)))
	return err
}

func (l *Lib) DeleteMark(ctx context.Context, vid string, id int32) error {
	_, err := l.Pool.Exec(ctx, `DELETE FROM marks WHERE user_id = $1 AND vid_id = $2 AND mark_id = $3`, l.User, vid, id)
	return err
}

// --- collections --------------------------------------------------------------

// CreateCollection makes a collection, or returns the one already so named.
func (l *Lib) CreateCollection(ctx context.Context, name string) (Ref, error) {
	name = clip(name, 64)
	if name == "" {
		return Ref{}, badInput("name required")
	}
	var r Ref
	err := l.Pool.QueryRow(ctx, `
		WITH ins AS (
			INSERT INTO collections (user_id, coll_name) VALUES ($1, $2)
			ON CONFLICT (user_id, lower(coll_name)) DO NOTHING RETURNING coll_id, coll_name
		)
		SELECT coll_id, coll_name FROM ins
		UNION ALL
		SELECT coll_id, coll_name FROM collections WHERE user_id = $1 AND lower(coll_name) = lower($2)
		LIMIT 1`, l.User, name).Scan(&r.ID, &r.Name)
	return r, err
}

func (l *Lib) ownCollection(ctx context.Context, tx pgx.Tx, id int32) error {
	var ok bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM collections WHERE coll_id = $1 AND user_id = $2)`,
		id, l.User).Scan(&ok); err != nil {
		return err
	}
	if !ok {
		return badInput("unknown collection")
	}
	return nil
}

func (l *Lib) CollectionAdd(ctx context.Context, id int32, vid string) error {
	return pgx.BeginFunc(ctx, l.Pool, func(tx pgx.Tx) error {
		if err := l.ownCollection(ctx, tx, id); err != nil {
			return err
		}
		if err := l.ensureVideo(ctx, tx, vid); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, `
			INSERT INTO collection_videos (coll_id, vid_id, covi_rank)
			SELECT $1, $2, COALESCE(max(covi_rank), -1) + 1 FROM collection_videos WHERE coll_id = $1
			ON CONFLICT DO NOTHING`, id, vid)
		return err
	})
}

func (l *Lib) CollectionRemove(ctx context.Context, id int32, vid string) error {
	return pgx.BeginFunc(ctx, l.Pool, func(tx pgx.Tx) error {
		if err := l.ownCollection(ctx, tx, id); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, `DELETE FROM collection_videos WHERE coll_id = $1 AND vid_id = $2`, id, vid)
		return err
	})
}

// --- sources ------------------------------------------------------------------

func (l *Lib) updateSource(ctx context.Context, tx pgx.Tx, id, set string, args ...any) error {
	table, key, _, val := sourceTable(id)
	tag, err := tx.Exec(ctx, `UPDATE `+table+` SET `+set+` WHERE user_id = $1 AND `+key+` = $2`,
		append([]any{l.User, val}, args...)...)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrUnknownSource
	}
	return nil
}

func (l *Lib) SetShelved(ctx context.Context, id string, value bool) error {
	_, _, prefix, _ := sourceTable(id)
	return pgx.BeginFunc(ctx, l.Pool, func(tx pgx.Tx) error {
		return l.updateSource(ctx, tx, id, prefix+`_shelved = $3`, value)
	})
}

// SetPinned pins or unpins a source; the cap counts every kind of source.
func (l *Lib) SetPinned(ctx context.Context, id string, value bool) error {
	_, _, prefix, _ := sourceTable(id)
	return pgx.BeginFunc(ctx, l.Pool, func(tx pgx.Tx) error {
		if !value {
			return l.updateSource(ctx, tx, id, prefix+`_pinned_at = NULL`)
		}
		// Serialise pinning per user so two tabs can't both take the last slot.
		if _, err := tx.Exec(ctx, `SELECT 1 FROM users WHERE user_id = $1 FOR UPDATE`, l.User); err != nil {
			return err
		}
		var n int
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM user_sources($1) WHERE src_pinned_at IS NOT NULL AND src_id <> $2`,
			l.User, id).Scan(&n); err != nil {
			return err
		}
		if n >= MaxPins {
			// Checked after the source exists, so an unknown id still says so.
			if _, err := l.SourceKind(ctx, id); err != nil {
				return err
			}
			return badInput(fmt.Sprintf("%d pins already — finish and unpin one first", MaxPins))
		}
		return l.updateSource(ctx, tx, id, prefix+`_pinned_at = COALESCE(`+prefix+`_pinned_at, now())`)
	})
}

// Forget stops tracking a source. The shared catalog keeps it; what this
// user remembers about its videos stays only where it still means something:
// starred, collected, or on this device.
func (l *Lib) Forget(ctx context.Context, id string) error {
	table, key, _, val := sourceTable(id)
	return pgx.BeginFunc(ctx, l.Pool, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `DELETE FROM `+table+` WHERE user_id = $1 AND `+key+` = $2`, l.User, val)
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			return ErrUnknownSource
		}
		_, err = tx.Exec(ctx, `
			DELETE FROM user_videos uv
			 WHERE uv.user_id = $1 AND NOT uv.usvi_starred
			   AND NOT EXISTS (SELECT 1 FROM user_source_videos($1) x WHERE x.vid_id = uv.vid_id)
			   AND NOT EXISTS (SELECT 1 FROM collection_videos y JOIN collections c ON c.coll_id = y.coll_id
			                    WHERE c.user_id = $1 AND y.vid_id = uv.vid_id)
			   AND NOT EXISTS (SELECT 1 FROM device_videos d WHERE d.devi_id = $2 AND d.vid_id = uv.vid_id)`,
			l.User, l.Device)
		return err
	})
}

// SetSort remembers a view's sort; "default" clears a view's own choice so
// the user's default shows through. With no source or collection it sets
// that default.
func (l *Lib) SetSort(ctx context.Context, sort, source string, collection int32) error {
	if !ValidSort(sort) {
		return badInput(fmt.Sprintf("unknown sort %q", sort))
	}
	var value *string
	if sort != "default" {
		u := strings.ToUpper(sort)
		value = &u
	}
	switch {
	case collection != 0:
		tag, err := l.Pool.Exec(ctx, `UPDATE collections SET sort_code = $3 WHERE coll_id = $1 AND user_id = $2`,
			collection, l.User, value)
		if err == nil && tag.RowsAffected() == 0 {
			return badInput("unknown collection")
		}
		return err
	case source != "":
		return pgx.BeginFunc(ctx, l.Pool, func(tx pgx.Tx) error {
			return l.updateSource(ctx, tx, source, `sort_code = $3`, value)
		})
	default:
		_, err := l.Pool.Exec(ctx, `UPDATE users SET sort_code = $2 WHERE user_id = $1`,
			l.User, strings.ToUpper(sort))
		return err
	}
}

// --- topics -------------------------------------------------------------------

// SetTopic files a creator (their channel id) under a topic; blank removes it.
func (l *Lib) SetTopic(ctx context.Context, creator, topic string) error {
	creator = strings.TrimSpace(creator)
	topic = strings.Join(strings.Fields(topic), " ")
	if creator == "" {
		return badInput("which creator?")
	}
	if strings.HasPrefix(creator, "name:") {
		return badInput("that creator has no channel id yet; sync it first")
	}
	return pgx.BeginFunc(ctx, l.Pool, func(tx pgx.Tx) error {
		if topic == "" {
			if _, err := tx.Exec(ctx, `DELETE FROM channel_topics WHERE user_id = $1 AND chan_id = $2`, l.User, creator); err != nil {
				return err
			}
		} else {
			var tid int32
			if err := tx.QueryRow(ctx, `
				WITH ins AS (
					INSERT INTO topics (user_id, topi_name) VALUES ($1, $2)
					ON CONFLICT (user_id, lower(topi_name)) DO NOTHING RETURNING topi_id
				)
				SELECT topi_id FROM ins
				UNION ALL SELECT topi_id FROM topics WHERE user_id = $1 AND lower(topi_name) = lower($2)
				LIMIT 1`, l.User, clip(topic, 64)).Scan(&tid); err != nil {
				return err
			}
			_, err := tx.Exec(ctx, `
				INSERT INTO channel_topics (user_id, chan_id, topi_id) VALUES ($1, $2, $3)
				ON CONFLICT (user_id, chan_id) DO UPDATE SET topi_id = EXCLUDED.topi_id`, l.User, creator, tid)
			var pgErr *pgconn.PgError
			if errors.As(err, &pgErr) && pgErr.Code == "23503" {
				return badInput("unknown creator")
			}
			if err != nil {
				return err
			}
		}
		// A topic nobody is filed under any more goes away with its last creator.
		_, err := tx.Exec(ctx, `
			DELETE FROM topics t WHERE t.user_id = $1
			   AND NOT EXISTS (SELECT 1 FROM channel_topics ct WHERE ct.user_id = t.user_id AND ct.topi_id = t.topi_id)`,
			l.User)
		return err
	})
}
