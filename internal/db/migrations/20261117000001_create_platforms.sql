-- +goose Up
-- +goose StatementBegin

-- Where downloads live. Each operating system has one default folder, set in
-- the app; a computer may point somewhere else. The agent on that computer
-- reads the answer from here, so nobody edits files on each machine.
CREATE TABLE platforms
(
    plat_code       VARCHAR(16)  PRIMARY KEY
        CONSTRAINT plat_code_uppercase_only CHECK (plat_code ~ '^[A-Z_]+$'),
    plat_name       VARCHAR(32)  NOT NULL UNIQUE,
    -- "~" is the home folder of whoever runs the agent.
    plat_media_dir  VARCHAR(512) NOT NULL
        CONSTRAINT plat_media_dir_not_blank CHECK (btrim(plat_media_dir) <> ''),
    plat_updated_at TIMESTAMPTZ  NOT NULL DEFAULT now()
);

COMMENT ON TABLE platforms IS 'Validation
The operating systems an agent runs on, each with the folder its downloads go
to unless the computer has its own (devices.devi_media_dir).';

CREATE TRIGGER platforms_set_updated_at BEFORE UPDATE ON platforms
    FOR EACH ROW EXECUTE FUNCTION set_updated_at('plat_updated_at');

INSERT INTO platforms (plat_code, plat_name, plat_media_dir)
VALUES ('LINUX', 'Linux', '~/Videos/Flamingo'),
       ('MACOS', 'macOS', '~/Movies/Flamingo'),
       ('WINDOWS', 'Windows', '~/Videos/Flamingo');

ALTER TABLE devices
    -- NULL until the computer's agent first says what it runs on.
    ADD COLUMN plat_code       VARCHAR(16) REFERENCES platforms (plat_code) ON DELETE RESTRICT,
    -- NULL: the platform's folder.
    ADD COLUMN devi_media_dir  VARCHAR(512)
        CONSTRAINT devi_media_dir_not_blank CHECK (btrim(devi_media_dir) <> ''),
    -- The agent's key (sha256 of it): pairing hands the agent the key once.
    ADD COLUMN devi_token_hash BYTEA UNIQUE,
    -- Who paired it; the computer stays when that account goes.
    ADD COLUMN user_id         INTEGER REFERENCES users (user_id) ON DELETE SET NULL;

COMMENT ON TABLE device_videos IS 'Linking
Which videos are on which computer, as that computer''s agent last reported.
Only the fact, never the path: the agent finds each file by the [id] in its
name, inside the folder platforms/devices name.';

-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
ALTER TABLE devices
    DROP COLUMN user_id,
    DROP COLUMN devi_token_hash,
    DROP COLUMN devi_media_dir,
    DROP COLUMN plat_code;
DROP TABLE platforms;
COMMENT ON TABLE device_videos IS 'Linking
Which videos are on which computer. Only the fact, never the path: the
file''s location is the agent''s business.';
-- +goose StatementEnd
