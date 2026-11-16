-- +goose Up
-- +goose StatementBegin

CREATE TABLE toggl_accounts
(
    user_id              INTEGER     PRIMARY KEY REFERENCES users (user_id) ON DELETE CASCADE,
    -- The API token, sealed with the server's secret key (AES-256-GCM,
    -- nonce first). Never stored in the clear.
    toac_token_sealed    BYTEA       NOT NULL,
    -- The workspace to file entries under. NULL: the account's default.
    toac_workspace_id    BIGINT
        CONSTRAINT toac_workspace_positive CHECK (toac_workspace_id > 0),
    toac_created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    toac_updated_at      TIMESTAMPTZ NOT NULL DEFAULT now()
);

COMMENT ON TABLE toggl_accounts IS 'Subset
A user''s Toggl Track connection. Without a row the timer button stays
hidden for them.';

CREATE TRIGGER toggl_accounts_set_updated_at BEFORE UPDATE ON toggl_accounts
    FOR EACH ROW EXECUTE FUNCTION set_updated_at('toac_updated_at');

-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP TABLE toggl_accounts;
-- +goose StatementEnd
