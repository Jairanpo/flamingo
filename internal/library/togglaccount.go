package library

import (
	"context"
	"errors"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Jairanpo/flamingo/internal/secret"
	"github.com/Jairanpo/flamingo/internal/toggl"
)

// ErrNoToggl: the user has not connected Toggl.
var ErrNoToggl = errors.New("Toggl is not connected for you")

// TogglAccount opens the user's sealed token.
func (l *Lib) TogglAccount(ctx context.Context, box *secret.Box) (toggl.Account, error) {
	var sealed []byte
	var wid *int64
	err := l.Pool.QueryRow(ctx, `
		SELECT toac_token_sealed, toac_workspace_id FROM toggl_accounts WHERE user_id = $1`, l.User).Scan(&sealed, &wid)
	if errors.Is(err, pgx.ErrNoRows) {
		return toggl.Account{}, ErrNoToggl
	}
	if err != nil {
		return toggl.Account{}, err
	}
	tok, err := box.Open(sealed)
	if err != nil {
		return toggl.Account{}, err
	}
	a := toggl.Account{Token: string(tok)}
	if wid != nil {
		a.Workspace = *wid
	}
	return a, nil
}

// SetTogglAccount seals and stores a user's token; an empty token removes
// the connection. workspace 0 means their default.
func SetTogglAccount(ctx context.Context, pool *pgxpool.Pool, box *secret.Box, userName, token string, workspace int64) error {
	var uid int32
	err := pool.QueryRow(ctx, `SELECT user_id FROM users WHERE user_name = $1`, userName).Scan(&uid)
	if errors.Is(err, pgx.ErrNoRows) {
		return badInput("no user named " + userName)
	}
	if err != nil {
		return err
	}
	return setToggl(ctx, pool, box, uid, token, workspace)
}

// SetMyToggl is SetTogglAccount for the signed-in user, from the page.
func (l *Lib) SetMyToggl(ctx context.Context, box *secret.Box, token string, workspace int64) error {
	return setToggl(ctx, l.Pool, box, l.User, token, workspace)
}

// TogglConnected says whether the user has a token stored, and for which
// workspace (0: their default). The token itself never leaves the server.
func (l *Lib) TogglConnected(ctx context.Context) (bool, int64, error) {
	var wid *int64
	err := l.Pool.QueryRow(ctx, `
		SELECT toac_workspace_id FROM toggl_accounts WHERE user_id = $1`, l.User).Scan(&wid)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, 0, nil
	}
	if err != nil || wid == nil {
		return err == nil, 0, err
	}
	return true, *wid, nil
}

func setToggl(ctx context.Context, pool *pgxpool.Pool, box *secret.Box, uid int32, token string, workspace int64) error {
	token = strings.TrimSpace(token)
	if token == "" {
		_, err := pool.Exec(ctx, `DELETE FROM toggl_accounts WHERE user_id = $1`, uid)
		return err
	}
	sealed, err := box.Seal([]byte(token))
	if err != nil {
		return err
	}
	var wid *int64
	if workspace > 0 {
		wid = &workspace
	}
	_, err = pool.Exec(ctx, `
		INSERT INTO toggl_accounts (user_id, toac_token_sealed, toac_workspace_id) VALUES ($1, $2, $3)
		ON CONFLICT (user_id) DO UPDATE SET toac_token_sealed = EXCLUDED.toac_token_sealed,
		                                    toac_workspace_id = EXCLUDED.toac_workspace_id`,
		uid, sealed, wid)
	return err
}

// TogglEntry is what a timer started from a video is called and filed as.
type TogglEntry struct {
	Description string
	Project     string
	Tags        []string
}

// TogglEntryFor works out a video's entry from the user's rules: tags are
// the channel's own plus Youtube and Youtube:<Channel>, repeats in other
// capitals dropped. A name set for the channel wins; left blank, a video
// watched from a playlist is named after it, else after the channel.
func (l *Lib) TogglEntryFor(ctx context.Context, vid, source string) (TogglEntry, error) {
	v, err := l.Video(ctx, vid)
	if err != nil {
		return TogglEntry{}, err
	}
	// Trimmed to match how the sheet saved it: some channel names arrive
	// from YouTube with a trailing space.
	ch := strings.TrimSpace(v.Channel)
	if ch == "" {
		ch = "unknown"
	}
	rules, err := l.TogglMap(ctx)
	if err != nil {
		return TogglEntry{}, err
	}
	rule := rules[ch]
	var tags []string
	seen := map[string]bool{}
	for _, t := range append(strings.Split(rule.Tags, ","), "Youtube", "Youtube:"+ch) {
		t = strings.TrimSpace(t)
		if t != "" && !seen[strings.ToLower(t)] {
			seen[strings.ToLower(t)] = true
			tags = append(tags, t)
		}
	}
	name := rule.Name
	if name == "" && source != "" {
		err := l.Pool.QueryRow(ctx, `
			SELECT p.play_title FROM playlists p JOIN playlist_videos pv USING (play_id)
			 WHERE p.play_id = $1 AND pv.vid_id = $2`, source, vid).Scan(&name)
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return TogglEntry{}, err
		}
	}
	if name == "" {
		name = ch
	}
	return TogglEntry{Description: name, Project: rule.Project, Tags: tags}, nil
}
