-- +goose Up
-- +goose StatementBegin

-- Finishing a source. The app calls pins "Focus"; once you have watched
-- what you came for you mark it complete, which takes it off Focus and
-- keeps a record of when you finished. A completed channel stays completed
-- when new uploads arrive. Moving it back to Focus clears the date, so a
-- source is in Focus, completed, or neither, never both.

ALTER TABLE user_channels
    ADD COLUMN usch_completed_at TIMESTAMPTZ,
    ADD CONSTRAINT usch_focus_or_completed
        CHECK (usch_pinned_at IS NULL OR usch_completed_at IS NULL);

ALTER TABLE user_playlists
    ADD COLUMN uspl_completed_at TIMESTAMPTZ,
    ADD CONSTRAINT uspl_focus_or_completed
        CHECK (uspl_pinned_at IS NULL OR uspl_completed_at IS NULL);

ALTER TABLE user_picks
    ADD COLUMN uspi_completed_at TIMESTAMPTZ,
    ADD CONSTRAINT uspi_focus_or_completed
        CHECK (uspi_pinned_at IS NULL OR uspi_completed_at IS NULL);

-- user_sources gains src_completed_at; a new result column means a new
-- function, so drop and recreate it.
DROP FUNCTION user_sources(INTEGER);

CREATE FUNCTION user_sources(p_user_id INTEGER)
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

-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP FUNCTION user_sources(INTEGER);

CREATE FUNCTION user_sources(p_user_id INTEGER)
RETURNS TABLE (
    src_id        VARCHAR,
    src_kind      VARCHAR,
    src_section   VARCHAR,
    src_title     VARCHAR,
    chan_id       VARCHAR,
    chan_name     VARCHAR,
    chan_handle   VARCHAR,
    src_shelved   BOOLEAN,
    src_pinned_at TIMESTAMPTZ,
    sort_code    VARCHAR
)
LANGUAGE sql STABLE AS $$
    SELECT c.chan_id, 'channel', NULL, c.chan_name,
           c.chan_id, c.chan_name, c.chan_handle,
           uc.usch_shelved, uc.usch_pinned_at, uc.sort_code
      FROM user_channels uc
      JOIN channels c USING (chan_id)
     WHERE uc.user_id = p_user_id
    UNION ALL
    SELECT p.play_id, 'playlist',
           CASE p.plse_code WHEN 'PLAYLIST' THEN NULL ELSE lower(p.plse_code) END,
           p.play_title, c.chan_id, c.chan_name, c.chan_handle,
           up.uspl_shelved, up.uspl_pinned_at, up.sort_code
      FROM user_playlists up
      JOIN playlists p USING (play_id)
      LEFT JOIN channels c ON c.chan_id = p.chan_id
     WHERE up.user_id = p_user_id
    UNION ALL
    SELECT 'PICKS:' || c.chan_id, 'playlist', 'picks', 'Picked videos',
           c.chan_id, c.chan_name, c.chan_handle,
           pk.uspi_shelved, pk.uspi_pinned_at, pk.sort_code
      FROM user_picks pk
      JOIN channels c USING (chan_id)
     WHERE pk.user_id = p_user_id
$$;

ALTER TABLE user_picks     DROP COLUMN uspi_completed_at;
ALTER TABLE user_playlists DROP COLUMN uspl_completed_at;
ALTER TABLE user_channels  DROP COLUMN usch_completed_at;
-- +goose StatementEnd
