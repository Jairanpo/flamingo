package library

import (
	"context"
	"strings"

	"github.com/jackc/pgx/v5"
)

// The page still names creators by their channel name in the Toggl sheet;
// the database keys rules by channel id. These translate between the two.

type TogglRule struct {
	Name    string `json:"name"`
	Project string `json:"project"`
	Tags    string `json:"tags"` // comma separated, as the sheet shows them
}

type TogglChannel struct {
	Channel string `json:"channel"`
	TogglRule
}

// TogglMap is {channel name: rule} for creators that have one.
func (l *Lib) TogglMap(ctx context.Context) (map[string]TogglRule, error) {
	rows, err := l.Pool.Query(ctx, `
		SELECT trim(c.chan_name), COALESCE(r.tcru_entry_name, ''), COALESCE(r.tcru_project, ''),
		       COALESCE(string_agg(t.tag_name, ', ' ORDER BY t.tag_id), '')
		  FROM toggl_channel_rules r
		  JOIN channels c USING (chan_id)
		  LEFT JOIN toggl_channel_rule_tags rt ON rt.user_id = r.user_id AND rt.chan_id = r.chan_id
		  LEFT JOIN tags t ON t.user_id = rt.user_id AND t.tag_id = rt.tag_id
		 WHERE r.user_id = $1
		 GROUP BY c.chan_name, r.tcru_entry_name, r.tcru_project`, l.User)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]TogglRule{}
	for rows.Next() {
		var name string
		var r TogglRule
		if err := rows.Scan(&name, &r.Name, &r.Project, &r.Tags); err != nil {
			return nil, err
		}
		out[name] = r
	}
	return out, rows.Err()
}

// TogglChannels is every creator in the user's sources, rule or blanks.
func (l *Lib) TogglChannels(ctx context.Context) ([]TogglChannel, error) {
	have, err := l.TogglMap(ctx)
	if err != nil {
		return nil, err
	}
	rows, err := l.Pool.Query(ctx, `
		SELECT DISTINCT trim(chan_name) FROM user_sources($1)
		 WHERE COALESCE(trim(chan_name), '') <> ''
		 ORDER BY 1`, l.User)
	if err != nil {
		return nil, err
	}
	names, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		return nil, err
	}
	sortFold(names)
	out := make([]TogglChannel, 0, len(names))
	for _, n := range names {
		out = append(out, TogglChannel{Channel: n, TogglRule: have[n]})
	}
	return out, nil
}

func sortFold(xs []string) {
	for i := 1; i < len(xs); i++ {
		for j := i; j > 0 && strings.ToLower(xs[j]) < strings.ToLower(xs[j-1]); j-- {
			xs[j], xs[j-1] = xs[j-1], xs[j]
		}
	}
}

// SetToggl saves one creator's rule, or drops it once it is all blank.
// Tags are saved as Toggl will get them (TogglTag); repeats go.
func (l *Lib) SetToggl(ctx context.Context, channel, name, project, tags string) error {
	channel = strings.TrimSpace(channel)
	if channel == "" {
		return badInput("which channel?")
	}
	name, project = clip(name, 256), clip(project, 128)
	var tagList []string
	seen := map[string]bool{}
	for _, t := range strings.Split(tags, ",") {
		t = TogglTag(clip(t, 64))
		if t != "" && !seen[strings.ToLower(t)] {
			seen[strings.ToLower(t)] = true
			tagList = append(tagList, t)
		}
	}
	return pgx.BeginFunc(ctx, l.Pool, func(tx pgx.Tx) error {
		var chanID string
		err := tx.QueryRow(ctx, `
			SELECT chan_id FROM user_sources($1) WHERE lower(trim(chan_name)) = lower($2) AND chan_id IS NOT NULL
			 ORDER BY (src_kind = 'channel') DESC LIMIT 1`, l.User, channel).Scan(&chanID)
		if err == pgx.ErrNoRows {
			return badInput("no channel by that name in your library")
		}
		if err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `DELETE FROM toggl_channel_rules WHERE user_id = $1 AND chan_id = $2`, l.User, chanID); err != nil {
			return err
		}
		if name == "" && project == "" && len(tagList) == 0 {
			return nil
		}
		if _, err := tx.Exec(ctx, `
			INSERT INTO toggl_channel_rules (user_id, chan_id, tcru_entry_name, tcru_project)
			VALUES ($1, $2, $3, $4)`, l.User, chanID, nullIfEmpty(name), nullIfEmpty(project)); err != nil {
			return err
		}
		for _, t := range tagList {
			var tid int32
			if err := tx.QueryRow(ctx, `
				WITH ins AS (
					INSERT INTO tags (user_id, tag_name) VALUES ($1, $2)
					ON CONFLICT (user_id, lower(tag_name)) DO NOTHING RETURNING tag_id
				)
				SELECT tag_id FROM ins
				UNION ALL SELECT tag_id FROM tags WHERE user_id = $1 AND lower(tag_name) = lower($2)
				LIMIT 1`, l.User, t).Scan(&tid); err != nil {
				return err
			}
			if _, err := tx.Exec(ctx, `INSERT INTO toggl_channel_rule_tags (user_id, chan_id, tag_id) VALUES ($1, $2, $3)`,
				l.User, chanID, tid); err != nil {
				return err
			}
		}
		return nil
	})
}
