-- +goose Up
-- +goose StatementBegin

-- Each user's library: what they track, how they arranged it, and where they
-- are in each video. Everything here is private to user_id.
--
-- Pins (the priorities dashboard) can sit on a channel, a playlist or a picks
-- list, so each of those tables carries its own *_pinned_at; the 5-pin cap
-- counts across all three and is enforced by the application.

CREATE TABLE user_channels
(
    user_id          INTEGER     NOT NULL REFERENCES users (user_id) ON DELETE CASCADE,
    chan_id          VARCHAR(24) NOT NULL REFERENCES channels (chan_id),
    usch_shelved    BOOLEAN     NOT NULL DEFAULT false,
    usch_pinned_at  TIMESTAMPTZ,
    sort_code       VARCHAR(16) REFERENCES sort_orders (sort_code),
    usch_created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (user_id, chan_id)
);

COMMENT ON TABLE user_channels IS 'Linking
A channel a user tracks. Shelved: kept but out of the way. sort_code NULL
means the user''s default.';

CREATE TABLE user_playlists
(
    user_id           INTEGER     NOT NULL REFERENCES users (user_id) ON DELETE CASCADE,
    play_id          VARCHAR(64) NOT NULL REFERENCES playlists (play_id),
    uspl_shelved    BOOLEAN     NOT NULL DEFAULT false,
    uspl_pinned_at  TIMESTAMPTZ,
    sort_code        VARCHAR(16) REFERENCES sort_orders (sort_code),
    uspl_created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (user_id, play_id)
);

COMMENT ON TABLE user_playlists IS 'Linking
A playlist or course a user tracks, with the same shelf/pin/sort choices
as user_channels.';

CREATE TABLE user_picks
(
    uspi_id         INTEGER GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    user_id          INTEGER     NOT NULL REFERENCES users (user_id) ON DELETE CASCADE,
    chan_id          VARCHAR(24) NOT NULL REFERENCES channels (chan_id),
    uspi_shelved    BOOLEAN     NOT NULL DEFAULT false,
    uspi_pinned_at  TIMESTAMPTZ,
    sort_code       VARCHAR(16) REFERENCES sort_orders (sort_code),
    uspi_created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT user_picks_one_per_channel UNIQUE (user_id, chan_id)
);

COMMENT ON TABLE user_picks IS 'Data
Videos a user hand-picked from one channel without tracking all of it,
shown as that channel''s "Picked videos" list.';

CREATE TABLE user_pick_videos
(
    uspi_id       INTEGER     NOT NULL REFERENCES user_picks (uspi_id) ON DELETE CASCADE,
    vid_id         VARCHAR(11) NOT NULL REFERENCES videos (vid_id),
    upvi_position INTEGER     NOT NULL
        CONSTRAINT upvi_position_not_negative CHECK (upvi_position >= 0),
    PRIMARY KEY (uspi_id, vid_id)
);

COMMENT ON TABLE user_pick_videos IS 'Linking
The videos in a picks list, in the order they were picked.';

CREATE TABLE user_videos
(
    user_id                INTEGER       NOT NULL REFERENCES users (user_id) ON DELETE CASCADE,
    vid_id                 VARCHAR(11)   NOT NULL REFERENCES videos (vid_id),
    usvi_watched           BOOLEAN       NOT NULL DEFAULT false,
    -- NULL when watched before times were recorded, or not watched.
    usvi_watched_at        TIMESTAMPTZ,
    usvi_progress_seconds  NUMERIC(9, 3) NOT NULL DEFAULT 0
        CONSTRAINT usvi_progress_not_negative CHECK (usvi_progress_seconds >= 0),
    usvi_starred           BOOLEAN       NOT NULL DEFAULT false,
    -- Hidden by this user from every view. (Videos YouTube made unavailable
    -- are hidden for everyone by videos.vid_unavailable_since instead.)
    usvi_hidden            BOOLEAN       NOT NULL DEFAULT false,
    usvi_created_at        TIMESTAMPTZ   NOT NULL DEFAULT now(),
    usvi_updated_at        TIMESTAMPTZ   NOT NULL DEFAULT now(),
    PRIMARY KEY (user_id, vid_id),
    CONSTRAINT usvi_watched_at_only_when_watched CHECK (usvi_watched OR usvi_watched_at IS NULL)
);

COMMENT ON TABLE user_videos IS 'Linking
One user''s state for one video. A row exists only once there is something
to remember; no row means unwatched, unstarred, at 0:00.';

CREATE TRIGGER user_videos_set_updated_at BEFORE UPDATE ON user_videos
    FOR EACH ROW EXECUTE FUNCTION set_updated_at('usvi_updated_at');

CREATE TABLE collections
(
    coll_id         INTEGER GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    user_id         INTEGER     NOT NULL REFERENCES users (user_id) ON DELETE CASCADE,
    coll_name       VARCHAR(64) NOT NULL,
    sort_code      VARCHAR(16) REFERENCES sort_orders (sort_code),
    coll_created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    coll_updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE UNIQUE INDEX idx_collections_user_name ON collections (user_id, lower(coll_name));

COMMENT ON TABLE collections IS 'Data
A list a user builds by hand, in their own order.';

CREATE TRIGGER collections_set_updated_at BEFORE UPDATE ON collections
    FOR EACH ROW EXECUTE FUNCTION set_updated_at('coll_updated_at');

CREATE TABLE collection_videos
(
    coll_id          INTEGER     NOT NULL REFERENCES collections (coll_id) ON DELETE CASCADE,
    vid_id           VARCHAR(11) NOT NULL REFERENCES videos (vid_id),
    covi_rank     INTEGER     NOT NULL,
    covi_added_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (coll_id, vid_id)
);

CREATE INDEX idx_collection_videos_rank ON collection_videos (coll_id, covi_rank);

COMMENT ON TABLE collection_videos IS 'Linking
The videos in a collection, ordered by covi_rank.';

CREATE TABLE marks
(
    mark_id         INTEGER GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    user_id         INTEGER       NOT NULL REFERENCES users (user_id) ON DELETE CASCADE,
    vid_id          VARCHAR(11)   NOT NULL REFERENCES videos (vid_id),
    mark_seconds    NUMERIC(9, 3) NOT NULL
        CONSTRAINT mark_seconds_not_negative CHECK (mark_seconds >= 0),
    mark_note       TEXT,
    mark_created_at TIMESTAMPTZ   NOT NULL DEFAULT now()
);

CREATE INDEX idx_marks_user_video ON marks (user_id, vid_id, mark_seconds);

COMMENT ON TABLE marks IS 'Data
A spot a user marked in a video, with an optional note.';

CREATE TABLE topics
(
    topi_id         INTEGER GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    user_id          INTEGER     NOT NULL REFERENCES users (user_id) ON DELETE CASCADE,
    topi_name       VARCHAR(64) NOT NULL,
    topi_created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    -- lets channel_topics require the topic to be the same user's
    CONSTRAINT topics_user_topic_unique UNIQUE (user_id, topi_id)
);

CREATE UNIQUE INDEX idx_topics_user_name ON topics (user_id, lower(topi_name));

COMMENT ON TABLE topics IS 'Data
A shelf a user sorts creators onto in the sidebar (Japanese, Racing...).';

CREATE TABLE channel_topics
(
    user_id  INTEGER     NOT NULL REFERENCES users (user_id) ON DELETE CASCADE,
    chan_id  VARCHAR(24) NOT NULL REFERENCES channels (chan_id),
    topi_id INTEGER     NOT NULL,
    PRIMARY KEY (user_id, chan_id),
    FOREIGN KEY (user_id, topi_id) REFERENCES topics (user_id, topi_id) ON DELETE CASCADE
);

COMMENT ON TABLE channel_topics IS 'Linking
Which topic a creator sits under, for one user. One topic per creator.';

-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP TABLE channel_topics;
DROP TABLE topics;
DROP TABLE marks;
DROP TABLE collection_videos;
DROP TABLE collections;
DROP TABLE user_videos;
DROP TABLE user_pick_videos;
DROP TABLE user_picks;
DROP TABLE user_playlists;
DROP TABLE user_channels;
-- +goose StatementEnd
