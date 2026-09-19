package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
	"github.com/aoagents/agent-orchestrator/backend/internal/storage/sqlite/gen"
)

var _ ports.AgyAccountSwitchStore = (*Store)(nil)

// CreateAgyAccountSwitch inserts or returns an idempotent global switch.
func (s *Store) CreateAgyAccountSwitch(ctx context.Context, rec domain.AgyAccountSwitch) (domain.AgyAccountSwitch, bool, error) {
	if rec.SourceKind == "" {
		rec.SourceKind = domain.AgyAccountSwitchSourceManaged
	}
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	var n int64
	err := s.inTx(ctx, "create Agy account switch", func(q *gen.Queries) error {
		var insertErr error
		n, insertErr = q.InsertAgyAccountSwitch(ctx, gen.InsertAgyAccountSwitchParams{
			ID: rec.ID, SourceKind: string(rec.SourceKind), SourceAccountID: rec.SourceAccountID, TargetAccountID: rec.TargetAccountID,
			IdempotencyKey: rec.IdempotencyKey,
			Phase:          string(rec.Phase),
			CreatedAt:      rec.CreatedAt.UTC(), UpdatedAt: rec.UpdatedAt.UTC(),
		})
		return insertErr
	})
	if err != nil {
		return domain.AgyAccountSwitch{}, false, fmt.Errorf("create Agy account switch %s: %w", rec.ID, err)
	}
	if n > 0 {
		return rec, true, nil
	}
	if row, readErr := s.qw.GetAgyAccountSwitchByIdempotency(ctx, rec.IdempotencyKey); readErr == nil {
		existing := agyAccountSwitchFromGen(row)
		if existing.TargetAccountID == rec.TargetAccountID {
			return existing, false, nil
		}
		return existing, false, ports.ErrAgyAccountSwitchIdempotencyConflict
	} else if !errors.Is(readErr, sql.ErrNoRows) {
		return domain.AgyAccountSwitch{}, false, readErr
	}
	if row, readErr := s.qw.GetActiveAgyAccountSwitch(ctx); readErr == nil {
		return agyAccountSwitchFromGen(row), false, ports.ErrAgyAccountSwitchInProgress
	} else if !errors.Is(readErr, sql.ErrNoRows) {
		return domain.AgyAccountSwitch{}, false, readErr
	}
	return domain.AgyAccountSwitch{}, false, ports.ErrAgyAccountSwitchIdempotencyConflict
}

// GetAgyAccountSwitch reads one switch by ID.
func (s *Store) GetAgyAccountSwitch(ctx context.Context, id string) (domain.AgyAccountSwitch, bool, error) {
	row, err := s.qr.GetAgyAccountSwitch(ctx, id)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.AgyAccountSwitch{}, false, nil
	}
	if err != nil {
		return domain.AgyAccountSwitch{}, false, fmt.Errorf("get Agy account switch %s: %w", id, err)
	}
	return agyAccountSwitchFromGen(row), true, nil
}

// GetAgyAccountSwitchByIdempotency reads one switch by idempotency key.
func (s *Store) GetAgyAccountSwitchByIdempotency(ctx context.Context, key string) (domain.AgyAccountSwitch, bool, error) {
	row, err := s.qr.GetAgyAccountSwitchByIdempotency(ctx, key)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.AgyAccountSwitch{}, false, nil
	}
	if err != nil {
		return domain.AgyAccountSwitch{}, false, fmt.Errorf("get Agy account switch by idempotency key: %w", err)
	}
	return agyAccountSwitchFromGen(row), true, nil
}

// GetActiveAgyAccountSwitch reads the sole nonterminal switch.
func (s *Store) GetActiveAgyAccountSwitch(ctx context.Context) (domain.AgyAccountSwitch, bool, error) {
	row, err := s.qr.GetActiveAgyAccountSwitch(ctx)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.AgyAccountSwitch{}, false, nil
	}
	if err != nil {
		return domain.AgyAccountSwitch{}, false, fmt.Errorf("get active Agy account switch: %w", err)
	}
	return agyAccountSwitchFromGen(row), true, nil
}

// UpdateAgyAccountSwitch applies a compare-and-swap phase transition.
func (s *Store) UpdateAgyAccountSwitch(ctx context.Context, rec domain.AgyAccountSwitch, expected domain.AgyAccountSwitchPhase) (bool, error) {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	n, err := s.qw.UpdateAgyAccountSwitchPhase(ctx, gen.UpdateAgyAccountSwitchPhaseParams{
		NextPhase: string(rec.Phase), FailureCode: rec.FailureCode,
		CredentialsCommittedAt: timePtrToNull(rec.CredentialsCommittedAt),
		UpdatedAt:              rec.UpdatedAt.UTC(), CompletedAt: timePtrToNull(rec.CompletedAt),
		ID: rec.ID, ExpectedPhase: string(expected),
	})
	if err != nil {
		return false, fmt.Errorf("update Agy account switch %s: %w", rec.ID, err)
	}
	return n > 0, nil
}

func agyAccountSwitchFromGen(row gen.AgyAccountSwitch) domain.AgyAccountSwitch {
	return domain.AgyAccountSwitch{
		ID: row.ID, SourceKind: domain.AgyAccountSwitchSourceKind(row.SourceKind), SourceAccountID: row.SourceAccountID, TargetAccountID: row.TargetAccountID,
		Phase: domain.AgyAccountSwitchPhase(row.Phase), FailureCode: row.FailureCode,
		CredentialsCommittedAt: nullTimeToPtr(row.CredentialsCommittedAt),
		CreatedAt:              row.CreatedAt, UpdatedAt: row.UpdatedAt, CompletedAt: nullTimeToPtr(row.CompletedAt),
		IdempotencyKey: row.IdempotencyKey,
	}
}
