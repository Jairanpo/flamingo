-- +goose Up
-- +goose StatementBegin

-- Pairing a computer from the app. The page asks for a short code, the
-- computer's agent trades it for its key. A code works once, for a few
-- minutes, and only its hash is kept, like a password.
CREATE TABLE pairing_codes
(
    paco_code_hash  BYTEA       PRIMARY KEY,
    -- Whoever asked for it: the computer is paired to this account.
    user_id         INTEGER     NOT NULL REFERENCES users (user_id) ON DELETE CASCADE,
    paco_expires_at TIMESTAMPTZ NOT NULL,
    paco_created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX pairing_codes_user_id_idx ON pairing_codes (user_id);

COMMENT ON TABLE pairing_codes IS 'Data
One-time codes that pair a computer''s agent to the account that asked for
them. Deleted when used; expired ones are swept when a new code is made.';

-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP TABLE pairing_codes;
-- +goose StatementEnd
