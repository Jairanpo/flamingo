-- The first migration only proves the pipeline end to end: one row that says
-- when this database was set up. The real schema arrives in phase 2.

-- +goose Up
CREATE TABLE instance (
    id          smallint PRIMARY KEY DEFAULT 1 CHECK (id = 1),
    created_at  timestamptz NOT NULL DEFAULT now()
);
INSERT INTO instance DEFAULT VALUES;

-- +goose Down
DROP TABLE instance;
