-- +goose Up
-- +goose StatementBegin

-- The catalog: facts about YouTube, shared by every user and fetched once.
-- YouTube's own ids are the keys; they are stable and short, so a generated
-- surrogate would only add a lookup.

CREATE TABLE channels
(
    chan_id         VARCHAR(24)  PRIMARY KEY
        CONSTRAINT chan_id_valid CHECK (chan_id ~ '^UC[A-Za-z0-9_-]{22}$'),
    chan_handle     VARCHAR(64)  UNIQUE
        CONSTRAINT chan_handle_starts_with_at CHECK (chan_handle ~ '^@'),
    chan_name       VARCHAR(128) NOT NULL,
    chan_synced_at  TIMESTAMPTZ,
    chan_created_at TIMESTAMPTZ  NOT NULL DEFAULT now(),
    chan_updated_at TIMESTAMPTZ  NOT NULL DEFAULT now()
);

COMMENT ON TABLE channels IS 'Data
A YouTube channel, whether someone tracks it or it is only known as the
owner of a playlist. chan_synced_at is the last time its uploads were
catalogued; NULL means never.';

CREATE TRIGGER channels_set_updated_at BEFORE UPDATE ON channels
    FOR EACH ROW EXECUTE FUNCTION set_updated_at('chan_updated_at');

CREATE TABLE playlists
(
    play_id         VARCHAR(64)  PRIMARY KEY
        CONSTRAINT play_id_valid CHECK (play_id ~ '^[A-Za-z0-9_-]+$'),
    -- NULL when the owner could not be found out (YouTube hid it).
    chan_id          VARCHAR(24)  REFERENCES channels (chan_id),
    plse_code    VARCHAR(16)  NOT NULL DEFAULT 'PLAYLIST' REFERENCES playlist_sections (plse_code),
    play_title      VARCHAR(256) NOT NULL,
    play_synced_at  TIMESTAMPTZ,
    play_created_at TIMESTAMPTZ  NOT NULL DEFAULT now(),
    play_updated_at TIMESTAMPTZ  NOT NULL DEFAULT now()
);

CREATE INDEX ON playlists (chan_id);

COMMENT ON TABLE playlists IS 'Data
A YouTube playlist, course or live-stream list. The videos keep the order
its author chose (see playlist_videos).';

CREATE TRIGGER playlists_set_updated_at BEFORE UPDATE ON playlists
    FOR EACH ROW EXECUTE FUNCTION set_updated_at('play_updated_at');

CREATE TABLE videos
(
    vid_id                 VARCHAR(11)  PRIMARY KEY
        CONSTRAINT vid_id_valid CHECK (vid_id ~ '^[A-Za-z0-9_-]{11}$'),
    -- The uploader. NULL when the video was only ever seen inside someone
    -- else's playlist and no sync has said who made it.
    chan_id                VARCHAR(24)  REFERENCES channels (chan_id),
    vid_title              VARCHAR(512) NOT NULL,
    vid_description        TEXT,
    vid_duration_seconds   INTEGER
        CONSTRAINT vid_duration_not_negative CHECK (vid_duration_seconds >= 0),
    vid_published_on       DATE,
    -- TRUE when only the month of vid_published_on is known.
    vid_published_month_only BOOLEAN NOT NULL DEFAULT false,
    -- Where the video sits in its channel's uploads, 0 = newest. Dates are
    -- often month-precise, so this is what keeps a channel in true order.
    vid_channel_position   INTEGER
        CONSTRAINT vid_channel_position_not_negative CHECK (vid_channel_position >= 0),
    -- Set by sync when YouTube lists the slot but the video is private,
    -- deleted or blocked; cleared if it comes back.
    vid_unavailable_since  TIMESTAMPTZ,
    -- When chapters were last looked up. NULL: never asked. Set with no rows
    -- in video_chapters: the creator made none.
    vid_chapters_checked_at TIMESTAMPTZ,
    vid_created_at         TIMESTAMPTZ  NOT NULL DEFAULT now(),
    vid_updated_at         TIMESTAMPTZ  NOT NULL DEFAULT now()
);

CREATE INDEX idx_videos_channel_position ON videos (chan_id, vid_channel_position);

COMMENT ON TABLE videos IS 'Data
One YouTube video as catalogued: metadata only. Files live on each
device, never here; per-user state is in user_videos.';

CREATE TRIGGER videos_set_updated_at BEFORE UPDATE ON videos
    FOR EACH ROW EXECUTE FUNCTION set_updated_at('vid_updated_at');

CREATE TABLE playlist_videos
(
    play_id       VARCHAR(64) NOT NULL REFERENCES playlists (play_id) ON DELETE CASCADE,
    vid_id         VARCHAR(11) NOT NULL REFERENCES videos (vid_id) ON DELETE CASCADE,
    plvi_position INTEGER     NOT NULL
        CONSTRAINT plvi_position_not_negative CHECK (plvi_position >= 0),
    PRIMARY KEY (play_id, vid_id)
);

CREATE INDEX idx_playlist_videos_position ON playlist_videos (play_id, plvi_position);
CREATE INDEX ON playlist_videos (vid_id);

COMMENT ON TABLE playlist_videos IS 'Linking
Which videos a playlist holds, in the author''s order.';

CREATE TABLE video_chapters
(
    vid_id             VARCHAR(11)   NOT NULL REFERENCES videos (vid_id) ON DELETE CASCADE,
    vich_start_seconds NUMERIC(9, 3) NOT NULL
        CONSTRAINT vich_start_not_negative CHECK (vich_start_seconds >= 0),
    vich_end_seconds   NUMERIC(9, 3) NOT NULL,
    vich_title         VARCHAR(256)  NOT NULL,
    PRIMARY KEY (vid_id, vich_start_seconds),
    CONSTRAINT vich_ends_after_start CHECK (vich_end_seconds >= vich_start_seconds)
);

COMMENT ON TABLE video_chapters IS 'Data
The sections a creator marked in a video.';

-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP TABLE video_chapters;
DROP TABLE playlist_videos;
DROP TABLE videos;
DROP TABLE playlists;
DROP TABLE channels;
-- +goose StatementEnd
