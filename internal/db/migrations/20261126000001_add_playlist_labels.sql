-- +goose Up
-- +goose StatementBegin

-- A playlist's name in Flamingo, kept apart from its name on YouTube.
-- playlists.play_title stays whatever YouTube calls it now (a sync keeps it
-- current); each user's label is theirs. When YouTube renames a playlist
-- the label stays put until the user says yes or no, and uspl_seen_title
-- remembers which YouTube title they last decided on, so they are asked
-- once per rename.
ALTER TABLE user_playlists
    ADD COLUMN uspl_label      VARCHAR(256),
    ADD COLUMN uspl_seen_title VARCHAR(256);

COMMENT ON COLUMN user_playlists.uspl_label IS
'The name Flamingo shows this user; NULL means the YouTube title.';
COMMENT ON COLUMN user_playlists.uspl_seen_title IS
'The YouTube title when the user last chose a name; when play_title moves
away from it, the page asks whether to take the new one.';

UPDATE user_playlists up
   SET uspl_label = p.play_title, uspl_seen_title = p.play_title
  FROM playlists p
 WHERE p.play_id = up.play_id;

-- Same columns as before: src_title is now the label.
CREATE OR REPLACE FUNCTION user_sources(p_user_id INTEGER)
RETURNS TABLE (
    src_id           VARCHAR,
    src_kind         VARCHAR,   -- 'channel' | 'playlist'
    src_section      VARCHAR,   -- NULL | 'course' | 'live' | 'picks'
    src_title        VARCHAR,
    chan_id          VARCHAR,
    chan_name        VARCHAR,
    chan_handle      VARCHAR,
    src_shelved      BOOLEAN,
    src_pinned_at    TIMESTAMPTZ,
    sort_code        VARCHAR,
    src_completed_at TIMESTAMPTZ
)
LANGUAGE sql STABLE AS $$
    SELECT c.chan_id, 'channel', NULL, c.chan_name,
           c.chan_id, c.chan_name, c.chan_handle,
           uc.usch_shelved, uc.usch_pinned_at, uc.sort_code, uc.usch_completed_at
      FROM user_channels uc
      JOIN channels c USING (chan_id)
     WHERE uc.user_id = p_user_id
    UNION ALL
    SELECT p.play_id, 'playlist',
           CASE p.plse_code WHEN 'PLAYLIST' THEN NULL ELSE lower(p.plse_code) END,
           COALESCE(up.uspl_label, p.play_title), c.chan_id, c.chan_name, c.chan_handle,
           up.uspl_shelved, up.uspl_pinned_at, up.sort_code, up.uspl_completed_at
      FROM user_playlists up
      JOIN playlists p USING (play_id)
      LEFT JOIN channels c ON c.chan_id = p.chan_id
     WHERE up.user_id = p_user_id
    UNION ALL
    SELECT 'PICKS:' || c.chan_id, 'playlist', 'picks', 'Picked videos',
           c.chan_id, c.chan_name, c.chan_handle,
           pk.uspi_shelved, pk.uspi_pinned_at, pk.sort_code, pk.uspi_completed_at
      FROM user_picks pk
      JOIN channels c USING (chan_id)
     WHERE pk.user_id = p_user_id
$$;

-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
CREATE OR REPLACE FUNCTION user_sources(p_user_id INTEGER)
RETURNS TABLE (
    src_id           VARCHAR,
    src_kind         VARCHAR,   -- 'channel' | 'playlist'
    src_section      VARCHAR,   -- NULL | 'course' | 'live' | 'picks'
    src_title        VARCHAR,
    chan_id          VARCHAR,
    chan_name        VARCHAR,
    chan_handle      VARCHAR,
    src_shelved      BOOLEAN,
    src_pinned_at    TIMESTAMPTZ,
    sort_code        VARCHAR,
    src_completed_at TIMESTAMPTZ
)
LANGUAGE sql STABLE AS $$
    SELECT c.chan_id, 'channel', NULL, c.chan_name,
           c.chan_id, c.chan_name, c.chan_handle,
           uc.usch_shelved, uc.usch_pinned_at, uc.sort_code, uc.usch_completed_at
      FROM user_channels uc
      JOIN channels c USING (chan_id)
     WHERE uc.user_id = p_user_id
    UNION ALL
    SELECT p.play_id, 'playlist',
           CASE p.plse_code WHEN 'PLAYLIST' THEN NULL ELSE lower(p.plse_code) END,
           p.play_title, c.chan_id, c.chan_name, c.chan_handle,
           up.uspl_shelved, up.uspl_pinned_at, up.sort_code, up.uspl_completed_at
      FROM user_playlists up
      JOIN playlists p USING (play_id)
      LEFT JOIN channels c ON c.chan_id = p.chan_id
     WHERE up.user_id = p_user_id
    UNION ALL
    SELECT 'PICKS:' || c.chan_id, 'playlist', 'picks', 'Picked videos',
           c.chan_id, c.chan_name, c.chan_handle,
           pk.uspi_shelved, pk.uspi_pinned_at, pk.sort_code, pk.uspi_completed_at
      FROM user_picks pk
      JOIN channels c USING (chan_id)
     WHERE pk.user_id = p_user_id
$$;

ALTER TABLE user_playlists
    DROP COLUMN uspl_label,
    DROP COLUMN uspl_seen_title;
-- +goose StatementEnd
