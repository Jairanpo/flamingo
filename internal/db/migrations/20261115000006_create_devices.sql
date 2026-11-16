-- +goose Up
-- +goose StatementBegin

CREATE TABLE devices
(
    devi_id           INTEGER GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    devi_name         VARCHAR(64) NOT NULL UNIQUE
        CONSTRAINT devi_name_valid_chars CHECK (devi_name ~ '^[A-Za-z0-9_.-]+$'),
    devi_last_seen_at TIMESTAMPTZ,
    devi_created_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    devi_updated_at   TIMESTAMPTZ NOT NULL DEFAULT now()
);

COMMENT ON TABLE devices IS 'Data
A computer running the agent. Its downloads belong to the computer, so
everyone using it shares them.';

CREATE TRIGGER devices_set_updated_at BEFORE UPDATE ON devices
    FOR EACH ROW EXECUTE FUNCTION set_updated_at('devi_updated_at');

CREATE TABLE device_videos
(
    devi_id               INTEGER     NOT NULL REFERENCES devices (devi_id) ON DELETE CASCADE,
    vid_id               VARCHAR(11) NOT NULL REFERENCES videos (vid_id),
    dvvi_size_bytes    BIGINT
        CONSTRAINT dvvi_size_not_negative CHECK (dvvi_size_bytes >= 0),
    dvvi_downloaded_at TIMESTAMPTZ NOT NULL,
    -- Sound only (a podcast-style video fetched as .m4a); cards say so.
    dvvi_audio_only    BOOLEAN     NOT NULL DEFAULT false,
    PRIMARY KEY (devi_id, vid_id)
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
