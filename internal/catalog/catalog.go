// Package catalog fills the shared catalog (channels, playlists, videos)
// from yt-dlp listings, and files what a user adds into their library.
//
// The catalog is shared: a channel two people track is listed once and
// stored once. What each person tracks, shelves or picks stays in their
// own user_* rows.
package catalog

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Jairanpo/yt/internal/ytdlp"
)

// Lister is the part of ytdlp.Runner the catalog needs; tests swap it.
type Lister interface {
	List(ctx context.Context, url string, limit int) (ytdlp.Meta, []ytdlp.Entry, error)
	Browse(ctx context.Context, ref string) (string, []ytdlp.BrowseItem, error)
}

type Catalog struct {
	Pool *pgxpool.Pool
	YT   Lister
}

// PicksPrefix marks a picks list's source id, as the API names it.
const PicksPrefix = "PICKS:"

// Result is what one listing changed.
type Result struct {
	SourceID string `json:"source_id"`
	Kind     string `json:"kind"` // channel | playlist
	Name     string `json:"name"`
	New      int    `json:"new"`   // videos new to this source
	Total    int    `json:"total"` // videos it holds
}

// Add lists what ref points at, stores it, and puts it in the user's
// library (off the shelf, if it was shelved: adding again is asking to see
// it). section is "course" when Browse said so, else "".
func (c *Catalog) Add(ctx context.Context, user int32, ref, section string, progress func(string)) (Result, error) {
	url := ytdlp.SourceURL(ref)
	if ytdlp.IsIndex(url) {
		return Result{}, fmt.Errorf("that is a creator's playlist index — use Browse")
	}
	meta, entries, err := c.YT.List(ctx, url, 0)
	if err != nil {
		return Result{}, err
	}
	if section == "course" && meta.Kind == "playlist" {
		meta.Section = "course"
	}
	if progress != nil {
		progress(fmt.Sprintf("%d found — filing", len(entries)))
	}
	var res Result
	err = pgx.BeginFunc(ctx, c.Pool, func(tx pgx.Tx) error {
		r, err := save(ctx, tx, meta, entries, true)
		if err != nil {
			return err
		}
		res = r
		return track(ctx, tx, user, meta, entries, &res)
	})
	return res, err
}

// Save stores one listing in the catalog. full says the listing was not
// cut short, so whatever it no longer holds has left.
func (c *Catalog) Save(ctx context.Context, meta ytdlp.Meta, entries []ytdlp.Entry, full bool) (Result, error) {
	var res Result
	err := pgx.BeginFunc(ctx, c.Pool, func(tx pgx.Tx) error {
		r, err := save(ctx, tx, meta, entries, full)
		res = r
		return err
	})
	return res, err
}

func save(ctx context.Context, tx pgx.Tx, meta ytdlp.Meta, entries []ytdlp.Entry, full bool) (Result, error) {
	entries = dedupe(entries)
	switch meta.Kind {
	case "channel":
		return saveChannel(ctx, tx, meta, entries, full)
	case "playlist":
		return savePlaylist(ctx, tx, meta, entries, full)
	case "picks":
		if err := upsertChannel(ctx, tx, meta.ChannelID, meta.Owner, meta.Handle); err != nil {
			return Result{}, err
		}
		if err := upsertUploaders(ctx, tx, entries); err != nil {
			return Result{}, err
		}
		err := upsertVideos(ctx, tx, entries, func(e ytdlp.Entry) *string {
			if ytdlp.IsChannelID(e.ChannelID) {
				return &e.ChannelID
			}
			return nil
		}, false)
		return Result{SourceID: PicksPrefix + meta.ChannelID, Kind: "playlist",
			Name: firstTitle(entries), Total: len(entries)}, err
	}
	return Result{}, fmt.Errorf("unknown listing kind %q", meta.Kind)
}

func firstTitle(entries []ytdlp.Entry) string {
	for _, e := range entries {
		if e.Title != "" {
			return e.Title
		}
	}
	return "Picked videos"
}

// dedupe keeps a video's first slot: a playlist may hold one video twice.
func dedupe(entries []ytdlp.Entry) []ytdlp.Entry {
	seen := map[string]bool{}
	out := entries[:0:0]
	for _, e := range entries {
		if !seen[e.ID] {
			seen[e.ID] = true
			out = append(out, e)
		}
	}
	return out
}

func ids(entries []ytdlp.Entry) []string {
	out := make([]string, len(entries))
	for i, e := range entries {
		out[i] = e.ID
	}
	return out
}

func saveChannel(ctx context.Context, tx pgx.Tx, meta ytdlp.Meta, entries []ytdlp.Entry, full bool) (Result, error) {
	cid := meta.ID
	if !ytdlp.IsChannelID(cid) {
		return Result{}, fmt.Errorf("YouTube gave %q as the channel id", cid)
	}
	if err := upsertChannel(ctx, tx, cid, meta.Name, meta.Handle); err != nil {
		return Result{}, err
	}
	known := map[string]int{}
	rows, err := tx.Query(ctx, `SELECT vid_id, vid_channel_position FROM videos
		WHERE chan_id = $1 AND vid_channel_position IS NOT NULL`, cid)
	if err != nil {
		return Result{}, err
	}
	for rows.Next() {
		var id string
		var pos int
		if err := rows.Scan(&id, &pos); err != nil {
			return Result{}, err
		}
		known[id] = pos
	}
	if err := rows.Err(); err != nil {
		return Result{}, err
	}
	seen := ids(entries)
	if !full {
		// A short listing only saw the newest uploads. Whatever it pushed
		// down moves down by as much, so the older ones keep their order.
		for _, e := range entries {
			if old, ok := known[e.ID]; ok {
				if shift := e.Position - old; shift > 0 {
					if _, err := tx.Exec(ctx, `UPDATE videos SET vid_channel_position = vid_channel_position + $2
						WHERE chan_id = $1 AND vid_channel_position IS NOT NULL AND NOT vid_id = ANY($3)`,
						cid, shift, seen); err != nil {
						return Result{}, err
					}
				}
				break
			}
		}
	}
	err = upsertVideos(ctx, tx, entries, func(ytdlp.Entry) *string { return &cid }, true)
	if err != nil {
		return Result{}, err
	}
	if full {
		// Gone from the uploads tab: still in the catalog (a playlist may
		// hold it), just no longer part of the channel's listing.
		if _, err := tx.Exec(ctx, `UPDATE videos SET vid_channel_position = NULL
			WHERE chan_id = $1 AND vid_channel_position IS NOT NULL AND NOT vid_id = ANY($2)`,
			cid, seen); err != nil {
			return Result{}, err
		}
	}
	if _, err := tx.Exec(ctx, `UPDATE channels SET chan_synced_at = now() WHERE chan_id = $1`, cid); err != nil {
		return Result{}, err
	}
	name := meta.Name
	if name == "" {
		name = meta.Handle
	}
	if name == "" {
		name = cid
	}
	return Result{SourceID: cid, Kind: "channel", Name: name, New: countNew(entries, known), Total: len(entries)}, nil
}

func countNew[V any](entries []ytdlp.Entry, known map[string]V) int {
	n := 0
	for _, e := range entries {
		if _, ok := known[e.ID]; !ok {
			n++
		}
	}
	return n
}

func savePlaylist(ctx context.Context, tx pgx.Tx, meta ytdlp.Meta, entries []ytdlp.Entry, full bool) (Result, error) {
	pid := meta.ID
	if !ytdlp.IsPlaylistID(pid) {
		return Result{}, fmt.Errorf("YouTube gave %q as the playlist id", pid)
	}
	var owner *string
	if ytdlp.IsChannelID(meta.ChannelID) {
		if err := upsertChannel(ctx, tx, meta.ChannelID, meta.Owner, meta.Handle); err != nil {
			return Result{}, err
		}
		owner = &meta.ChannelID
	}
	title := meta.Name
	if title == "" {
		title = pid
	}
	// Only Browse knows a course, and a live list says so in its id; a plain
	// sync leaves a shelf that was set alone.
	section, explicit := "PLAYLIST", false
	switch meta.Section {
	case "course":
		section, explicit = "COURSE", true
	case "live":
		section, explicit = "LIVE", true
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO playlists (play_id, chan_id, plse_code, play_title, play_synced_at)
		VALUES ($1, $2, $3, $4, now())
		ON CONFLICT (play_id) DO UPDATE SET
			chan_id        = COALESCE(EXCLUDED.chan_id, playlists.chan_id),
			plse_code      = CASE WHEN $5 THEN EXCLUDED.plse_code ELSE playlists.plse_code END,
			play_title     = EXCLUDED.play_title,
			play_synced_at = now()`,
		pid, owner, section, title, explicit); err != nil {
		return Result{}, err
	}
	known := map[string]bool{}
	rows, err := tx.Query(ctx, `SELECT vid_id FROM playlist_videos WHERE play_id = $1`, pid)
	if err != nil {
		return Result{}, err
	}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return Result{}, err
		}
		known[id] = true
	}
	if err := rows.Err(); err != nil {
		return Result{}, err
	}
	if err := upsertUploaders(ctx, tx, entries); err != nil {
		return Result{}, err
	}
	err = upsertVideos(ctx, tx, entries, func(e ytdlp.Entry) *string {
		if ytdlp.IsChannelID(e.ChannelID) {
			return &e.ChannelID
		}
		return nil
	}, false)
	if err != nil {
		return Result{}, err
	}
	b := &pgx.Batch{}
	for _, e := range entries {
		b.Queue(`INSERT INTO playlist_videos (play_id, vid_id, plvi_position) VALUES ($1, $2, $3)
			ON CONFLICT (play_id, vid_id) DO UPDATE SET plvi_position = EXCLUDED.plvi_position`,
			pid, e.ID, e.Position)
	}
	if full {
		b.Queue(`DELETE FROM playlist_videos WHERE play_id = $1 AND NOT vid_id = ANY($2)`, pid, ids(entries))
	}
	if err := tx.SendBatch(ctx, b).Close(); err != nil {
		return Result{}, err
	}
	return Result{SourceID: pid, Kind: "playlist", Name: title, New: countNew(entries, known), Total: len(entries)}, nil
}

// upsertChannel records a channel. A name that is only its id (YouTube
// gave none) never replaces a real one, and a handle moves to whichever
// channel holds it now.
func upsertChannel(ctx context.Context, tx pgx.Tx, id, name, handle string) error {
	if !ytdlp.IsChannelID(id) {
		return fmt.Errorf("YouTube gave %q as the channel id", id)
	}
	name = strings.TrimSpace(name)
	if name == "" {
		name = id
	}
	var h *string
	if strings.HasPrefix(handle, "@") && len(handle) <= 64 {
		h = &handle
		if _, err := tx.Exec(ctx, `UPDATE channels SET chan_handle = NULL
			WHERE chan_handle = $1 AND chan_id <> $2`, handle, id); err != nil {
			return err
		}
	}
	_, err := tx.Exec(ctx, `
		INSERT INTO channels (chan_id, chan_name, chan_handle) VALUES ($1, $2, $3)
		ON CONFLICT (chan_id) DO UPDATE SET
			chan_name   = CASE WHEN EXCLUDED.chan_name = EXCLUDED.chan_id
			                   THEN channels.chan_name ELSE EXCLUDED.chan_name END,
			chan_handle = COALESCE(EXCLUDED.chan_handle, channels.chan_handle)`,
		id, truncate(name, 128), h)
	return err
}

// upsertUploaders records the channels a playlist's videos came from, so
// those videos can say who made them.
func upsertUploaders(ctx context.Context, tx pgx.Tx, entries []ytdlp.Entry) error {
	done := map[string]bool{}
	for _, e := range entries {
		if !ytdlp.IsChannelID(e.ChannelID) || done[e.ChannelID] {
			continue
		}
		done[e.ChannelID] = true
		if err := upsertChannel(ctx, tx, e.ChannelID, e.ChannelName, ""); err != nil {
			return err
		}
	}
	return nil
}

func truncate(s string, n int) string {
	r := []rune(s)
	if len(r) > n {
		return string(r[:n])
	}
	return s
}

// upsertVideos stores each entry's facts. A slot YouTube shows without a
// title is a video gone private or deleted: it keeps its old title and is
// marked unavailable until it comes back. A known exact date is never
// replaced by a rough one.
func upsertVideos(ctx context.Context, tx pgx.Tx, entries []ytdlp.Entry, uploader func(ytdlp.Entry) *string, channelPos bool) error {
	b := &pgx.Batch{}
	for _, e := range entries {
		var pos *int
		if channelPos {
			p := e.Position
			pos = &p
		}
		title, unavailable := e.Title, e.Unavailable()
		if unavailable {
			title = e.ID
		}
		var desc, published *string
		if strings.TrimSpace(e.Description) != "" {
			desc = &e.Description
		}
		if e.Published != "" {
			published = &e.Published
		}
		b.Queue(`
			INSERT INTO videos (vid_id, chan_id, vid_title, vid_description, vid_duration_seconds,
			                    vid_published_on, vid_published_month_only, vid_channel_position,
			                    vid_unavailable_since)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, CASE WHEN $9 THEN now() END)
			ON CONFLICT (vid_id) DO UPDATE SET
				chan_id              = COALESCE(EXCLUDED.chan_id, videos.chan_id),
				vid_title            = CASE WHEN $9 THEN videos.vid_title ELSE EXCLUDED.vid_title END,
				vid_description      = COALESCE(EXCLUDED.vid_description, videos.vid_description),
				vid_duration_seconds = COALESCE(EXCLUDED.vid_duration_seconds, videos.vid_duration_seconds),
				vid_published_on     = CASE
					WHEN videos.vid_published_on IS NOT NULL AND NOT videos.vid_published_month_only
					     THEN videos.vid_published_on
					ELSE COALESCE(EXCLUDED.vid_published_on, videos.vid_published_on) END,
				vid_published_month_only = CASE
					WHEN videos.vid_published_on IS NOT NULL AND NOT videos.vid_published_month_only THEN false
					WHEN EXCLUDED.vid_published_on IS NULL THEN videos.vid_published_month_only
					ELSE EXCLUDED.vid_published_month_only END,
				vid_channel_position = COALESCE(EXCLUDED.vid_channel_position, videos.vid_channel_position),
				vid_unavailable_since = CASE WHEN $9
					THEN COALESCE(videos.vid_unavailable_since, now()) END`,
			e.ID, uploader(e), truncate(title, 512), desc, e.Duration, published, e.MonthOnly && published != nil,
			pos, unavailable)
	}
	return tx.SendBatch(ctx, b).Close()
}

// track puts a stored listing into one user's library.
func track(ctx context.Context, tx pgx.Tx, user int32, meta ytdlp.Meta, entries []ytdlp.Entry, res *Result) error {
	switch meta.Kind {
	case "channel":
		_, err := tx.Exec(ctx, `INSERT INTO user_channels (user_id, chan_id) VALUES ($1, $2)
			ON CONFLICT (user_id, chan_id) DO UPDATE SET usch_shelved = false`, user, res.SourceID)
		return err
	case "playlist":
		_, err := tx.Exec(ctx, `INSERT INTO user_playlists (user_id, play_id) VALUES ($1, $2)
			ON CONFLICT (user_id, play_id) DO UPDATE SET uspl_shelved = false`, user, res.SourceID)
		return err
	case "picks":
		var list int
		err := tx.QueryRow(ctx, `INSERT INTO user_picks (user_id, chan_id) VALUES ($1, $2)
			ON CONFLICT ON CONSTRAINT user_picks_one_per_channel DO UPDATE SET uspi_shelved = false
			RETURNING uspi_id`, user, meta.ChannelID).Scan(&list)
		if err != nil {
			return err
		}
		// A pick lands after the ones before it.
		res.New = 0
		for _, e := range dedupe(entries) {
			tag, err := tx.Exec(ctx, `INSERT INTO user_pick_videos (uspi_id, vid_id, upvi_position)
				SELECT $1, $2, COALESCE(max(upvi_position) + 1, 0) FROM user_pick_videos WHERE uspi_id = $1
				ON CONFLICT (uspi_id, vid_id) DO NOTHING`, list, e.ID)
			if err != nil {
				return err
			}
			res.New += int(tag.RowsAffected())
		}
		return tx.QueryRow(ctx, `SELECT count(*) FROM user_pick_videos WHERE uspi_id = $1`, list).Scan(&res.Total)
	}
	return errors.New("nothing to track")
}

// MarkCourses files the playlists Browse found on a Courses tab as
// courses, for those already in the catalog.
func (c *Catalog) MarkCourses(ctx context.Context, ids []string) error {
	if len(ids) == 0 {
		return nil
	}
	_, err := c.Pool.Exec(ctx, `UPDATE playlists SET plse_code = 'COURSE'
		WHERE play_id = ANY($1) AND plse_code <> 'COURSE'`, ids)
	return err
}
