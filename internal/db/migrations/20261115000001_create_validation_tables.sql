-- +goose Up
-- +goose StatementBegin

-- Every Data table keeps a <prefix>_updated_at column current through this
-- one function; the trigger passes the column's name as its argument.
CREATE FUNCTION set_updated_at() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
    NEW := jsonb_populate_record(NEW, jsonb_build_object(TG_ARGV[0], now()));
    RETURN NEW;
END
$$;

CREATE TABLE user_roles
(
    usro_code VARCHAR(16) PRIMARY KEY
        CONSTRAINT usro_code_uppercase_only CHECK (usro_code ~ '^[A-Z_]+$')
);

COMMENT ON TABLE user_roles IS 'Validation
What a user may do: an ADMIN also manages users and devices.';

INSERT INTO user_roles (usro_code)
VALUES ('ADMIN'), ('MEMBER');

CREATE TABLE playlist_sections
(
    plse_code VARCHAR(16) PRIMARY KEY
        CONSTRAINT plse_code_uppercase_only CHECK (plse_code ~ '^[A-Z_]+$'),
    plse_rank  SMALLINT NOT NULL UNIQUE
);

COMMENT ON TABLE playlist_sections IS 'Validation
The shelf a playlist sits on under its channel; plse_rank orders the
shelves (courses first).';

INSERT INTO playlist_sections (plse_code, plse_rank)
VALUES ('COURSE', 1), ('LIVE', 2), ('PLAYLIST', 3);

CREATE TABLE sort_orders
(
    sort_code VARCHAR(16) PRIMARY KEY
        CONSTRAINT sort_code_uppercase_only CHECK (sort_code ~ '^[A-Z_]+$')
);

COMMENT ON TABLE sort_orders IS 'Validation
The ways a list of videos can be ordered. DEFAULT is the view''s own order:
newest first for a channel, the author''s order for a playlist.';

INSERT INTO sort_orders (sort_code)
VALUES ('DEFAULT'), ('NEWEST'), ('OLDEST'), ('ADDED'), ('TITLE'), ('LONGEST'), ('SHORTEST');

-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP TABLE sort_orders;
DROP TABLE playlist_sections;
DROP TABLE user_roles;
DROP FUNCTION set_updated_at();
-- +goose StatementEnd
