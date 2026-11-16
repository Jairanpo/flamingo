-- +goose Up
-- +goose StatementBegin

CREATE TABLE devices
(
    dev_id           INTEGER GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    dev_name         VARCHAR(64) NOT NULL UNIQUE
        CONSTRAINT dev_name_valid_chars CHECK (dev_name ~ '^[A-Za-z0-9_.-]+$'),
    dev_last_seen_at TIMESTAMPTZ,
    dev_created_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    dev_updated_at   TIMESTAMPTZ NOT NULL DEFAULT now()
);

COMMENT ON TABLE devices IS 'Data
A computer running the agent. Its downloads belong to the computer, so
everyone using it shares them.';

CREATE TRIGGER devices_set_updated_at BEFORE UPDATE ON devices
    FOR EACH ROW EXECUTE FUNCTION set_updated_at('dev_updated_at');

CREATE TABLE device_videos
(
    dev_id               INTEGER     NOT NULL REFERENCES devices (dev_id) ON DELETE CASCADE,
    vid_id               VARCHAR(11) NOT NULL REFERENCES videos (vid_id),
    devvid_size_bytes    BIGINT
        CONSTRAINT devvid_size_not_negative CHECK (devvid_size_bytes >= 0),
    devvid_downloaded_at TIMESTAMPTZ NOT NULL,
    PRIMARY KEY (dev_id, vid_id)
);

CREATE INDEX ON device_videos (vid_id);

COMMENT ON TABLE device_videos IS 'Linking
Which videos are on which computer. Only the fact, never the path: the
file''s location is the agent''s business.';

-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP TABLE device_videos;
DROP TABLE devices;
-- +goose StatementEnd
