package catalog

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/Jairanpo/flamingo/internal/ytdlp"
)

// ErrUnknownVideo: the catalog has no such video.
var ErrUnknownVideo = errors.New("unknown video")

// Chapters returns a video's chapters, asking YouTube the first time only.
// Offline or rate-limited, it answers none for now and asks again next time.
func (c *Catalog) Chapters(ctx context.Context, vid string) ([]ytdlp.Chapter, error) {
	var checked *time.Time
	err := c.Pool.QueryRow(ctx, `SELECT vid_chapters_checked_at FROM videos WHERE vid_id = $1`, vid).Scan(&checked)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrUnknownVideo
	}
	if err != nil {
		return nil, err
	}
	if checked != nil {
		return c.storedChapters(ctx, vid)
	}
	d, err := c.YT.Details(ctx, vid)
	if err != nil {
		return []ytdlp.Chapter{}, nil
	}
	found := d.Chapters
	if found == nil {
		found = []ytdlp.Chapter{} // the page reads a list, never null
	}
	if err := c.FillDescription(ctx, vid, d.Description); err != nil {
		return nil, err
	}
	return found, c.SetChapters(ctx, vid, found)
}

// Description returns a video's description, asking YouTube the first time
// only: quick syncs list videos without one. Offline or rate-limited, it
// answers none for now and asks again next time.
func (c *Catalog) Description(ctx context.Context, vid string) (string, error) {
	var desc *string
	var checked *time.Time
	err := c.Pool.QueryRow(ctx, `SELECT vid_description, vid_chapters_checked_at FROM videos WHERE vid_id = $1`,
		vid).Scan(&desc, &checked)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", ErrUnknownVideo
	}
	if err != nil {
		return "", err
	}
	if desc != nil {
		return *desc, nil
	}
	d, err := c.YT.Details(ctx, vid)
	if err != nil {
		return "", nil
	}
	if err := c.FillDescription(ctx, vid, d.Description); err != nil {
		return "", err
	}
	// The same answer carries the chapters; keep them if never looked up.
	if checked == nil {
		if err := c.SetChapters(ctx, vid, d.Chapters); err != nil {
			return "", err
		}
	}
	return d.Description, nil
}

// FillDescription stores a description the catalog lacks. NULL means never
// asked; an empty one means asked, and the author wrote none, so it is not
// asked again. A description already known is left alone.
func (c *Catalog) FillDescription(ctx context.Context, vid, desc string) error {
	_, err := c.Pool.Exec(ctx, `
		UPDATE videos SET vid_description = $2
		 WHERE vid_id = $1 AND (vid_description IS NULL OR (vid_description = '' AND $2 <> ''))`, vid, desc)
	return err
}

func (c *Catalog) storedChapters(ctx context.Context, vid string) ([]ytdlp.Chapter, error) {
	rows, err := c.Pool.Query(ctx, `
		SELECT vich_start_seconds::float8, vich_end_seconds::float8, vich_title
		  FROM video_chapters WHERE vid_id = $1 ORDER BY vich_start_seconds`, vid)
	if err != nil {
		return nil, err
	}
	out, err := pgx.CollectRows(rows, pgx.RowToStructByPos[ytdlp.Chapter])
	if out == nil {
		out = []ytdlp.Chapter{}
	}
	return out, err
}

// SetChapters replaces a video's chapters and notes they were looked up.
// A video the catalog lacks is left alone.
func (c *Catalog) SetChapters(ctx context.Context, vid string, chs []ytdlp.Chapter) error {
	return pgx.BeginFunc(ctx, c.Pool, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `UPDATE videos SET vid_chapters_checked_at = now() WHERE vid_id = $1`, vid)
		if err != nil || tag.RowsAffected() == 0 {
			return err
		}
		if _, err := tx.Exec(ctx, `DELETE FROM video_chapters WHERE vid_id = $1`, vid); err != nil {
			return err
		}
		seen := map[float64]bool{}
		for _, ch := range chs {
			// The key is the start; a broken listing may repeat one.
			if seen[ch.Start] || ch.Start < 0 || ch.End < ch.Start {
				continue
			}
			seen[ch.Start] = true
			if _, err := tx.Exec(ctx, `
				INSERT INTO video_chapters (vid_id, vich_start_seconds, vich_end_seconds, vich_title)
				VALUES ($1, $2, $3, $4)`, vid, ch.Start, ch.End, truncate(ch.Title, 256)); err != nil {
				return err
			}
		}
		return nil
	})
}

// FillDuration sets a video's length when the catalog listing lacked one:
// without it a streamed video's card cannot show how far in you are.
func (c *Catalog) FillDuration(ctx context.Context, vid string, seconds *int) error {
	if seconds == nil || *seconds < 0 {
		return nil
	}
	_, err := c.Pool.Exec(ctx, `
		UPDATE videos SET vid_duration_seconds = $2
		 WHERE vid_id = $1 AND vid_duration_seconds IS NULL`, vid, *seconds)
	return err
}
