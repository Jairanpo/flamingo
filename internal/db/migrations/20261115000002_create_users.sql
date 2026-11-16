-- +goose Up
-- +goose StatementBegin

CREATE TABLE users
(
    user_id            INTEGER GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    user_role          VARCHAR(16)  NOT NULL DEFAULT 'MEMBER' REFERENCES user_roles (user_role),
    user_name          VARCHAR(32)  NOT NULL UNIQUE
        CONSTRAINT user_name_valid_chars CHECK (user_name ~ '^[a-z0-9_.-]{2,32}$'),
    user_display_name  VARCHAR(64)  NOT NULL,
    -- argon2id, PHC string format. NULL until a password is set: an imported
    -- user exists before anyone has chosen one, and cannot log in until then.
    user_password_hash VARCHAR(256),
    sort_order         VARCHAR(16)  NOT NULL DEFAULT 'DEFAULT' REFERENCES sort_orders (sort_order),
    user_created_at    TIMESTAMPTZ  NOT NULL DEFAULT now(),
    user_updated_at    TIMESTAMPTZ  NOT NULL DEFAULT now()
);

COMMENT ON TABLE users IS 'Data
A person in the house. Everything they track, watch and set up hangs off
user_id; sort_order is their default for views with no order of their own.';

CREATE TRIGGER users_set_updated_at BEFORE UPDATE ON users
    FOR EACH ROW EXECUTE FUNCTION set_updated_at('user_updated_at');

-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP TABLE users;
-- +goose StatementEnd
