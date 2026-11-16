package catalog

import (
	"context"
	"fmt"
	"strings"

	"github.com/Jairanpo/flamingo/internal/ytdlp"
)

// Target is one source a sync lists again.
type Target struct {
	ID   string // channel id, playlist id, or PICKS:<channel id>
	Kind string // channel | playlist | picks
	Name string
	url  string
	vids []string // picks only: every video anyone picked from the channel
}

// SyncOptions choose what Targets returns.
type SyncOptions struct {
	Source  string // just this one, shelved or not
	Shelved bool   // include sources everyone has shelved
}

// Targets lists the sources to sync. Something counts as shelved only when
// everyone tracking it has shelved it: a shelf of a creator's whole back
// catalogue would otherwise cost a round-trip per playlist on every sync.
// Returns the targets and how many shelved ones were left out.
func (c *Catalog) Targets(ctx context.Context, o SyncOptions) ([]Target, int, error) {
	rows, err := c.Pool.Query(ctx, `
		SELECT c.chan_id, 'channel', c.chan_name, bool_and(uc.usch_shelved), NULL::text[]
		  FROM user_channels uc JOIN channels c USING (chan_id)
		 GROUP BY c.chan_id
		UNION ALL
		SELECT p.play_id, 'playlist', p.play_title, bool_and(up.uspl_shelved), NULL
		  FROM user_playlists up JOIN playlists p USING (play_id)
		 GROUP BY p.play_id
		UNION ALL
		SELECT 'PICKS:' || c.chan_id, 'picks', c.chan_name, bool_and(pk.uspi_shelved),
		       array_agg(DISTINCT pv.vid_id)
		  FROM user_picks pk JOIN channels c USING (chan_id)
		  JOIN user_pick_videos pv USING (uspi_id)
		 GROUP BY c.chan_id
		 ORDER BY 2, 3`)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	var out []Target
	skipped := 0
	for rows.Next() {
		var t Target
		var shelved bool
		if err := rows.Scan(&t.ID, &t.Kind, &t.Name, &shelved, &t.vids); err != nil {
			return nil, 0, err
		}
		if o.Source != "" {
			if !strings.EqualFold(t.ID, o.Source) {
				continue
			}
		} else if shelved && !o.Shelved {
			skipped++
			continue
		}
		switch t.Kind {
		case "channel":
			t.url = ytdlp.ChannelURL(t.ID)
		case "playlist":
			if ch := ytdlp.ChannelOfLive(t.ID); ch != "" {
				// The Live tab is the reliable way to list past streams.
				t.url = "https://www.youtube.com/channel/" + ch + "/streams"
			} else {
				t.url = ytdlp.PlaylistURL(t.ID)
			}
		}
		out = append(out, t)
	}
	if err := rows.Err(); err != nil {
		return nil, 0, err
	}
	if o.Source != "" && len(out) == 0 {
		return nil, 0, fmt.Errorf("nobody tracks %q", o.Source)
	}
	return out, skipped, nil
}

// Sync lists one target again and stores what changed. limit > 0 lists
// only the newest that many, which never counts as a full pass.
func (c *Catalog) Sync(ctx context.Context, t Target, limit int) (Result, error) {
	if t.Kind == "picks" {
		// Picks are refreshed in rounds: each is its own request.
		total := Result{SourceID: t.ID, Kind: "playlist", Name: t.Name}
		for i := 0; i < len(t.vids); i += 50 {
			end := min(i+50, len(t.vids))
			meta, entries, err := c.YT.List(ctx, ytdlp.PicksURL+strings.Join(t.vids[i:end], ","), 0)
			if err != nil {
				return total, err
			}
			if _, err := c.Save(ctx, meta, entries, false); err != nil {
				return total, err
			}
			total.Total += len(entries)
		}
		return total, nil
	}
	meta, entries, err := c.YT.List(ctx, t.url, limit)
	if err != nil {
		return Result{}, err
	}
	return c.Save(ctx, meta, entries, limit == 0)
}
