package importer

import (
	"context"
	"fmt"
	"sort"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Options says what to import and for whom.
type Options struct {
	SQLitePath  string
	UserName    string
	DisplayName string // used only when the user is created
	DeviceName  string // "" skips recording downloads
	Resolver    Resolver
	Now         time.Time
}

// Report counts what was written, plus anything that could not be carried
// over as it was.
type Report struct {
	UserID   int32
	Counts   []Count
	Warnings []string
}

type Count struct {
	Table string
	Rows  int
}

// Run reads the old SQLite library and writes it in one transaction: the
// catalog is merged (it is shared), while the user's own library and the
// device's download list are replaced, so running it again is safe.
func Run(ctx context.Context, pool *pgxpool.Pool, o Options) (*Report, error) {
	if o.Now.IsZero() {
		o.Now = time.Now().UTC()
	}
	snap, err := readSQLite(ctx, o.SQLitePath)
	if err != nil {
		return nil, err
	}
	p, err := buildPlan(ctx, snap, o.Resolver, o.Now)
	if err != nil {
		return nil, err
	}

	rep := &Report{Warnings: p.warnings}
	err = pgx.BeginFunc(ctx, pool, func(tx pgx.Tx) error {
		w := &writer{ctx: ctx, tx: tx, rep: rep}
		return w.all(p, o)
	})
	if err != nil {
		return nil, err
	}
	return rep, nil
}

type writer struct {
	ctx  context.Context
	tx   pgx.Tx
	rep  *Report
	user int32
}

// batch queues one statement per row and sends them together.
func (w *writer) batch(table string, n int, sql string, args func(i int) []any) error {
	b := &pgx.Batch{}
	for i := range n {
		b.Queue(sql, args(i)...)
	}
	if err := w.tx.SendBatch(w.ctx, b).Close(); err != nil {
		return fmt.Errorf("writing %s: %w", table, err)
	}
	w.rep.Counts = append(w.rep.Counts, Count{table, n})
	return nil
}

func (w *writer) exec(sql string, args ...any) error {
	_, err := w.tx.Exec(w.ctx, sql, args...)
	return err
}

func (w *writer) all(p *plan, o Options) error {
	// --- user -----------------------------------------------------------
	display := o.DisplayName
	if display == "" {
		display = o.UserName
	}
	err := w.tx.QueryRow(w.ctx, `
		INSERT INTO users (user_name, user_display_name, user_role, sort_order)
		VALUES ($1, $2, CASE WHEN EXISTS (SELECT 1 FROM users) THEN 'MEMBER' ELSE 'ADMIN' END, $3)
		ON CONFLICT (user_name) DO UPDATE SET sort_order = EXCLUDED.sort_order
		RETURNING user_id`, o.UserName, display, p.defaultSort).Scan(&w.user)
	if err != nil {
		return fmt.Errorf("creating user %s: %w", o.UserName, err)
	}
	w.rep.UserID = w.user
	for _, t := range []string{"user_channels", "user_playlists", "user_picks", "user_videos",
		"collections", "marks", "channel_topics", "topics", "toggl_channel_rules", "tags"} {
		if err := w.exec("DELETE FROM "+t+" WHERE user_id = $1", w.user); err != nil {
			return fmt.Errorf("clearing %s: %w", t, err)
		}
	}

	// --- catalog (merged) -------------------------------------------------
	chans := make([]*channelRow, 0, len(p.channels))
	for _, c := range p.channels {
		chans = append(chans, c)
	}
	sort.Slice(chans, func(i, j int) bool { return chans[i].ID < chans[j].ID })
	// A handle already held by a different channel in the database makes
	// this fail loudly rather than move it silently.
	if err := w.batch("channels", len(chans), `
		INSERT INTO channels (chan_id, chan_handle, chan_name, chan_synced_at, chan_created_at)
		VALUES ($1, $2, $3, $4, COALESCE($5, now()))
		ON CONFLICT (chan_id) DO UPDATE SET
			chan_handle    = COALESCE(EXCLUDED.chan_handle, channels.chan_handle),
			chan_name      = EXCLUDED.chan_name,
			chan_synced_at = GREATEST(channels.chan_synced_at, EXCLUDED.chan_synced_at)`,
		func(i int) []any {
			c := chans[i]
			return []any{c.ID, c.Handle, c.Name, c.SyncedAt, c.CreatedAt}
		}); err != nil {
		return err
	}

	if err := w.batch("playlists", len(p.playlists), `
		INSERT INTO playlists (plist_id, chan_id, plist_section, plist_title, plist_synced_at, plist_created_at)
		VALUES ($1, $2, $3, $4, $5, $6)
		ON CONFLICT (plist_id) DO UPDATE SET
			chan_id         = COALESCE(EXCLUDED.chan_id, playlists.chan_id),
			plist_section   = EXCLUDED.plist_section,
			plist_title     = EXCLUDED.plist_title,
			plist_synced_at = GREATEST(playlists.plist_synced_at, EXCLUDED.plist_synced_at)`,
		func(i int) []any {
			r := p.playlists[i]
			return []any{r.ID, r.ChanID, r.Section, r.Title, r.SyncedAt, r.CreatedAt}
		}); err != nil {
		return err
	}

	if err := w.batch("videos", len(p.videos), `
		INSERT INTO videos (vid_id, chan_id, vid_title, vid_description, vid_duration_seconds,
		                    vid_published_on, vid_published_month_only, vid_channel_position,
		                    vid_unavailable_since, vid_chapters_checked_at, vid_created_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11)
		ON CONFLICT (vid_id) DO UPDATE SET
			chan_id                  = COALESCE(EXCLUDED.chan_id, videos.chan_id),
			vid_title                = EXCLUDED.vid_title,
			vid_description          = COALESCE(EXCLUDED.vid_description, videos.vid_description),
			vid_duration_seconds     = COALESCE(EXCLUDED.vid_duration_seconds, videos.vid_duration_seconds),
			vid_published_on         = COALESCE(EXCLUDED.vid_published_on, videos.vid_published_on),
			vid_published_month_only = EXCLUDED.vid_published_month_only,
			vid_channel_position     = COALESCE(EXCLUDED.vid_channel_position, videos.vid_channel_position),
			vid_unavailable_since    = CASE WHEN EXCLUDED.vid_unavailable_since IS NULL THEN NULL
			                                ELSE COALESCE(videos.vid_unavailable_since, EXCLUDED.vid_unavailable_since) END,
			vid_chapters_checked_at  = COALESCE(EXCLUDED.vid_chapters_checked_at, videos.vid_chapters_checked_at),
			vid_created_at           = LEAST(videos.vid_created_at, EXCLUDED.vid_created_at)`,
		func(i int) []any {
			v := p.videos[i]
			return []any{v.ID, v.ChanID, v.Title, v.Description, v.Duration, v.PublishedOn, v.MonthOnly,
				v.ChannelPos, v.UnavailableSince, v.ChaptersCheckedAt, v.CreatedAt}
		}); err != nil {
		return err
	}

	plistIDs := make([]string, len(p.playlists))
	for i, r := range p.playlists {
		plistIDs[i] = r.ID
	}
	if err := w.exec("DELETE FROM playlist_videos WHERE plist_id = ANY($1)", plistIDs); err != nil {
		return err
	}
	if err := w.batch("playlist_videos", len(p.plVideos), `
		INSERT INTO playlist_videos (plist_id, vid_id, plvid_position) VALUES ($1, $2, $3)`,
		func(i int) []any {
			r := p.plVideos[i]
			return []any{r.PlistID, r.VidID, r.Position}
		}); err != nil {
		return err
	}

	if err := w.exec("DELETE FROM video_chapters WHERE vid_id = ANY($1)", p.chapterVids); err != nil {
		return err
	}
	if err := w.batch("video_chapters", len(p.chapters), `
		INSERT INTO video_chapters (vid_id, chap_start_seconds, chap_end_seconds, chap_title)
		VALUES ($1, $2, $3, $4) ON CONFLICT DO NOTHING`,
		func(i int) []any {
			c := p.chapters[i]
			return []any{c.VidID, c.Start, c.End, c.Title}
		}); err != nil {
		return err
	}

	// --- the user's library (replaced) -------------------------------------
	if err := w.batch("user_channels", len(p.userChans), `
		INSERT INTO user_channels (user_id, chan_id, uchan_shelved, uchan_pinned_at, sort_order, uchan_created_at)
		VALUES ($1, $2, $3, $4, $5, $6)`,
		func(i int) []any {
			r := p.userChans[i]
			return []any{w.user, r.ID, r.Shelved, r.PinnedAt, r.Sort, r.CreatedAt}
		}); err != nil {
		return err
	}
	if err := w.batch("user_playlists", len(p.userPlists), `
		INSERT INTO user_playlists (user_id, plist_id, uplist_shelved, uplist_pinned_at, sort_order, uplist_created_at)
		VALUES ($1, $2, $3, $4, $5, $6)`,
		func(i int) []any {
			r := p.userPlists[i]
			return []any{w.user, r.ID, r.Shelved, r.PinnedAt, r.Sort, r.CreatedAt}
		}); err != nil {
		return err
	}

	pickVids := 0
	for _, pk := range p.picks {
		var id int32
		if err := w.tx.QueryRow(w.ctx, `
			INSERT INTO user_picks (user_id, chan_id, upick_shelved, upick_pinned_at, sort_order, upick_created_at)
			VALUES ($1, $2, $3, $4, $5, $6) RETURNING upick_id`,
			w.user, pk.ID, pk.Shelved, pk.PinnedAt, pk.Sort, pk.CreatedAt).Scan(&id); err != nil {
			return fmt.Errorf("writing user_picks: %w", err)
		}
		b := &pgx.Batch{}
		for _, v := range pk.Videos {
			b.Queue(`INSERT INTO user_pick_videos (upick_id, vid_id, upvid_position) VALUES ($1, $2, $3)`,
				id, v.VidID, v.Position)
		}
		if err := w.tx.SendBatch(w.ctx, b).Close(); err != nil {
			return fmt.Errorf("writing user_pick_videos: %w", err)
		}
		pickVids += len(pk.Videos)
	}
	w.rep.Counts = append(w.rep.Counts, Count{"user_picks", len(p.picks)}, Count{"user_pick_videos", pickVids})

	if err := w.batch("user_videos", len(p.userVideos), `
		INSERT INTO user_videos (user_id, vid_id, uvid_watched, uvid_watched_at, uvid_progress_seconds,
		                         uvid_starred, uvid_hidden)
		VALUES ($1, $2, $3, $4, $5, $6, $7)`,
		func(i int) []any {
			r := p.userVideos[i]
			return []any{w.user, r.VidID, r.Watched, r.WatchedAt, r.Progress, r.Starred, r.Hidden}
		}); err != nil {
		return err
	}

	collVids := 0
	for _, c := range p.collections {
		var id int32
		if err := w.tx.QueryRow(w.ctx, `
			INSERT INTO collections (user_id, coll_name, sort_order, coll_created_at)
			VALUES ($1, $2, $3, $4) RETURNING coll_id`, w.user, c.Name, c.Sort, c.CreatedAt).Scan(&id); err != nil {
			return fmt.Errorf("writing collections: %w", err)
		}
		b := &pgx.Batch{}
		for _, it := range c.Items {
			b.Queue(`INSERT INTO collection_videos (coll_id, vid_id, collvid_rank, collvid_added_at)
			         VALUES ($1, $2, $3, $4)`, id, it.VideoID, it.Rank, epoch(it.AddedAt))
		}
		if err := w.tx.SendBatch(w.ctx, b).Close(); err != nil {
			return fmt.Errorf("writing collection_videos: %w", err)
		}
		collVids += len(c.Items)
	}
	w.rep.Counts = append(w.rep.Counts, Count{"collections", len(p.collections)}, Count{"collection_videos", collVids})

	if err := w.batch("marks", len(p.marks), `
		INSERT INTO marks (user_id, vid_id, mark_seconds, mark_note, mark_created_at) VALUES ($1, $2, $3, $4, $5)`,
		func(i int) []any {
			m := p.marks[i]
			return []any{w.user, m.VidID, m.Seconds, m.Note, m.CreatedAt}
		}); err != nil {
		return err
	}

	topicNames := make([]string, 0, len(p.topics))
	for name := range p.topics {
		topicNames = append(topicNames, name)
	}
	sort.Strings(topicNames)
	linked := 0
	for _, name := range topicNames {
		var id int32
		if err := w.tx.QueryRow(w.ctx, `INSERT INTO topics (user_id, topic_name) VALUES ($1, $2) RETURNING topic_id`,
			w.user, name).Scan(&id); err != nil {
			return fmt.Errorf("writing topics: %w", err)
		}
		for _, chanID := range p.topics[name] {
			// One topic per creator: a later name for the same channel wins.
			if err := w.exec(`
				INSERT INTO channel_topics (user_id, chan_id, topic_id) VALUES ($1, $2, $3)
				ON CONFLICT (user_id, chan_id) DO UPDATE SET topic_id = EXCLUDED.topic_id`,
				w.user, chanID, id); err != nil {
				return fmt.Errorf("writing channel_topics: %w", err)
			}
			linked++
		}
	}
	w.rep.Counts = append(w.rep.Counts, Count{"topics", len(topicNames)}, Count{"channel_topics", linked})

	if err := w.rules(p); err != nil {
		return err
	}

	// --- the device's downloads (replaced) -----------------------------------
	if o.DeviceName == "" {
		return nil
	}
	var dev int32
	if err := w.tx.QueryRow(w.ctx, `
		INSERT INTO devices (dev_name) VALUES ($1)
		ON CONFLICT (dev_name) DO UPDATE SET dev_name = EXCLUDED.dev_name
		RETURNING dev_id`, o.DeviceName).Scan(&dev); err != nil {
		return fmt.Errorf("writing devices: %w", err)
	}
	if err := w.exec("DELETE FROM device_videos WHERE dev_id = $1", dev); err != nil {
		return err
	}
	return w.batch("device_videos", len(p.downloads), `
		INSERT INTO device_videos (dev_id, vid_id, devvid_size_bytes, devvid_downloaded_at) VALUES ($1, $2, $3, $4)`,
		func(i int) []any {
			d := p.downloads[i]
			return []any{dev, d.VidID, d.Size, d.DownloadedAt}
		})
}

func (w *writer) rules(p *plan) error {
	tagID := map[string]int32{}
	chans := make([]string, 0, len(p.rules))
	for id := range p.rules {
		chans = append(chans, id)
	}
	sort.Strings(chans)
	links := 0
	for _, chanID := range chans {
		r := p.rules[chanID]
		if err := w.exec(`
			INSERT INTO toggl_channel_rules (user_id, chan_id, tgrule_entry_name, tgrule_project, tgrule_created_at)
			VALUES ($1, $2, $3, $4, $5)`, w.user, chanID, r.Name, r.Project, r.CreatedAt); err != nil {
			return fmt.Errorf("writing toggl_channel_rules: %w", err)
		}
		for _, tag := range r.Tags {
			id, ok := tagID[key(tag)]
			if !ok {
				if err := w.tx.QueryRow(w.ctx, `INSERT INTO tags (user_id, tag_name) VALUES ($1, $2) RETURNING tag_id`,
					w.user, tag).Scan(&id); err != nil {
					return fmt.Errorf("writing tags: %w", err)
				}
				tagID[key(tag)] = id
			}
			if err := w.exec(`INSERT INTO toggl_channel_rule_tags (user_id, chan_id, tag_id) VALUES ($1, $2, $3)`,
				w.user, chanID, id); err != nil {
				return fmt.Errorf("writing toggl_channel_rule_tags: %w", err)
			}
			links++
		}
	}
	w.rep.Counts = append(w.rep.Counts, Count{"toggl_channel_rules", len(chans)},
		Count{"tags", len(tagID)}, Count{"toggl_channel_rule_tags", links})
	return nil
}
