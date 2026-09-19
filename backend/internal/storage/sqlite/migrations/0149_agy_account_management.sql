-- Summary: durable Antigravity account switches, the shape Codex switches
-- have after 0146: the device credential file is the active-account
-- authority, so there is no active-pointer table and no session journal.
-- +goose Up
CREATE TABLE agy_account_switches (
    id TEXT PRIMARY KEY,
    source_account_id TEXT NOT NULL,
    target_account_id TEXT NOT NULL,
    idempotency_key TEXT NOT NULL UNIQUE,
    phase TEXT NOT NULL CHECK (phase IN (
        'requested', 'checkpointing_source', 'activating_target',
        'recovery_required', 'completed', 'failed'
    )),
    failure_code TEXT NOT NULL DEFAULT '',
    credentials_committed_at TIMESTAMP,
    created_at TIMESTAMP NOT NULL,
    updated_at TIMESTAMP NOT NULL,
    completed_at TIMESTAMP,
    source_kind TEXT NOT NULL DEFAULT 'managed'
        CHECK (source_kind IN ('managed', 'device', 'none'))
);

CREATE UNIQUE INDEX idx_agy_account_switches_one_active
ON agy_account_switches((1))
WHERE phase NOT IN ('completed', 'failed');

-- +goose Down
DROP INDEX IF EXISTS idx_agy_account_switches_one_active;
DROP TABLE IF EXISTS agy_account_switches;
