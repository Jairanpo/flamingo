-- +goose Up
-- +goose StatementBegin

-- One user's sources, whatever kind: a tracked channel, a tracked playlist,
-- or a channel's picks list. src_id is what the API calls the source: the
-- channel id, the playlist id, or 'PICKS:' plus the channel id.
CREATE FUNCTION user_sources(p_user_id INTEGER)
RETURNS TABLE (
    src_id        VARCHAR,
    src_kind      VARCHAR,   -- 'channel' | 'playlist'
    src_section   VARCHAR,   -- NULL | 'course' | 'live' | 'picks'
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

-- Which videos each of a user's sources holds, and where. A channel holds
-- the uploads catalogued from it (newest is position 0); a playlist, its
-- author's order; a picks list, the order the videos were picked.
CREATE FUNCTION user_source_videos(p_user_id INTEGER)
RETURNS TABLE (
    src_id       VARCHAR,
    vid_id       VARCHAR,
    src_position INTEGER
)
LANGUAGE sql STABLE AS $$
    SELECT v.chan_id, v.vid_id, v.vid_channel_position
      FROM user_channels uc
      JOIN videos v ON v.chan_id = uc.chan_id AND v.vid_channel_position IS NOT NULL
     WHERE uc.user_id = p_user_id
    UNION ALL
    SELECT pv.play_id, pv.vid_id, pv.plvi_position
      FROM user_playlists up
      JOIN playlist_videos pv USING (play_id)
     WHERE up.user_id = p_user_id
    UNION ALL
    SELECT 'PICKS:' || pk.chan_id, pkv.vid_id, pkv.upvi_position
      FROM user_picks pk
      JOIN user_pick_videos pkv USING (uspi_id)
     WHERE pk.user_id = p_user_id
$$;

-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP FUNCTION user_source_videos(INTEGER);
DROP FUNCTION user_sources(INTEGER);
-- +goose StatementEnd
