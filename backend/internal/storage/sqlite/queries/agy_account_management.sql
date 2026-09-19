-- name: InsertAgyAccountSwitch :execrows
INSERT INTO agy_account_switches (
	 id, source_kind, source_account_id, target_account_id, idempotency_key,
	 phase, failure_code,
	 created_at, updated_at
) VALUES (?, ?, ?, ?, ?, ?, '', ?, ?)
ON CONFLICT DO NOTHING;

-- name: GetAgyAccountSwitch :one
SELECT id, source_account_id, target_account_id, idempotency_key,
	   phase, failure_code,
	   credentials_committed_at, created_at, updated_at, completed_at, source_kind
FROM agy_account_switches WHERE id = ?;

-- name: GetAgyAccountSwitchByIdempotency :one
SELECT id, source_account_id, target_account_id, idempotency_key,
	   phase, failure_code,
	   credentials_committed_at, created_at, updated_at, completed_at, source_kind
FROM agy_account_switches WHERE idempotency_key = ?;

-- name: GetActiveAgyAccountSwitch :one
SELECT id, source_account_id, target_account_id, idempotency_key,
	   phase, failure_code,
	   credentials_committed_at, created_at, updated_at, completed_at, source_kind
FROM agy_account_switches
WHERE phase NOT IN ('completed', 'failed')
ORDER BY created_at LIMIT 1;

-- name: UpdateAgyAccountSwitchPhase :execrows
UPDATE agy_account_switches
SET phase = sqlc.arg(next_phase), failure_code = sqlc.arg(failure_code),
    credentials_committed_at = sqlc.narg(credentials_committed_at),
    updated_at = sqlc.arg(updated_at), completed_at = sqlc.narg(completed_at)
WHERE id = sqlc.arg(id) AND phase = sqlc.arg(expected_phase);
