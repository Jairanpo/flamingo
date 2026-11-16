-- +goose Up
-- +goose StatementBegin

CREATE TABLE tags
(
    tag_id         INTEGER GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    user_id        INTEGER     NOT NULL REFERENCES users (user_id) ON DELETE CASCADE,
    -- Kept exactly as typed (Toggl tags are Title Case by habit);
    -- uniqueness ignores case.
    tag_name       VARCHAR(64) NOT NULL,
    tag_created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    -- lets toggl_channel_rule_tags require the tag to be the same user's
    CONSTRAINT tags_user_tag_unique UNIQUE (user_id, tag_id)
);

CREATE UNIQUE INDEX idx_tags_user_name ON tags (user_id, lower(tag_name));

COMMENT ON TABLE tags IS 'Data
A Toggl Track tag a user applies to time entries.';

CREATE TABLE toggl_channel_rules
(
    user_id           INTEGER      NOT NULL REFERENCES users (user_id) ON DELETE CASCADE,
    chan_id           VARCHAR(24)  NOT NULL REFERENCES channels (chan_id),
    -- What to call the time entry. NULL: the playlist's title, else the
    -- channel's name.
    tgrule_entry_name VARCHAR(256),
    -- The Toggl project, by name. NULL: no project.
    tgrule_project    VARCHAR(128),
    tgrule_created_at TIMESTAMPTZ  NOT NULL DEFAULT now(),
    tgrule_updated_at TIMESTAMPTZ  NOT NULL DEFAULT now(),
    PRIMARY KEY (user_id, chan_id)
);

COMMENT ON TABLE toggl_channel_rules IS 'Data
How a channel''s videos are tracked in one user''s Toggl Track. The
playlist layer arrives with phase 7.';

CREATE TRIGGER toggl_channel_rules_set_updated_at BEFORE UPDATE ON toggl_channel_rules
    FOR EACH ROW EXECUTE FUNCTION set_updated_at('tgrule_updated_at');

CREATE TABLE toggl_channel_rule_tags
(
    user_id INTEGER     NOT NULL,
    chan_id VARCHAR(24) NOT NULL,
    tag_id  INTEGER     NOT NULL,
    PRIMARY KEY (user_id, chan_id, tag_id),
    FOREIGN KEY (user_id, chan_id) REFERENCES toggl_channel_rules (user_id, chan_id) ON DELETE CASCADE,
    FOREIGN KEY (user_id, tag_id) REFERENCES tags (user_id, tag_id) ON DELETE CASCADE
);

COMMENT ON TABLE toggl_channel_rule_tags IS 'Linking
The tags a channel rule adds to every entry, besides the automatic
Youtube and Youtube:<Channel>.';

-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP TABLE toggl_channel_rule_tags;
DROP TABLE toggl_channel_rules;
DROP TABLE tags;
-- +goose StatementEnd
