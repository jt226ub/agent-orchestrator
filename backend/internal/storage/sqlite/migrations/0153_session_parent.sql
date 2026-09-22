-- +goose Up
ALTER TABLE sessions ADD COLUMN parent_session_id TEXT NOT NULL DEFAULT '';

-- +goose Down
ALTER TABLE sessions DROP COLUMN parent_session_id;
