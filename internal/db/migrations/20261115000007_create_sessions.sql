-- +goose Up
-- +goose StatementBegin

CREATE TABLE sessions
(
    -- SHA-256 of the token in the browser's cookie. The token itself is
    -- never stored, so a copy of this table can't be used to log in.
    sess_id           BYTEA        PRIMARY KEY
        CONSTRAINT sess_id_is_sha256 CHECK (length(sess_id) = 32),
    user_id           INTEGER      NOT NULL REFERENCES users (user_id) ON DELETE CASCADE,
    sess_user_agent   VARCHAR(256),
    sess_created_at   TIMESTAMPTZ  NOT NULL DEFAULT now(),
    sess_last_seen_at TIMESTAMPTZ  NOT NULL DEFAULT now(),
    sess_expires_at   TIMESTAMPTZ  NOT NULL,
    CONSTRAINT sess_expires_after_creation CHECK (sess_expires_at > sess_created_at)
);

CREATE INDEX ON sessions (user_id);
CREATE INDEX ON sessions (sess_expires_at);

COMMENT ON TABLE sessions IS 'Data
A signed-in browser. Deleting the row signs it out; it also ends on its
own at sess_expires_at, which moves forward while it is in use.';

-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP TABLE sessions;
-- +goose StatementEnd
