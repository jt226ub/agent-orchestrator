-- +goose Up
ALTER TABLE sessions ADD COLUMN session_profile TEXT NOT NULL DEFAULT '';

-- +goose Down
ALTER TABLE sessions DROP COLUMN session_profile;
